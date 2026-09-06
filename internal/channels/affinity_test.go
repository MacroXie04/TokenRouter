package channels

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cache"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/textutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func configureAffinityTest(t *testing.T, maxEntries int, rules []setting.ChannelAffinityRule) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	require.NoError(t, setting.Init())
	encoded, err := jsonutil.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption:           "true",
		setting.ChannelAffinitySwitchOnSuccessOption:   "true",
		setting.ChannelAffinityKeepOnDisabledOption:    "false",
		setting.ChannelAffinityMaxEntriesOption:        textutil.Int2Str(maxEntries),
		setting.ChannelAffinityDefaultTTLSecondsOption: "60",
		setting.ChannelAffinityRulesOption:             string(encoded),
	}))
	affinityMemory = newBoundedTTLCache[affinityCacheEntry](maxEntries)
	affinityUsageMemory = newBoundedTTLCache[ChannelAffinityUsageCacheStats](maxEntries)
}

func affinityHeaderRule(name string) setting.ChannelAffinityRule {
	return setting.ChannelAffinityRule{
		Name:       name,
		ModelRegex: []string{"^model-"},
		PathRegex:  []string{"^/v1/responses$"},
		KeySources: []setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Affinity-Key"}},
		TTLSeconds: 60, SkipRetryOnFailure: true,
		IncludeRuleName: true, IncludeModelName: true, IncludeUsingGroup: true,
	}
}

func affinityContext(key string) *gin.Context {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a"}`))
	ctx.Request.Header.Set("X-Affinity-Key", key)
	return ctx
}

func TestRuleBasedChannelAffinityRoutingAndPrivacy(t *testing.T) {
	testutil.InitTestDB(t)
	configureAffinityTest(t, 10, []setting.ChannelAffinityRule{affinityHeaderRule("header-rule")})

	first := affinityContext("private-session-123")
	preferred, found := GetPreferredChannelByAffinity(first, "model-a", "default", []byte(`{"model":"model-a"}`))
	assert.False(t, found)
	assert.Zero(t, preferred)
	RecordChannelAffinity(first, 41, 41)

	entries := affinityMemory.snapshot()
	require.Len(t, entries, 1)
	for key := range entries {
		assert.NotContains(t, key, "private-session-123")
	}

	second := affinityContext("private-session-123")
	preferred, found = GetPreferredChannelByAffinity(second, "model-a", "default", []byte(`{"model":"model-a"}`))
	require.True(t, found)
	assert.Equal(t, 41, preferred)
	MarkChannelAffinityUsed(second, "default", preferred)
	assert.True(t, ShouldSkipRetryAfterChannelAffinityFailure(second))

	meta, ok := getAffinityMeta(second)
	require.True(t, ok)
	assert.Len(t, meta.KeyFingerprint, 8)
	assert.NotContains(t, meta.CacheKey, "private-session-123")

	otherGroup := affinityContext("private-session-123")
	_, found = GetPreferredChannelByAffinity(otherGroup, "model-a", "vip", []byte(`{"model":"model-a"}`))
	assert.False(t, found, "include_using_group must isolate cache entries")
}

func TestChannelAffinityCapacityExpiryAndClear(t *testing.T) {
	testutil.InitTestDB(t)
	rule := affinityHeaderRule("bounded-rule")
	configureAffinityTest(t, 2, []setting.ChannelAffinityRule{rule})

	for i, key := range []string{"one", "two", "three"} {
		ctx := affinityContext(key)
		_, _ = GetPreferredChannelByAffinity(ctx, "model-a", "default", nil)
		RecordChannelAffinity(ctx, i+1, i+1)
	}
	stats := GetChannelAffinityCacheStats()
	assert.Equal(t, 2, stats.Total)
	assert.Equal(t, 2, stats.ByRuleName["bounded-rule"])
	assert.Equal(t, 2, stats.CacheCapacity)
	assert.Equal(t, "LRU", stats.CacheAlgo)

	deleted, err := ClearChannelAffinityCacheByRuleName("bounded-rule")
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.Zero(t, GetChannelAffinityCacheStats().Total)

	expired := affinityContext("expired")
	_, _ = GetPreferredChannelByAffinity(expired, "model-a", "default", nil)
	meta, ok := getAffinityMeta(expired)
	require.True(t, ok)
	affinityMemory.set(meta.CacheKey, affinityCacheEntry{ChannelID: 9, RuleName: rule.Name}, time.Nanosecond)
	time.Sleep(time.Millisecond)
	_, found := GetPreferredChannelByAffinity(affinityContext("expired"), "model-a", "default", nil)
	assert.False(t, found)

	_, err = ClearChannelAffinityCacheByRuleName("missing")
	assert.EqualError(t, err, "未知规则名称")
}

