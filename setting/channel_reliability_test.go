package setting

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noReliabilityEnvironment(string) (string, bool) { return "", false }

func reliabilityEnvironment(values map[string]string) environmentLookup {
	return func(key string) (string, bool) {
		value, present := values[key]
		return value, present
	}
}

func TestChannelReliabilityDefaultsAreFailSafe(t *testing.T) {
	config, err := buildChannelReliabilitySettingWithEnv(nil, noReliabilityEnvironment)
	require.NoError(t, err)

	assert.Zero(t, config.RetryTimes)
	assert.False(t, config.AutomaticDisableChannelEnabled)
	assert.False(t, config.AutomaticEnableChannelEnabled)
	assert.False(t, config.AutoTestChannelEnabled)
	assert.Equal(t, DefaultAutoTestChannelMinutes, config.AutoTestChannelMinutes)
	assert.Equal(t, ChannelTestModeScheduledAll, config.ChannelTestMode)
	assert.Equal(t, DefaultChannelDisableThreshold, config.ChannelDisableThreshold)
	assert.Equal(t, "401", config.AutomaticDisableStatusCodesText)
	assert.Equal(t, defaultAutomaticRetryStatusCodes, config.AutomaticRetryStatusCodesText)
	assert.True(t, config.ShouldDisableForStatus(401))
	assert.False(t, config.ShouldDisableForStatus(500))
	assert.True(t, config.ShouldRetryForStatus(429))
	assert.True(t, config.ShouldRetryForStatus(599))
	assert.False(t, config.ShouldRetryForStatus(400))
	assert.False(t, config.ShouldRetryForStatus(504))
	assert.False(t, config.ShouldRetryForStatus(524))
	assert.True(t, config.MatchesAutomaticDisableKeyword("UPSTREAM: Permission Denied by provider"))

	defaults := ChannelReliabilityOptionDefaults()
	assert.Equal(t, "0", defaults[RetryTimesOption])
	assert.Equal(t, "false", defaults[AutoTestChannelEnabledOption])
	assert.Equal(t, "10", defaults[AutoTestChannelMinutesOption])
	assert.Equal(t, ChannelTestModeScheduledAll, defaults[ChannelTestModeOption])
	for key := range defaults {
		assert.True(t, IsChannelReliabilityOption(key), key)
	}
	assert.False(t, IsChannelReliabilityOption("not.a.reliability.option"))
}

func TestBuildChannelReliabilitySettingNormalizesBoundedValues(t *testing.T) {
	options := ChannelReliabilityOptionDefaults()
	options[RetryTimesOption] = "10"
	options[ChannelDisableThresholdOption] = "12.5"
	options[AutomaticDisableChannelEnabledOption] = "true"
	options[AutomaticEnableChannelEnabledOption] = "true"
	options[AutomaticDisableKeywordsOption] = "  Quota Exceeded  \r\nquota exceeded\r\n Disabled "
	options[AutomaticDisableStatusCodesOption] = " 500-502， 401, 402-404,403 "
	options[AutomaticRetryStatusCodesOption] = "429, 500-503, 502-505"
	options[AutoTestChannelEnabledOption] = "true"
	options[AutoTestChannelMinutesOption] = "525600"
	options[ChannelTestModeOption] = ChannelTestModePassiveRecovery

	config, err := buildChannelReliabilitySettingWithEnv(options, noReliabilityEnvironment)
	require.NoError(t, err)
	assert.Equal(t, 10, config.RetryTimes)
	assert.Equal(t, 12.5, config.ChannelDisableThreshold)
	assert.True(t, config.AutomaticDisableChannelEnabled)
	assert.True(t, config.AutomaticEnableChannelEnabled)
	assert.Equal(t, "Quota Exceeded\nDisabled", config.AutomaticDisableKeywordsText)
	assert.Equal(t, []string{"quota exceeded", "disabled"}, config.AutomaticDisableKeywords)
	assert.Equal(t, "401-404,500-502", config.AutomaticDisableStatusCodesText)
	assert.Equal(t, "429,500-505", config.AutomaticRetryStatusCodesText)
	assert.True(t, config.AutoTestChannelEnabled)
	assert.Equal(t, MaxAutoTestChannelMinutes, config.AutoTestChannelMinutes)
	assert.Equal(t, ChannelTestModePassiveRecovery, config.ChannelTestMode)
}

func TestBuildChannelReliabilitySettingRejectsMalformedPrimitiveOptions(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{RetryTimesOption, "-1"},
		{RetryTimesOption, "11"},
		{RetryTimesOption, "01"},
		{RetryTimesOption, " 1"},
		{ChannelDisableThresholdOption, "-0"},
		{ChannelDisableThresholdOption, "15.1"},
		{ChannelDisableThresholdOption, "NaN"},
		{ChannelDisableThresholdOption, "+Inf"},
		{AutomaticDisableChannelEnabledOption, "1"},
		{AutomaticEnableChannelEnabledOption, "TRUE"},
		{AutoTestChannelEnabledOption, ""},
		{AutoTestChannelMinutesOption, "0"},
		{AutoTestChannelMinutesOption, "525601"},
		{AutoTestChannelMinutesOption, "1.5"},
		{ChannelTestModeOption, "all"},
		{ChannelTestModeOption, " scheduled_all"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			_, err := buildChannelReliabilitySettingWithEnv(map[string]string{test.key: test.value}, noReliabilityEnvironment)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.key)
		})
	}
}

