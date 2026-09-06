package settings

import (
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func setupAffinitySettingTest(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, Init())
}

func validAffinityRule(name string) ChannelAffinityRule {
	return ChannelAffinityRule{
		Name:             name,
		ModelRegex:       []string{"^model-" + name + "$"},
		PathRegex:        []string{"^/v1/" + name + "$"},
		UserAgentInclude: []string{"client-" + name},
		KeySources: []ChannelAffinityKeySource{
			{Type: "request_header", Key: "X-Affinity-Key"},
		},
		ValueRegex: "^key-[0-9]+$",
		ParamOverrideTemplate: map[string]any{
			"operations": []any{map[string]any{
				"mode": "pass_headers", "value": []any{"X-Trace-" + name}, "keep_origin": true,
			}},
		},
		IncludeRuleName: true,
	}
}

func marshalAffinityRules(t *testing.T, rules []ChannelAffinityRule) string {
	t.Helper()
	encoded, err := jsonutil.Marshal(rules)
	require.NoError(t, err)
	return string(encoded)
}

func requireAffinityRulesError(t *testing.T, rules []ChannelAffinityRule, message string) {
	t.Helper()
	_, err := buildChannelAffinitySetting(map[string]string{
		ChannelAffinityRulesOption: marshalAffinityRules(t, rules),
	})
	require.ErrorContains(t, err, message)
}

func TestChannelAffinitySettingValidationAndRemoteSync(t *testing.T) {
	setupAffinitySettingTest(t)
	defaults := GetChannelAffinitySetting()
	assert.True(t, defaults.Enabled)
	assert.Equal(t, DefaultChannelAffinityMaxEntries, defaults.MaxEntries)
	require.Len(t, defaults.Rules, 2)

	invalidRules := `[{"name":"broken","model_regex":["("],"key_sources":[{"type":"gjson","path":"key"}]}]`
	require.Error(t, UpdateOption(ChannelAffinityRulesOption, invalidRules))
	assert.Empty(t, GetOption(ChannelAffinityRulesOption))
	assert.Equal(t, defaults.Rules[0].Name, GetChannelAffinitySetting().Rules[0].Name)

	require.NoError(t, model.DB.Create(&model.Option{Key: ChannelAffinityMaxEntriesOption, Value: "23"}).Error)
	require.NoError(t, Sync())
	assert.Equal(t, 23, GetChannelAffinitySetting().MaxEntries)

	require.NoError(t, model.DB.Model(&model.Option{}).
		Where("key = ?", ChannelAffinityMaxEntriesOption).Update("value", "-1").Error)
	require.Error(t, Sync())
	assert.Equal(t, 23, GetChannelAffinitySetting().MaxEntries, "invalid remote options must not replace the live snapshot")
}

func TestChannelAffinityPassHeadersRejectCredentialsAndInvalidNames(t *testing.T) {
	setupAffinitySettingTest(t)
	for _, header := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Host", "Content-Length",
		"Connection", "X-Forwarded-For", "X-Api-Key", "X-Goog-Api-Key",
		"X-Auth", "X-Token", "X-Secret", "X-Signature", "X-Credential",
		"X-Password", "Foo-Key", "X-Unclassified-Metadata",
		"Sec-WebSocket-Protocol", "Bad Header", "X-Bad\r\nInjected",
	} {
		t.Run(strings.NewReplacer(" ", "_", "\r", "_", "\n", "_").Replace(header), func(t *testing.T) {
			rule := validAffinityRule("unsafe-header")
			rule.ParamOverrideTemplate = map[string]any{
				"operations": []any{map[string]any{
					"mode": "pass_headers", "value": []any{header},
				}},
			}
			requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "contains an unsafe header")
		})
	}
	assert.True(t, IsChannelAffinityPassthroughHeaderAllowed("X-Codex-Turn-State"))
	assert.True(t, IsChannelAffinityPassthroughHeaderAllowed("Anthropic-Beta"))
	assert.True(t, IsChannelAffinityPassthroughHeaderAllowed("X-Trace-Request"))
}