func TestPreferredChannelMustRemainEligible(t *testing.T) {
	testutil.InitTestDB(t)
	channels := seedChannels(t, 2)
	high, low := int64(100), int64(1)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "model-a", ChannelId: channels[0].Id, Enabled: true, Priority: &high, Weight: 1}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "model-a", ChannelId: channels[1].Id, Enabled: true, Priority: &low, Weight: 1}).Error)
	require.NoError(t, InitAbilityCache())

	selected, used, err := GetSatisfiedChannelWithPreferred("default", "model-a", channels[1].Id, nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.True(t, used)
	assert.Equal(t, channels[1].Id, selected.Id, "eligible affinity overrides ordinary priority")

	ignored := map[int]struct{}{channels[1].Id: {}}
	selected, used, err = GetSatisfiedChannelWithPreferred("default", "model-a", channels[1].Id, ignored, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.False(t, used)
	assert.Equal(t, channels[0].Id, selected.Id)

	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channels[1].Id).Update("enabled", false).Error)
	require.NoError(t, InitAbilityCache())
	selected, used, err = GetSatisfiedChannelWithPreferred("default", "model-a", channels[1].Id, nil, rand.New(rand.NewSource(1)))
	require.NoError(t, err)
	assert.False(t, used)
	assert.Equal(t, channels[0].Id, selected.Id, "a channel without an enabled ability cannot satisfy affinity")
}

func TestChannelAffinityUsageCacheConcurrentAggregation(t *testing.T) {
	testutil.InitTestDB(t)
	configureAffinityTest(t, 10, []setting.ChannelAffinityRule{affinityHeaderRule("usage-rule")})
	ctx := affinityContext("usage-session")
	_, _ = GetPreferredChannelByAffinity(ctx, "model-a", "default", nil)
	meta, ok := getAffinityMeta(ctx)
	require.True(t, ok)

	usage := &protocolkit.Usage{
		PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140,
		PromptCacheHitTokens: 30,
		PromptTokensDetails:  &protocolkit.InputTokenDetails{CachedTokens: 30},
	}
	const workers = 32
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			format := channelcatalog.RelayFormatOpenAI
			if index%2 == 1 {
				format = channelcatalog.RelayFormatClaude
			}
			ObserveChannelAffinityUsage(ctx, usage, format)
		}(i)
	}
	wait.Wait()

	stats := GetChannelAffinityUsageCacheStats("usage-rule", "default", meta.KeyFingerprint)
	assert.EqualValues(t, workers, stats.Total)
	assert.EqualValues(t, workers, stats.Hit)
	assert.EqualValues(t, workers*100, stats.PromptTokens)
	assert.EqualValues(t, workers*30, stats.CachedTokens)
	assert.Equal(t, affinityModeMixed, stats.CachedTokenRateMode)
	assert.EqualValues(t, 60, stats.WindowSeconds)
	assert.NotZero(t, stats.LastSeenAt)
}

func TestChannelAffinityPassHeadersTemplate(t *testing.T) {
	testutil.InitTestDB(t)
	rule := affinityHeaderRule("headers-rule")
	rule.ParamOverrideTemplate = map[string]any{
		"operations": []any{map[string]any{"mode": "pass_headers", "value": []any{"X-Trace-ID"}}},
	}
	configureAffinityTest(t, 10, []setting.ChannelAffinityRule{rule})
	ctx := affinityContext("header-session")
	ctx.Request.Header.Set("X-Trace-ID", "trace-123")
	_, _ = GetPreferredChannelByAffinity(ctx, "model-a", "default", nil)

	outbound := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/responses", nil)
	ApplyChannelAffinityRequestHeaders(ctx, outbound)
	assert.Equal(t, "trace-123", outbound.Header.Get("X-Trace-ID"))
}

