package service

import (
	"container/list"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/tidwall/gjson"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

const (
	affinityRedisPrefix      = "tokenrouter:channel_affinity:v1:entry:"
	affinityRedisIndex       = "tokenrouter:channel_affinity:v1:index"
	affinityUsageRedisPrefix = "tokenrouter:channel_affinity_usage:v1:entry:"
	affinityUsageRedisIndex  = "tokenrouter:channel_affinity_usage:v1:index"

	affinityModeCachedOverPrompt           = "cached_over_prompt"
	affinityModeCachedOverPromptPlusCached = "cached_over_prompt_plus_cached"
	affinityModeMixed                      = "mixed"

	affinityContextMetaKey      = "tokenrouter_channel_affinity_meta"
	affinityContextUsedKey      = "tokenrouter_channel_affinity_used"
	affinityContextSkipRetryKey = "tokenrouter_channel_affinity_skip_retry"
)

type channelAffinityMeta struct {
	CacheKey       string
	TTLSeconds     int
	RuleName       string
	SkipRetry      bool
	ParamTemplate  map[string]any
	KeySourceType  string
	KeySourceKey   string
	KeySourcePath  string
	KeyFingerprint string
	UsingGroup     string
	ModelName      string
	RequestPath    string
	InitialChannel int
}

type affinityCacheEntry struct {
	ChannelID int    `json:"channel_id"`
	RuleName  string `json:"rule_name"`
}

type ChannelAffinityCacheStats struct {
	Enabled       bool           `json:"enabled"`
	Total         int            `json:"total"`
	Unknown       int            `json:"unknown"`
	ByRuleName    map[string]int `json:"by_rule_name"`
	CacheCapacity int            `json:"cache_capacity"`
	CacheAlgo     string         `json:"cache_algo"`
}

type ChannelAffinityUsageCacheStats struct {
	RuleName             string `json:"rule_name"`
	UsingGroup           string `json:"using_group"`
	KeyFingerprint       string `json:"key_fp"`
	CachedTokenRateMode  string `json:"cached_token_rate_mode"`
	Hit                  int64  `json:"hit"`
	Total                int64  `json:"total"`
	WindowSeconds        int64  `json:"window_seconds"`
	PromptTokens         int64  `json:"prompt_tokens"`
	CompletionTokens     int64  `json:"completion_tokens"`
	TotalTokens          int64  `json:"total_tokens"`
	CachedTokens         int64  `json:"cached_tokens"`
	PromptCacheHitTokens int64  `json:"prompt_cache_hit_tokens"`
	LastSeenAt           int64  `json:"last_seen_at"`
}

type ttlCacheItem[T any] struct {
	key       string
	value     T
	expiresAt time.Time
}

type boundedTTLCache[T any] struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

func newBoundedTTLCache[T any](capacity int) *boundedTTLCache[T] {
	return &boundedTTLCache[T]{capacity: capacity, items: make(map[string]*list.Element), order: list.New()}
}

func (cache *boundedTTLCache[T]) configure(capacity int) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.capacity = capacity
	cache.purgeExpiredLocked(time.Now())
	cache.evictLocked()
}

func (cache *boundedTTLCache[T]) get(key string) (T, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var zero T
	element, exists := cache.items[key]
	if !exists {
		return zero, false
	}
	item := element.Value.(*ttlCacheItem[T])
	if !item.expiresAt.IsZero() && !time.Now().Before(item.expiresAt) {
		cache.removeLocked(element)
		return zero, false
	}
	cache.order.MoveToFront(element)
	return item.value, true
}

func (cache *boundedTTLCache[T]) set(key string, value T, ttl time.Duration) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, exists := cache.items[key]; exists {
		item := element.Value.(*ttlCacheItem[T])
		item.value = value
		item.expiresAt = time.Now().Add(ttl)
		cache.order.MoveToFront(element)
	} else {
		item := &ttlCacheItem[T]{key: key, value: value, expiresAt: time.Now().Add(ttl)}
		cache.items[key] = cache.order.PushFront(item)
	}
	cache.purgeExpiredLocked(time.Now())
	cache.evictLocked()
}

func (cache *boundedTTLCache[T]) delete(key string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, exists := cache.items[key]
	if exists {
		cache.removeLocked(element)
	}
	return exists
}