func TestChannelAffinityMaxEntriesBoundsAndExplicitZero(t *testing.T) {
	setupAffinitySettingTest(t)

	assert.Equal(t, 10_000, DefaultChannelAffinityMaxEntries)
	assert.Equal(t, 100_000, MaxChannelAffinityEntries)
	require.NoError(t, UpdateOption(ChannelAffinityMaxEntriesOption, "0"))
	assert.Zero(t, GetChannelAffinitySetting().MaxEntries, "an explicit zero disables affinity storage")
	assert.Equal(t, "0", GetOption(ChannelAffinityMaxEntriesOption))

	require.NoError(t, UpdateOption(ChannelAffinityMaxEntriesOption, strconv.Itoa(MaxChannelAffinityEntries)))
	before := GetChannelAffinitySetting()
	require.Error(t, UpdateOption(ChannelAffinityMaxEntriesOption, strconv.Itoa(MaxChannelAffinityEntries+1)))
	assert.Equal(t, MaxChannelAffinityEntries, GetChannelAffinitySetting().MaxEntries)
	assert.Equal(t, before.MaxEntries, GetChannelAffinitySetting().MaxEntries, "a rejected update must retain the old snapshot")
}

func TestChannelAffinityRulesRejectDuplicateJSONKeysBeforeDecode(t *testing.T) {
	setupAffinitySettingTest(t)
	baseline := marshalAffinityRules(t, []ChannelAffinityRule{validAffinityRule("baseline")})
	require.NoError(t, UpdateOption(ChannelAffinityRulesOption, baseline))

	// The first name also has the wrong type. A last-key-wins decoder would
	// otherwise hide it and accept the second name.
	duplicate := `[{
		"name": 123,
		"name": "replacement",
		"model_regex": ["^model$"],
		"key_sources": [{"type":"request_header","key":"X-Key"}]
	}]`
	err := UpdateOption(ChannelAffinityRulesOption, duplicate)
	require.ErrorContains(t, err, `duplicate JSON object key "name"`)
	assert.Equal(t, baseline, GetOption(ChannelAffinityRulesOption))
	config := GetChannelAffinitySetting()
	require.Len(t, config.Rules, 1)
	assert.Equal(t, "baseline", config.Rules[0].Name)
	assert.True(t, config.Rules[0].MatchesModel("model-baseline"))

	nestedDuplicate := `[{
		"name":"nested",
		"model_regex":["^model$"],
		"key_sources":[{"type":"request_header","key":"X-Key"}],
		"param_override_template":{"operations":[{"mode":"pass_headers","mode":"ignored","value":["X-A"]}]}
	}]`
	err = UpdateOption(ChannelAffinityRulesOption, nestedDuplicate)
	require.ErrorContains(t, err, `duplicate JSON object key "mode"`)
	assert.Equal(t, baseline, GetOption(ChannelAffinityRulesOption))
}