func TestChannelAffinityPassHeadersCannotOverwriteProviderCredentials(t *testing.T) {
	ctx := affinityContext("header-session")
	ctx.Request.Header.Set("Authorization", "Bearer user-secret")
	ctx.Request.Header.Set("X-Api-Key", "user-api-key")
	ctx.Request.Header.Set("Cookie", "dashboard_session=user-secret")
	ctx.Request.Header.Set("X-Auth", "user-auth-secret")
	ctx.Request.Header.Set("X-Token", "user-token-secret")
	ctx.Request.Header.Set("X-Signature", "user-signature-secret")
	ctx.Request.Header.Set("X-Upstream-Key", "attacker-controlled")
	ctx.Request.Header.Set("X-Trace-ID", "trace-123")
	ctx.Set(affinityContextMetaKey, channelAffinityMeta{ParamTemplate: map[string]any{
		"operations": []any{map[string]any{
			"mode":  "pass_headers",
			"value": []any{"Authorization", "X-Api-Key", "Cookie", "X-Auth", "X-Token", "X-Signature", "X-Upstream-Key", "X-Trace-ID"},
		}},
	}})

	outbound := httptest.NewRequest(http.MethodPost, "https://upstream.example/v1/responses", nil)
	outbound.Header.Set("Authorization", "Bearer provider-secret")
	outbound.Header.Set("X-Api-Key", "provider-api-key")
	outbound.Header.Set("X-Upstream-Key", "provider-custom-secret")
	ApplyChannelAffinityRequestHeaders(ctx, outbound)

	assert.Equal(t, "Bearer provider-secret", outbound.Header.Get("Authorization"))
	assert.Equal(t, "provider-api-key", outbound.Header.Get("X-Api-Key"))
	assert.Empty(t, outbound.Header.Get("Cookie"))
	assert.Empty(t, outbound.Header.Get("X-Auth"))
	assert.Empty(t, outbound.Header.Get("X-Token"))
	assert.Empty(t, outbound.Header.Get("X-Signature"))
	assert.Equal(t, "provider-custom-secret", outbound.Header.Get("X-Upstream-Key"))
	assert.Equal(t, "trace-123", outbound.Header.Get("X-Trace-ID"))
}

func TestBoundedTTLCacheUnchangedConfigureIsConstantWorkAndZeroDisables(t *testing.T) {
	cache := newBoundedTTLCache[int](2)
	cache.set("expired", 1, time.Hour)
	cache.mu.Lock()
	cache.items["expired"].Value.(*ttlCacheItem[int]).expiresAt = time.Now().Add(-time.Second)
	cache.mu.Unlock()

	cache.configure(2)
	cache.mu.Lock()
	assert.Len(t, cache.items, 1, "unchanged configure must not sweep the cache")
	cache.mu.Unlock()
	assert.Empty(t, cache.snapshot(), "explicit admin snapshots still sweep expiry")

	cache.set("present", 2, time.Hour)
	cache.configure(0)
	cache.mu.Lock()
	assert.Empty(t, cache.items, "capacity zero clears existing in-memory state")
	cache.mu.Unlock()
	cache.set("ignored", 3, time.Hour)
	_, found := cache.get("ignored")
	assert.False(t, found, "capacity zero rejects both writes and hits")
}

func TestChannelAffinityExplicitZeroDisablesChannelAndUsageStorage(t *testing.T) {
	testutil.InitTestDB(t)
	previousRedisEnabled := cache.RedisEnabled
	cache.RedisEnabled = false
	t.Cleanup(func() { cache.RedisEnabled = previousRedisEnabled })
	configureAffinityTest(t, 0, []setting.ChannelAffinityRule{affinityHeaderRule("disabled-storage")})

	ctx := affinityContext("zero-capacity-session")
	_, found := GetPreferredChannelByAffinity(ctx, "model-a", "default", nil)
	assert.False(t, found)
	RecordChannelAffinity(ctx, 11, 11)
	ObserveChannelAffinityUsage(ctx, &protocolkit.Usage{PromptTokens: 1, TotalTokens: 1}, channelcatalog.RelayFormatOpenAI)

	stats := GetChannelAffinityCacheStats()
	assert.False(t, stats.Enabled)
	assert.True(t, stats.Complete)
	assert.False(t, stats.Overflow)
	assert.Zero(t, stats.Total)
	assert.Empty(t, stats.ErrorCode)
	meta, ok := getAffinityMeta(ctx)
	require.True(t, ok)
	assert.Zero(t, GetChannelAffinityUsageCacheStats("disabled-storage", "default", meta.KeyFingerprint).Total)
}