func (cache *boundedTTLCache[T]) clearWhere(predicate func(T) bool) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.purgeExpiredLocked(time.Now())
	deleted := 0
	for element := cache.order.Back(); element != nil; {
		previous := element.Prev()
		if predicate(element.Value.(*ttlCacheItem[T]).value) {
			cache.removeLocked(element)
			deleted++
		}
		element = previous
	}
	return deleted
}

func (cache *boundedTTLCache[T]) snapshot() map[string]T {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.purgeExpiredLocked(time.Now())
	result := make(map[string]T, len(cache.items))
	for key, element := range cache.items {
		result[key] = element.Value.(*ttlCacheItem[T]).value
	}
	return result
}

func (cache *boundedTTLCache[T]) purgeExpiredLocked(now time.Time) {
	for element := cache.order.Back(); element != nil; {
		previous := element.Prev()
		item := element.Value.(*ttlCacheItem[T])
		if !item.expiresAt.IsZero() && !now.Before(item.expiresAt) {
			cache.removeLocked(element)
		}
		element = previous
	}
}

func (cache *boundedTTLCache[T]) evictLocked() {
	capacity := cache.capacity
	if capacity <= 0 {
		capacity = setting.DefaultChannelAffinityMaxEntries
	}
	for len(cache.items) > capacity {
		cache.removeLocked(cache.order.Back())
	}
}

func (cache *boundedTTLCache[T]) removeLocked(element *list.Element) {
	if element == nil {
		return
	}
	delete(cache.items, element.Value.(*ttlCacheItem[T]).key)
	cache.order.Remove(element)
}

var (
	affinityMemory              = newBoundedTTLCache[affinityCacheEntry](setting.DefaultChannelAffinityMaxEntries)
	affinityUsageMemory         = newBoundedTTLCache[ChannelAffinityUsageCacheStats](setting.DefaultChannelAffinityMaxEntries)
	affinityUsageMemoryUpdateMu sync.Mutex
	affinityRegexCache          sync.Map
)

func GetPreferredChannelByAffinity(c *gin.Context, modelName, usingGroup string, body []byte) (int, bool) {
	config := setting.GetChannelAffinitySetting()
	if !config.Enabled || c == nil {
		return 0, false
	}
	path := ""
	userAgent := ""
	if c.Request != nil {
		userAgent = c.Request.UserAgent()
		if c.Request.URL != nil {
			path = c.Request.URL.Path
		}
	}
	for _, rule := range config.Rules {
		if !matchesAnyRegex(rule.ModelRegex, modelName) {
			continue
		}
		if len(rule.PathRegex) > 0 && !matchesAnyRegex(rule.PathRegex, path) {
			continue
		}
		if len(rule.UserAgentInclude) > 0 && !containsAnyFold(rule.UserAgentInclude, userAgent) {
			continue
		}
		value := ""
		var source setting.ChannelAffinityKeySource
		for _, candidate := range rule.KeySources {
			value = extractChannelAffinityValue(c, body, candidate)
			if value != "" {
				source = candidate
				break
			}
		}
		if value == "" || (rule.ValueRegex != "" && !matchesAnyRegex([]string{rule.ValueRegex}, value)) {
			continue
		}
		ttlSeconds := rule.TTLSeconds
		if ttlSeconds <= 0 {
			ttlSeconds = config.DefaultTTLSeconds
		}
		meta := channelAffinityMeta{
			CacheKey:       affinityCacheKey(rule, modelName, usingGroup, value),
			TTLSeconds:     ttlSeconds,
			RuleName:       rule.Name,
			SkipRetry:      rule.SkipRetryOnFailure,
			ParamTemplate:  cloneAnyMap(rule.ParamOverrideTemplate),
			KeySourceType:  source.Type,
			KeySourceKey:   source.Key,
			KeySourcePath:  source.Path,
			KeyFingerprint: affinityFingerprint(value),
			UsingGroup:     usingGroup,
			ModelName:      modelName,
			RequestPath:    path,
		}
		c.Set(affinityContextMetaKey, meta)
		entry, found := getAffinityEntry(meta.CacheKey, config.MaxEntries)
		if found && entry.ChannelID > 0 {
			return entry.ChannelID, true
		}
		return 0, false
	}
	return 0, false
}