func TestChannelAffinityRuleResourceBounds(t *testing.T) {
	t.Run("raw rules JSON bytes", func(t *testing.T) {
		_, err := buildChannelAffinitySetting(map[string]string{
			ChannelAffinityRulesOption: strings.Repeat(" ", MaxChannelAffinityRulesJSONBytes+1),
		})
		require.ErrorContains(t, err, "must not exceed")
	})

	t.Run("total rules", func(t *testing.T) {
		rules := make([]ChannelAffinityRule, MaxChannelAffinityRules+1)
		for i := range rules {
			rules[i] = validAffinityRule(fmt.Sprintf("rule-%d", i))
		}
		requireAffinityRulesError(t, rules, "more than 64 rules")
	})

	t.Run("rule name bytes", func(t *testing.T) {
		rule := validAffinityRule("name")
		rule.Name = strings.Repeat("n", MaxChannelAffinityRuleNameBytes+1)
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "name must not exceed")
	})

	t.Run("regex list count", func(t *testing.T) {
		rule := validAffinityRule("regex-count")
		rule.ModelRegex = make([]string, MaxChannelAffinityRegexPatternsPerList+1)
		for i := range rule.ModelRegex {
			rule.ModelRegex[i] = fmt.Sprintf("^model-%d$", i)
		}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "model_regex must not contain more")
	})

	t.Run("regex pattern bytes", func(t *testing.T) {
		rule := validAffinityRule("regex-size")
		rule.ModelRegex = []string{strings.Repeat("a", MaxChannelAffinityRegexPatternBytes+1)}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "model_regex[0] must not exceed")
	})

	t.Run("aggregate regex bytes", func(t *testing.T) {
		rule := validAffinityRule("regex-total")
		rule.ModelRegex = make([]string, MaxChannelAffinityRegexBytesPerRule/MaxChannelAffinityRegexPatternBytes+1)
		for i := range rule.ModelRegex {
			rule.ModelRegex[i] = strings.Repeat("a", MaxChannelAffinityRegexPatternBytes)
		}
		rule.PathRegex = nil
		rule.ValueRegex = ""
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "regexes must not exceed")
	})

	t.Run("user agent list and strings", func(t *testing.T) {
		rule := validAffinityRule("user-agents")
		rule.UserAgentInclude = make([]string, MaxChannelAffinityUserAgentIncludes+1)
		for i := range rule.UserAgentInclude {
			rule.UserAgentInclude[i] = "agent"
		}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "user_agent_include must not contain more")

		rule = validAffinityRule("user-agent-string")
		rule.UserAgentInclude = []string{strings.Repeat("u", MaxChannelAffinityStringBytes+1)}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "user_agent_include[0] must not exceed")
	})

	t.Run("key source count and strings", func(t *testing.T) {
		rule := validAffinityRule("source-count")
		rule.KeySources = make([]ChannelAffinityKeySource, MaxChannelAffinityKeySources+1)
		for i := range rule.KeySources {
			rule.KeySources[i] = ChannelAffinityKeySource{Type: "request_header", Key: "X-Key"}
		}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "key_sources must not contain more")

		rule = validAffinityRule("source-string")
		rule.KeySources[0].Key = strings.Repeat("k", MaxChannelAffinityStringBytes+1)
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "key_sources[0].key must not exceed")
	})

	t.Run("template bytes", func(t *testing.T) {
		rule := validAffinityRule("template-bytes")
		headers := make([]any, 20)
		for i := range headers {
			headers[i] = strings.Repeat("h", 900)
		}
		rule.ParamOverrideTemplate = map[string]any{"operations": []any{
			map[string]any{"mode": "pass_headers", "value": headers},
		}}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "override template must not exceed 16384 bytes")
	})

	t.Run("template depth", func(t *testing.T) {
		rule := validAffinityRule("template-depth")
		var nested any = "leaf"
		for i := 0; i < MaxChannelAffinityTemplateDepth; i++ {
			nested = []any{nested}
		}
		rule.ParamOverrideTemplate = map[string]any{"unsupported": nested}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "must not exceed depth 8")
	})

	t.Run("template nodes", func(t *testing.T) {
		rule := validAffinityRule("template-nodes")
		nodes := make([]any, MaxChannelAffinityTemplateNodes)
		rule.ParamOverrideTemplate = map[string]any{"unsupported": nodes}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "more than 256 nodes")
	})

	t.Run("template strings", func(t *testing.T) {
		rule := validAffinityRule("template-string")
		rule.ParamOverrideTemplate = map[string]any{
			"operations": []any{map[string]any{
				"mode": "pass_headers", "value": []any{strings.Repeat("h", MaxChannelAffinityStringBytes+1)},
			}},
		}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "override template string must not exceed")
	})

	t.Run("template operation count", func(t *testing.T) {
		rule := validAffinityRule("operation-count")
		operations := make([]any, MaxChannelAffinityTemplateOperations+1)
		for i := range operations {
			operations[i] = map[string]any{"mode": "pass_headers", "value": []any{"X-Header"}}
		}
		rule.ParamOverrideTemplate = map[string]any{"operations": operations}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "more than 8 operations")
	})

	t.Run("headers per operation", func(t *testing.T) {
		rule := validAffinityRule("header-count")
		headers := make([]any, MaxChannelAffinityHeadersPerOperation+1)
		for i := range headers {
			headers[i] = "X-Header"
		}
		rule.ParamOverrideTemplate = map[string]any{"operations": []any{
			map[string]any{"mode": "pass_headers", "value": headers},
		}}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "more than 64 headers")
	})

	t.Run("aggregate headers", func(t *testing.T) {
		rule := validAffinityRule("header-total")
		operations := make([]any, 3)
		for i := range operations {
			headers := make([]any, MaxChannelAffinityHeadersPerOperation)
			for j := range headers {
				headers[j] = "X-Trace-ID"
			}
			operations[i] = map[string]any{"mode": "pass_headers", "value": headers}
		}
		rule.ParamOverrideTemplate = map[string]any{"operations": operations}
		requireAffinityRulesError(t, []ChannelAffinityRule{rule}, "more than 128 headers in total")
	})
}