func TestChannelReliabilityEnvironmentPrecedenceIsCoherent(t *testing.T) {
	options := map[string]string{
		RetryTimesOption:             "2",
		AutoTestChannelEnabledOption: "false",
		AutoTestChannelMinutesOption: "45",
		ChannelTestModeOption:        ChannelTestModePassiveRecovery,
	}
	config, err := buildChannelReliabilitySettingWithEnv(options, reliabilityEnvironment(map[string]string{
		"RETRY_TIMES":            "7",
		"CHANNEL_TEST_FREQUENCY": "30",
		"CHANNEL_TEST_ENABLED":   "false",
	}))
	require.NoError(t, err)
	assert.Equal(t, 7, config.RetryTimes)
	assert.Equal(t, 30, config.AutoTestChannelMinutes)
	assert.False(t, config.AutoTestChannelEnabled, "explicit enabled override is applied after frequency")
	assert.Equal(t, ChannelTestModeScheduledAll, config.ChannelTestMode, "frequency preserves the reference scheduled-all override")

	for key, value := range map[string]string{
		"RETRY_TIMES":            "100",
		"CHANNEL_TEST_FREQUENCY": "0",
		"CHANNEL_TEST_ENABLED":   "yes",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := buildChannelReliabilitySettingWithEnv(options, reliabilityEnvironment(map[string]string{key: value}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), key)
		})
	}
}

func TestParseHTTPStatusCodeRangesReferenceGrammar(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{" , ， , ", ""},
		{"429", "429"},
		{"500 - 503, 401，402-404,403,505", "401-404,500-503,505"},
		{"599,100-199,300-399", "100-199,300-399,599"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			ranges, err := ParseHTTPStatusCodeRanges(test.input)
			require.NoError(t, err)
			assert.Equal(t, test.want, HTTPStatusCodeRangesString(ranges))
		})
	}
}

func TestParseHTTPStatusCodeRangesRejectsMalformedOrUnboundedInput(t *testing.T) {
	invalid := []string{
		"99", "600", "500-400", "500-", "-500", "500--501", "5e2", "+500", "500.0", "500\n501", "500\u202e",
		strings.Repeat("5", maxHTTPStatusCodeRulesBytes+1),
	}
	for _, raw := range invalid {
		_, err := ParseHTTPStatusCodeRanges(raw)
		require.Error(t, err, "input %q", raw)
	}

	entries := make([]string, 0, maxHTTPStatusCodeRuleTokens+1)
	for i := 0; i <= maxHTTPStatusCodeRuleTokens; i++ {
		entries = append(entries, "500")
	}
	_, err := ParseHTTPStatusCodeRanges(strings.Join(entries, ","))
	require.ErrorContains(t, err, "too many")
}

func TestAutomaticDisableKeywordsAreBoundedAndSafe(t *testing.T) {
	_, keywords, err := parseAutomaticDisableKeywords("")
	require.NoError(t, err)
	assert.Empty(t, keywords)

	invalid := []string{
		"safe\tunsafe",
		"safe\u202eunsafe",
		strings.Repeat("x", maxAutomaticDisableKeywordBytes+1),
		strings.Repeat("x", maxAutomaticDisableKeywordsBytes+1),
	}
	for _, raw := range invalid {
		_, _, err := parseAutomaticDisableKeywords(raw)
		require.Error(t, err)
	}

	entries := make([]string, 0, maxAutomaticDisableKeywords+1)
	for i := 0; i <= maxAutomaticDisableKeywords; i++ {
		entries = append(entries, fmt.Sprintf("failure-%d", i))
	}
	_, _, err = parseAutomaticDisableKeywords(strings.Join(entries, "\n"))
	require.ErrorContains(t, err, "too many")
}

func TestChannelReliabilitySnapshotIsDefensiveAndAtomic(t *testing.T) {
	original := GetChannelReliabilitySetting()
	t.Cleanup(func() { storeChannelReliabilitySetting(original) })

	first, err := buildChannelReliabilitySettingWithEnv(map[string]string{
		RetryTimesOption:                  "1",
		AutomaticDisableStatusCodesOption: "401",
	}, noReliabilityEnvironment)
	require.NoError(t, err)
	second, err := buildChannelReliabilitySettingWithEnv(map[string]string{
		RetryTimesOption:                  "10",
		AutomaticDisableStatusCodesOption: "500-503",
	}, noReliabilityEnvironment)
	require.NoError(t, err)
	storeChannelReliabilitySetting(first)

	copyOfFirst := GetChannelReliabilitySetting()
	copyOfFirst.AutomaticDisableStatusCodes[0].Start = 200
	assert.Equal(t, "401", GetChannelReliabilitySetting().AutomaticDisableStatusCodesText)
	assert.True(t, GetChannelReliabilitySetting().ShouldDisableForStatus(401))

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for i := 0; i < 1000; i++ {
			storeChannelReliabilitySetting(first)
			storeChannelReliabilitySetting(second)
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < 2000; i++ {
			current := GetChannelReliabilitySetting()
			if current.RetryTimes != 1 && current.RetryTimes != 10 {
				t.Errorf("observed partial snapshot: %+v", current)
				return
			}
		}
	}()
	wait.Wait()

	beforeInvalid := GetChannelReliabilitySetting()
	_, err = buildChannelReliabilitySettingWithEnv(map[string]string{RetryTimesOption: "11"}, noReliabilityEnvironment)
	require.Error(t, err)
	assert.Equal(t, beforeInvalid, GetChannelReliabilitySetting(), "a rejected candidate must not publish")
}

func TestAutomaticDisableKeywordMatchRejectsOversizedMessages(t *testing.T) {
	config := defaultChannelReliabilitySetting()
	assert.False(t, config.MatchesAutomaticDisableKeyword(strings.Repeat("x", maxAutomaticDisableKeywordsBytes+1)+" permission denied"))
	assert.False(t, config.MatchesAutomaticDisableKeyword("invalid\xff permission denied"))
}