func extractChannelAffinityValue(c *gin.Context, body []byte, source setting.ChannelAffinityKeySource) string {
	switch source.Type {
	case "context_int":
		value := c.GetInt(source.Key)
		if value > 0 {
			return strconv.Itoa(value)
		}
	case "context_string":
		return strings.TrimSpace(c.GetString(source.Key))
	case "request_header":
		if c.Request != nil {
			return strings.TrimSpace(c.Request.Header.Get(source.Key))
		}
	case "gjson":
		result := gjson.GetBytes(body, source.Path)
		if result.Exists() {
			if result.Type == gjson.JSON {
				return strings.TrimSpace(result.Raw)
			}
			return strings.TrimSpace(result.String())
		}
	}
	return ""
}

func matchesAnyRegex(patterns []string, value string) bool {
	if len(patterns) == 0 || value == "" {
		return false
	}
	for _, pattern := range patterns {
		cached, exists := affinityRegexCache.Load(pattern)
		if !exists {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				continue
			}
			cached, _ = affinityRegexCache.LoadOrStore(pattern, compiled)
		}
		if cached.(*regexp.Regexp).MatchString(value) {
			return true
		}
	}
	return false
}

func containsAnyFold(parts []string, value string) bool {
	value = strings.ToLower(value)
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" && strings.Contains(value, strings.ToLower(part)) {
			return true
		}
	}
	return false
}