func TestChannelAffinityCompiledMatchersAndCloneIsolation(t *testing.T) {
	setupAffinitySettingTest(t)
	rule := validAffinityRule("compiled")
	rulesJSON := marshalAffinityRules(t, []ChannelAffinityRule{rule})
	require.NoError(t, UpdateOption(ChannelAffinityRulesOption, rulesJSON))

	first := GetChannelAffinitySetting()
	require.Len(t, first.Rules, 1)
	assert.True(t, first.Rules[0].MatchesModel("model-compiled"))
	assert.False(t, first.Rules[0].MatchesModel("model-other"))
	assert.True(t, first.Rules[0].HasPathMatchers())
	assert.True(t, first.Rules[0].MatchesPath("/v1/compiled"))
	assert.False(t, first.Rules[0].MatchesPath("/v1/other"))
	assert.True(t, first.Rules[0].MatchesValue("key-123"))
	assert.False(t, first.Rules[0].MatchesValue("wrong"))
	assert.False(t, first.Rules[0].MatchesValue(""))

	// Public presentation fields are cloned independently from the compiled
	// snapshot. Mutating a returned value cannot alter its matcher or the live
	// setting, including an initially empty template map.
	first.Rules[0].ModelRegex[0] = "^mutated$"
	first.Rules[0].PathRegex = nil
	first.Rules[0].ValueRegex = ""
	first.Rules[0].ParamOverrideTemplate["operations"].([]any)[0].(map[string]any)["value"].([]any)[0] = "X-Mutated"
	assert.True(t, first.Rules[0].MatchesModel("model-compiled"))
	assert.True(t, first.Rules[0].HasPathMatchers())
	assert.True(t, first.Rules[0].MatchesValue("key-123"))

	second := GetChannelAffinitySetting()
	assert.Equal(t, "^model-compiled$", second.Rules[0].ModelRegex[0])
	header := second.Rules[0].ParamOverrideTemplate["operations"].([]any)[0].(map[string]any)["value"].([]any)[0]
	assert.Equal(t, "X-Trace-compiled", header)

	unconstrainedRule := validAffinityRule("unconstrained")
	unconstrainedRule.PathRegex = nil
	unconstrainedRule.ValueRegex = ""
	require.NoError(t, UpdateOption(ChannelAffinityRulesOption, marshalAffinityRules(t, []ChannelAffinityRule{unconstrainedRule})))
	unconstrained := GetChannelAffinitySetting().Rules[0]
	assert.False(t, unconstrained.HasPathMatchers())
	assert.False(t, unconstrained.MatchesPath("/any/path"))
	assert.True(t, unconstrained.MatchesValue("any-non-empty-value"))
	assert.False(t, unconstrained.MatchesValue(""))

	emptyTemplateJSON := `[{
		"name":"empty-template",
		"model_regex":["^model-empty-template$"],
		"key_sources":[{"type":"request_header","key":"X-Key"}],
		"param_override_template":{}
	}]`
	require.NoError(t, UpdateOption(ChannelAffinityRulesOption, emptyTemplateJSON))
	emptyClone := GetChannelAffinitySetting()
	require.NotNil(t, emptyClone.Rules[0].ParamOverrideTemplate)
	emptyClone.Rules[0].ParamOverrideTemplate["injected"] = true
	assert.Empty(t, GetChannelAffinitySetting().Rules[0].ParamOverrideTemplate)
}

