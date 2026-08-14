package service

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/setting"
)

func configureAffinityTest(t *testing.T, maxEntries int, rules []setting.ChannelAffinityRule) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	require.NoError(t, setting.Init())
	encoded, err := common.Marshal(rules)
	require.NoError(t, err)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ChannelAffinityEnabledOption:           "true",
		setting.ChannelAffinitySwitchOnSuccessOption:   "true",
		setting.ChannelAffinityKeepOnDisabledOption:    "false",
		setting.ChannelAffinityMaxEntriesOption:        common.Int2Str(maxEntries),
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
	initTestDB(t)
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
	initTestDB(t)
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
	initTestDB(t)
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
	initTestDB(t)
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
			format := constant.RelayFormatOpenAI
			if index%2 == 1 {
				format = constant.RelayFormatClaude
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
	initTestDB(t)
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