func affinityCacheKey(rule setting.ChannelAffinityRule, modelName, usingGroup, value string) string {
	parts := make([]string, 0, 4)
	if rule.IncludeRuleName {
		parts = append(parts, rule.Name)
	}
	if rule.IncludeModelName {
		parts = append(parts, modelName)
	}
	if rule.IncludeUsingGroup {
		parts = append(parts, usingGroup)
	}
	parts = append(parts, value)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func affinityFingerprint(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

func getAffinityMeta(c *gin.Context) (channelAffinityMeta, bool) {
	if c == nil {
		return channelAffinityMeta{}, false
	}
	value, exists := c.Get(affinityContextMetaKey)
	if !exists {
		return channelAffinityMeta{}, false
	}
	meta, ok := value.(channelAffinityMeta)
	return meta, ok
}

func MarkChannelAffinityUsed(c *gin.Context, selectedGroup string, channelID int) {
	meta, ok := getAffinityMeta(c)
	if !ok || channelID <= 0 {
		return
	}
	meta.InitialChannel = channelID
	c.Set(affinityContextMetaKey, meta)
	c.Set(affinityContextUsedKey, true)
	c.Set(affinityContextSkipRetryKey, meta.SkipRetry)
}

func ShouldSkipRetryAfterChannelAffinityFailure(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if value, exists := c.Get(affinityContextSkipRetryKey); exists {
		if skip, ok := value.(bool); ok {
			return skip
		}
	}
	meta, ok := getAffinityMeta(c)
	return ok && meta.SkipRetry
}

func ShouldKeepChannelAffinityOnChannelDisabled() bool {
	return setting.GetChannelAffinitySetting().KeepOnChannelDisabled
}

func ClearCurrentChannelAffinityCache(c *gin.Context) bool {
	meta, ok := getAffinityMeta(c)
	if !ok || meta.CacheKey == "" {
		return false
	}
	deleted := deleteAffinityEntry(meta.CacheKey)
	c.Set(affinityContextSkipRetryKey, false)
	return deleted
}

func RecordChannelAffinity(c *gin.Context, initialChannelID, successfulChannelID int) {
	meta, ok := getAffinityMeta(c)
	if !ok || successfulChannelID <= 0 {
		return
	}
	config := setting.GetChannelAffinitySetting()
	if !config.Enabled {
		return
	}
	channelID := initialChannelID
	if config.SwitchOnSuccess || channelID <= 0 {
		channelID = successfulChannelID
	}
	ttlSeconds := meta.TTLSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = config.DefaultTTLSeconds
	}
	setAffinityEntry(meta.CacheKey, affinityCacheEntry{ChannelID: channelID, RuleName: meta.RuleName}, time.Duration(ttlSeconds)*time.Second, config.MaxEntries)
}

func GetChannelAffinityCacheStats() ChannelAffinityCacheStats {
	config := setting.GetChannelAffinitySetting()
	entries := affinityEntries(config.MaxEntries)
	byRule := make(map[string]int)
	countable := make(map[string]bool)
	for _, rule := range config.Rules {
		if rule.IncludeRuleName {
			byRule[rule.Name] = 0
			countable[rule.Name] = true
		}
	}
	unknown := 0
	for _, entry := range entries {
		if countable[entry.RuleName] {
			byRule[entry.RuleName]++
		} else {
			unknown++
		}
	}
	return ChannelAffinityCacheStats{
		Enabled: config.Enabled, Total: len(entries), Unknown: unknown, ByRuleName: byRule,
		CacheCapacity: config.MaxEntries, CacheAlgo: "LRU",
	}
}

func ClearChannelAffinityCacheAll() int {
	return clearAffinityEntries(func(affinityCacheEntry) bool { return true })
}

func ClearChannelAffinityCacheByRuleName(ruleName string) (int, error) {
	ruleName = strings.TrimSpace(ruleName)
	if ruleName == "" {
		return 0, fmt.Errorf("rule_name 不能为空")
	}
	config := setting.GetChannelAffinitySetting()
	for _, rule := range config.Rules {
		if rule.Name != ruleName {
			continue
		}
		if !rule.IncludeRuleName {
			return 0, fmt.Errorf("该规则未启用 include_rule_name，无法按规则清空缓存")
		}
		return clearAffinityEntries(func(entry affinityCacheEntry) bool { return entry.RuleName == ruleName }), nil
	}
	return 0, fmt.Errorf("未知规则名称")
}

func ApplyChannelAffinityRequestHeaders(c *gin.Context, request *http.Request) {
	meta, ok := getAffinityMeta(c)
	if !ok || request == nil || c.Request == nil {
		return
	}
	operations, ok := meta.ParamTemplate["operations"].([]any)
	if !ok {
		return
	}
	for _, rawOperation := range operations {
		operation, ok := rawOperation.(map[string]any)
		if !ok || operation["mode"] != "pass_headers" {
			continue
		}
		values, ok := operation["value"].([]any)
		if !ok {
			continue
		}
		for _, rawHeader := range values {
			header, ok := rawHeader.(string)
			if !ok || header == "" {
				continue
			}
			if value := c.Request.Header.Get(header); value != "" {
				request.Header.Set(header, value)
			}
		}
	}
}

func AppendChannelAffinityAdminInfo(c *gin.Context, adminInfo map[string]any) {
	meta, ok := getAffinityMeta(c)
	if !ok || adminInfo == nil {
		return
	}
	used, _ := c.Get(affinityContextUsedKey)
	adminInfo["channel_affinity"] = map[string]any{
		"rule_name": meta.RuleName, "using_group": meta.UsingGroup, "model": meta.ModelName,
		"request_path": meta.RequestPath, "key_source": meta.KeySourceType,
		"key_key": meta.KeySourceKey, "key_path": meta.KeySourcePath,
		"key_fp": meta.KeyFingerprint, "cache_hit": used == true,
	}
}

func ObserveChannelAffinityUsage(c *gin.Context, usage *protocolkit.Usage, format constant.RelayFormat) {
	meta, ok := getAffinityMeta(c)
	if !ok || meta.RuleName == "" || meta.KeyFingerprint == "" || meta.TTLSeconds <= 0 {
		return
	}
	mode := affinityUsageMode(format)
	cachedTokens, promptCacheHitTokens := usageCacheSignals(usage)
	stats := ChannelAffinityUsageCacheStats{
		RuleName: meta.RuleName, UsingGroup: meta.UsingGroup, KeyFingerprint: meta.KeyFingerprint,
		CachedTokenRateMode: mode, Total: 1, WindowSeconds: int64(meta.TTLSeconds),
		CachedTokens: cachedTokens, PromptCacheHitTokens: promptCacheHitTokens, LastSeenAt: time.Now().Unix(),
	}
	if cachedTokens > 0 || promptCacheHitTokens > 0 {
		stats.Hit = 1
	}
	if usage != nil {
		stats.PromptTokens = int64(usage.PromptTokens)
		stats.CompletionTokens = int64(usage.CompletionTokens)
		stats.TotalTokens = int64(usage.TotalTokens)
		if stats.TotalTokens == 0 {
			stats.TotalTokens = stats.PromptTokens + stats.CompletionTokens
		}
	}
	observeAffinityUsage(stats, time.Duration(meta.TTLSeconds)*time.Second, setting.GetChannelAffinitySetting().MaxEntries)
}

func GetChannelAffinityUsageCacheStats(ruleName, usingGroup, keyFingerprint string) ChannelAffinityUsageCacheStats {
	ruleName = strings.TrimSpace(ruleName)
	usingGroup = strings.TrimSpace(usingGroup)
	keyFingerprint = strings.TrimSpace(keyFingerprint)
	empty := ChannelAffinityUsageCacheStats{RuleName: ruleName, UsingGroup: usingGroup, KeyFingerprint: keyFingerprint}
	if ruleName == "" || keyFingerprint == "" {
		return empty
	}
	key := affinityUsageKey(ruleName, usingGroup, keyFingerprint)
	if redisAffinityEnabled() {
		values, err := common.RedisClient.HGetAll(context.Background(), affinityUsageRedisPrefix+key).Result()
		if err == nil {
			if len(values) == 0 {
				return empty
			}
			return affinityUsageStatsFromStrings(ruleName, usingGroup, keyFingerprint, values)
		}
		common.SysError("channel affinity usage cache read failed: " + err.Error())
	}
	if stats, found := affinityUsageMemory.get(key); found {
		return stats
	}
	return empty
}

func affinityUsageMode(format constant.RelayFormat) string {
	switch format {
	case constant.RelayFormatClaude:
		return affinityModeCachedOverPromptPlusCached
	case constant.RelayFormatOpenAI, constant.RelayFormatOpenAIResponses,
		constant.RelayFormatOpenAIResponsesCompaction, constant.RelayFormatOpenAIAlphaSearch:
		return affinityModeCachedOverPrompt
	default:
		return ""
	}
}

func usageCacheSignals(usage *protocolkit.Usage) (int64, int64) {
	if usage == nil {
		return 0, 0
	}
	cached := int64(0)
	if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
		cached = int64(usage.PromptTokensDetails.CachedTokens)
	}
	promptCacheHit := int64(usage.PromptCacheHitTokens)
	if cached == 0 && promptCacheHit > 0 {
		cached = promptCacheHit
	}
	return cached, promptCacheHit
}