func TestChannelAffinityCustomRulesDecodeIntoFreshValues(t *testing.T) {
	minimalJSON := `[{
		"name":"minimal",
		"model_regex":["^minimal$"],
		"key_sources":[{"type":"request_header","key":"X-Key"}]
	}]`
	config, err := buildChannelAffinitySetting(map[string]string{ChannelAffinityRulesOption: minimalJSON})
	require.NoError(t, err)
	require.Len(t, config.Rules, 1)
	rule := config.Rules[0]
	assert.Nil(t, rule.PathRegex)
	assert.Nil(t, rule.UserAgentInclude)
	assert.Nil(t, rule.ParamOverrideTemplate)
	assert.False(t, rule.SkipRetryOnFailure)
	assert.False(t, rule.IncludeUsingGroup)
	assert.False(t, rule.IncludeRuleName)
	assert.True(t, rule.MatchesModel("minimal"))
	assert.False(t, rule.HasPathMatchers())
	assert.True(t, rule.MatchesValue("any-value"))
}

func TestChannelAffinitySettingConcurrentUpdatesRemainCoherent(t *testing.T) {
	setupAffinitySettingTest(t)
	const workers = 40
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			if index%2 == 0 {
				errors <- UpdateOption(ChannelAffinityMaxEntriesOption, strconv.Itoa(100+index))
			} else {
				errors <- UpdateOption(ChannelAffinityEnabledOption, strconv.FormatBool(index%4 == 1))
			}
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	config := GetChannelAffinitySetting()
	storedMax, err := strconv.Atoi(GetOption(ChannelAffinityMaxEntriesOption))
	require.NoError(t, err)
	storedEnabled, err := strconv.ParseBool(GetOption(ChannelAffinityEnabledOption))
	require.NoError(t, err)
	assert.Equal(t, storedMax, config.MaxEntries)
	assert.Equal(t, storedEnabled, config.Enabled)
}

func TestChannelAffinityCompiledSnapshotsRemainCoherentDuringUpdates(t *testing.T) {
	setupAffinitySettingTest(t)
	alphaJSON := marshalAffinityRules(t, []ChannelAffinityRule{validAffinityRule("alpha")})
	betaJSON := marshalAffinityRules(t, []ChannelAffinityRule{validAffinityRule("beta")})
	require.NoError(t, UpdateOption(ChannelAffinityRulesOption, alphaJSON))

	const readers = 8
	const readsPerWorker = 400
	const updates = 40
	start := make(chan struct{})
	errors := make(chan error, readers+1)
	var wait sync.WaitGroup
	wait.Add(readers + 1)

	for i := 0; i < readers; i++ {
		go func() {
			defer wait.Done()
			<-start
			for read := 0; read < readsPerWorker; read++ {
				config := GetChannelAffinitySetting()
				if len(config.Rules) != 1 {
					errors <- fmt.Errorf("read an incoherent rule count: %d", len(config.Rules))
					return
				}
				rule := &config.Rules[0]
				switch rule.Name {
				case "alpha":
					if !rule.MatchesModel("model-alpha") || rule.MatchesModel("model-beta") || !rule.MatchesPath("/v1/alpha") {
						errors <- fmt.Errorf("alpha snapshot contains mismatched compiled regexes")
						return
					}
				case "beta":
					if !rule.MatchesModel("model-beta") || rule.MatchesModel("model-alpha") || !rule.MatchesPath("/v1/beta") {
						errors <- fmt.Errorf("beta snapshot contains mismatched compiled regexes")
						return
					}
				default:
					errors <- fmt.Errorf("read unexpected rule %q", rule.Name)
					return
				}

				// Exercise clone independence under the race detector while another
				// goroutine publishes fresh snapshots.
				rule.ModelRegex[0] = "^caller-mutated$"
				rule.ParamOverrideTemplate["caller"] = read
			}
		}()
	}

	go func() {
		defer wait.Done()
		<-start
		for update := 0; update < updates; update++ {
			raw := alphaJSON
			if update%2 == 1 {
				raw = betaJSON
			}
			if err := UpdateOption(ChannelAffinityRulesOption, raw); err != nil {
				errors <- fmt.Errorf("update %d failed: %w", update, err)
				return
			}
		}
	}()

	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
}