func TestWalkAffinityRedisEntriesPagesAndDeletesWithoutSkipping(t *testing.T) {
	type stored struct {
		member string
		value  any
	}
	items := make([]stored, 600)
	for i := range items {
		entry := affinityCacheEntry{ChannelID: i + 1, RuleName: "keep"}
		if i%3 == 0 {
			entry.RuleName = "drop"
		}
		encoded, err := jsonutil.Marshal(entry)
		require.NoError(t, err)
		items[i] = stored{member: fmt.Sprintf("%064x", i+1), value: string(encoded)}
	}
	items[377].value = `{"channel_id":1,"channel_id":2,"rule_name":"keep"}`

	fetchCalls := 0
	largestFetch := 0
	fetch := func(_ context.Context, start, stop int64) ([]string, []any, error) {
		fetchCalls++
		requested := int(stop - start + 1)
		if requested > largestFetch {
			largestFetch = requested
		}
		if start >= int64(len(items)) {
			return nil, nil, nil
		}
		end := int(stop) + 1
		if end > len(items) {
			end = len(items)
		}
		page := items[int(start):end]
		members := make([]string, len(page))
		values := make([]any, len(page))
		for i := range page {
			members[i], values[i] = page[i].member, page[i].value
		}
		return members, values, nil
	}
	deleteChunks := make([]int, 0)
	remove := func(_ context.Context, members []string) error {
		deleteChunks = append(deleteChunks, len(members))
		selected := make(map[string]struct{}, len(members))
		for _, member := range members {
			selected[member] = struct{}{}
		}
		kept := items[:0]
		for _, item := range items {
			if _, removeItem := selected[item.member]; !removeItem {
				kept = append(kept, item)
			}
		}
		items = kept
		return nil
	}

	visited := 0
	err := walkAffinityRedisEntries(context.Background(), 600, 600, affinityRedisPageSize, fetch, remove,
		func(_ string, entry affinityCacheEntry) bool {
			visited++
			return entry.RuleName == "drop"
		})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, fetchCalls, 3)
	assert.LessOrEqual(t, largestFetch, affinityRedisPageSize)
	for _, size := range deleteChunks {
		assert.LessOrEqual(t, size, affinityRedisPageSize)
	}
	assert.Equal(t, 599, visited, "the malformed duplicate-key row is rejected before the visitor")
	assert.Len(t, items, 399)
	for _, item := range items {
		var entry affinityCacheEntry
		require.True(t, decodeAffinityCacheEntry(item.value.(string), &entry))
		assert.Equal(t, "keep", entry.RuleName)
	}
}

func TestWalkAffinityRedisEntriesFailsBeforeFetchOnOverflowOrCancellation(t *testing.T) {
	fetchCalls := 0
	fetch := func(context.Context, int64, int64) ([]string, []any, error) {
		fetchCalls++
		return nil, nil, nil
	}
	remove := func(context.Context, []string) error { return nil }
	visit := func(string, affinityCacheEntry) bool { return false }

	err := walkAffinityRedisEntries(context.Background(), int64(setting.MaxChannelAffinityEntries)+1,
		setting.MaxChannelAffinityEntries, affinityRedisPageSize, fetch, remove, visit)
	require.ErrorIs(t, err, ErrChannelAffinityCacheScanOverflow)
	assert.Zero(t, fetchCalls)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = walkAffinityRedisEntries(ctx, 1, 1, 1, fetch, remove, visit)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, fetchCalls)

	injected := errors.New("page failed")
	err = walkAffinityRedisEntries(context.Background(), 1, 1, 1,
		func(context.Context, int64, int64) ([]string, []any, error) { return nil, nil, injected }, remove, visit)
	require.ErrorIs(t, err, injected)
}

func TestChannelAffinityRedisScriptsBoundMaintenanceWork(t *testing.T) {
	for _, script := range []string{affinitySetScript, affinityUsageObserveScript} {
		assert.Contains(t, script, "LIMIT', 0, batch")
		assert.Contains(t, script, "if excess > batch then excess = batch end")
		assert.NotContains(t, script, "ZREMRANGEBYSCORE")
		assert.NotContains(t, script, "size - maximum - 1")
	}
}

func TestBoundedTTLCacheConcurrentConfigureAndAccess(t *testing.T) {
	cache := newBoundedTTLCache[int](64)
	const workers = 24
	const iterations = 200
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				capacity := 64
				if iteration%19 == 0 {
					capacity = 32
				}
				cache.configure(capacity)
				key := fmt.Sprintf("%d-%d", worker, iteration)
				cache.set(key, iteration, time.Minute)
				_, _ = cache.get(key)
			}
		}(worker)
	}
	wait.Wait()
	cache.configure(64)
	cache.mu.Lock()
	assert.LessOrEqual(t, len(cache.items), 64)
	cache.mu.Unlock()
}