func affinityUsageKey(ruleName, usingGroup, keyFingerprint string) string {
	sum := sha256.Sum256([]byte(ruleName + "\x00" + usingGroup + "\x00" + keyFingerprint))
	return hex.EncodeToString(sum[:])
}

func cloneAnyMap(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	encoded, _ := common.Marshal(source)
	var cloned map[string]any
	_ = common.Unmarshal(encoded, &cloned)
	return cloned
}

func redisAffinityEnabled() bool {
	return common.RedisEnabled && common.RedisClient != nil
}

func getAffinityEntry(key string, capacity int) (affinityCacheEntry, bool) {
	if redisAffinityEnabled() {
		raw, err := common.RedisClient.Get(context.Background(), affinityRedisPrefix+key).Result()
		if err == redis.Nil {
			return affinityCacheEntry{}, false
		}
		if err == nil {
			var entry affinityCacheEntry
			if common.UnmarshalJsonStr(raw, &entry) == nil {
				return entry, true
			}
			_ = common.RedisClient.Del(context.Background(), affinityRedisPrefix+key).Err()
			return affinityCacheEntry{}, false
		}
		common.SysError("channel affinity cache read failed: " + err.Error())
	}
	affinityMemory.configure(capacity)
	return affinityMemory.get(key)
}

func setAffinityEntry(key string, entry affinityCacheEntry, ttl time.Duration, capacity int) {
	if ttl <= 0 {
		return
	}
	if redisAffinityEnabled() {
		encoded, err := common.Marshal(entry)
		if err == nil {
			now := time.Now()
			_, err = common.RedisClient.Eval(context.Background(), affinitySetScript,
				[]string{affinityRedisPrefix + key, affinityRedisIndex},
				string(encoded), ttl.Milliseconds(), now.Add(ttl).UnixMilli(), key,
				now.UnixMilli(), capacity, affinityRedisPrefix).Result()
		}
		if err == nil {
			return
		}
		common.SysError("channel affinity cache write failed: " + err.Error())
	}
	affinityMemory.configure(capacity)
	affinityMemory.set(key, entry, ttl)
}

func deleteAffinityEntry(key string) bool {
	deleted := affinityMemory.delete(key)
	if redisAffinityEnabled() {
		pipe := common.RedisClient.TxPipeline()
		deleteCommand := pipe.Del(context.Background(), affinityRedisPrefix+key)
		pipe.ZRem(context.Background(), affinityRedisIndex, key)
		_, err := pipe.Exec(context.Background())
		if err != nil {
			common.SysError("channel affinity cache delete failed: " + err.Error())
		} else if deleteCommand.Val() > 0 {
			deleted = true
		}
	}
	return deleted
}

func affinityEntries(capacity int) map[string]affinityCacheEntry {
	if redisAffinityEnabled() {
		entries, err := redisAffinityEntries()
		if err == nil {
			return entries
		}
		common.SysError("channel affinity cache stats failed: " + err.Error())
	}
	affinityMemory.configure(capacity)
	return affinityMemory.snapshot()
}

func redisAffinityEntries() (map[string]affinityCacheEntry, error) {
	ctx := context.Background()
	if err := common.RedisClient.ZRemRangeByScore(ctx, affinityRedisIndex, "-inf", strconv.FormatInt(time.Now().UnixMilli(), 10)).Err(); err != nil {
		return nil, err
	}
	members, err := common.RedisClient.ZRange(ctx, affinityRedisIndex, 0, -1).Result()
	if err != nil || len(members) == 0 {
		return map[string]affinityCacheEntry{}, err
	}
	keys := make([]string, len(members))
	for i, member := range members {
		keys[i] = affinityRedisPrefix + member
	}
	values, err := common.RedisClient.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]affinityCacheEntry, len(values))
	stale := make([]any, 0)
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			stale = append(stale, members[i])
			continue
		}
		var entry affinityCacheEntry
		if common.UnmarshalJsonStr(raw, &entry) != nil {
			stale = append(stale, members[i])
			continue
		}
		entries[members[i]] = entry
	}
	if len(stale) > 0 {
		_ = common.RedisClient.ZRem(ctx, affinityRedisIndex, stale...).Err()
	}
	return entries, nil
}

func clearAffinityEntries(predicate func(affinityCacheEntry) bool) int {
	deleted := affinityMemory.clearWhere(predicate)
	if !redisAffinityEnabled() {
		return deleted
	}
	entries, err := redisAffinityEntries()
	if err != nil {
		common.SysError("channel affinity cache list for clear failed: " + err.Error())
		return deleted
	}
	members := make([]string, 0)
	keys := make([]string, 0)
	for member, entry := range entries {
		if predicate(entry) {
			members = append(members, member)
			keys = append(keys, affinityRedisPrefix+member)
		}
	}
	if len(members) == 0 {
		return deleted
	}
	pipe := common.RedisClient.TxPipeline()
	pipe.Del(context.Background(), keys...)
	values := make([]any, len(members))
	for i := range members {
		values[i] = members[i]
	}
	pipe.ZRem(context.Background(), affinityRedisIndex, values...)
	if _, err := pipe.Exec(context.Background()); err != nil {
		common.SysError("channel affinity cache clear failed: " + err.Error())
		return deleted
	}
	return deleted + len(members)
}

func observeAffinityUsage(delta ChannelAffinityUsageCacheStats, ttl time.Duration, capacity int) {
	key := affinityUsageKey(delta.RuleName, delta.UsingGroup, delta.KeyFingerprint)
	if redisAffinityEnabled() {
		now := time.Now()
		_, err := common.RedisClient.Eval(context.Background(), affinityUsageObserveScript,
			[]string{affinityUsageRedisPrefix + key, affinityUsageRedisIndex},
			delta.CachedTokenRateMode, delta.Hit, delta.Total, delta.PromptTokens,
			delta.CompletionTokens, delta.TotalTokens, delta.CachedTokens,
			delta.PromptCacheHitTokens, delta.WindowSeconds, delta.LastSeenAt,
			delta.RuleName, delta.UsingGroup, delta.KeyFingerprint, int64(ttl/time.Second),
			now.Add(ttl).UnixMilli(), now.UnixMilli(), key, capacity, affinityUsageRedisPrefix).Result()
		if err == nil {
			return
		}
		common.SysError("channel affinity usage cache write failed: " + err.Error())
	}
	affinityUsageMemoryUpdateMu.Lock()
	defer affinityUsageMemoryUpdateMu.Unlock()
	affinityUsageMemory.configure(capacity)
	previous, _ := affinityUsageMemory.get(key)
	if previous.RuleName == "" {
		previous = delta
	} else {
		previous.CachedTokenRateMode = mergeAffinityUsageMode(previous.CachedTokenRateMode, delta.CachedTokenRateMode)
		previous.Hit += delta.Hit
		previous.Total += delta.Total
		previous.WindowSeconds = delta.WindowSeconds
		previous.PromptTokens += delta.PromptTokens
		previous.CompletionTokens += delta.CompletionTokens
		previous.TotalTokens += delta.TotalTokens
		previous.CachedTokens += delta.CachedTokens
		previous.PromptCacheHitTokens += delta.PromptCacheHitTokens
		previous.LastSeenAt = delta.LastSeenAt
	}
	affinityUsageMemory.set(key, previous, ttl)
}

func mergeAffinityUsageMode(current, incoming string) string {
	if incoming == "" {
		return current
	}
	if current == "" || current == incoming {
		return incoming
	}
	return affinityModeMixed
}

func affinityUsageStatsFromStrings(ruleName, usingGroup, keyFingerprint string, values map[string]string) ChannelAffinityUsageCacheStats {
	parse := func(key string) int64 {
		value, _ := strconv.ParseInt(values[key], 10, 64)
		return value
	}
	return ChannelAffinityUsageCacheStats{
		RuleName: ruleName, UsingGroup: usingGroup, KeyFingerprint: keyFingerprint,
		CachedTokenRateMode: values["cached_token_rate_mode"], Hit: parse("hit"), Total: parse("total"),
		WindowSeconds: parse("window_seconds"), PromptTokens: parse("prompt_tokens"),
		CompletionTokens: parse("completion_tokens"), TotalTokens: parse("total_tokens"),
		CachedTokens: parse("cached_tokens"), PromptCacheHitTokens: parse("prompt_cache_hit_tokens"),
		LastSeenAt: parse("last_seen_at"),
	}
}

const affinitySetScript = `
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[5])
local size = redis.call('ZCARD', KEYS[2])
local maximum = tonumber(ARGV[6])
if maximum > 0 and size > maximum then
  local stale = redis.call('ZRANGE', KEYS[2], 0, size - maximum - 1)
  for _, member in ipairs(stale) do
    redis.call('DEL', ARGV[7] .. member)
    redis.call('ZREM', KEYS[2], member)
  end
end
return 1
`

const affinityUsageObserveScript = `
local current = redis.call('HGET', KEYS[1], 'cached_token_rate_mode') or ''
local incoming = ARGV[1]
if incoming ~= '' then
  if current == '' or current == incoming then current = incoming
  elseif current ~= 'mixed' then current = 'mixed' end
end
redis.call('HSET', KEYS[1],
  'cached_token_rate_mode', current,
  'window_seconds', ARGV[9], 'last_seen_at', ARGV[10],
  'rule_name', ARGV[11], 'using_group', ARGV[12], 'key_fp', ARGV[13])
redis.call('HINCRBY', KEYS[1], 'hit', ARGV[2])
redis.call('HINCRBY', KEYS[1], 'total', ARGV[3])
redis.call('HINCRBY', KEYS[1], 'prompt_tokens', ARGV[4])
redis.call('HINCRBY', KEYS[1], 'completion_tokens', ARGV[5])
redis.call('HINCRBY', KEYS[1], 'total_tokens', ARGV[6])
redis.call('HINCRBY', KEYS[1], 'cached_tokens', ARGV[7])
redis.call('HINCRBY', KEYS[1], 'prompt_cache_hit_tokens', ARGV[8])
redis.call('EXPIRE', KEYS[1], ARGV[14])
redis.call('ZADD', KEYS[2], ARGV[15], ARGV[17])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[16])
local size = redis.call('ZCARD', KEYS[2])
local maximum = tonumber(ARGV[18])
if maximum > 0 and size > maximum then
  local stale = redis.call('ZRANGE', KEYS[2], 0, size - maximum - 1)
  for _, member in ipairs(stale) do
    redis.call('DEL', ARGV[19] .. member)
    redis.call('ZREM', KEYS[2], member)
  end
end
return 1
`
