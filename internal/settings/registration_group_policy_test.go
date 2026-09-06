package settings

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestRegistrationGroupPolicyDefaultsAndCanonicalQuotaPrecedence(t *testing.T) {
	defaults, err := buildRegistrationGroupPolicy(nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultQuotaForNewUser, defaults.QuotaForNewUser())
	assert.False(t, defaults.DefaultUseAutoGroup())
	assert.Equal(t, DefaultRegistrationGroup, defaults.DefaultGroup())
	assert.True(t, defaults.RegistrationEnabled())
	assert.True(t, defaults.PasswordRegistrationEnabled())
	assert.False(t, defaults.UsesLegacyQuotaFallback())
	assert.Empty(t, defaults.SpecialUsableGroupRules("default"))
	assert.Equal(t, "{}", GroupSpecialUsableGroupOptionDefault())
	assert.Equal(t, "false", DefaultUseAutoGroupOptionDefault())
	assert.Equal(t, "0", QuotaForNewUserOptionDefault())

	legacy, err := buildRegistrationGroupPolicy(map[string]string{InitialQuotaOption: "123"})
	require.NoError(t, err)
	assert.Equal(t, 123, legacy.QuotaForNewUser())
	assert.True(t, legacy.UsesLegacyQuotaFallback())

	canonical, err := buildRegistrationGroupPolicy(map[string]string{
		InitialQuotaOption:        "123",
		QuotaForNewUserOption:     "7",
		DefaultUseAutoGroupOption: "true",
		DefaultGroupOption:        "starter",
	})
	require.NoError(t, err)
	assert.Equal(t, 7, canonical.QuotaForNewUser())
	assert.False(t, canonical.UsesLegacyQuotaFallback())
	assert.True(t, canonical.DefaultUseAutoGroup())
	assert.Equal(t, "starter", canonical.DefaultGroup())

	canonicalReset, err := buildRegistrationGroupPolicy(map[string]string{
		InitialQuotaOption:    "123",
		QuotaForNewUserOption: "",
	})
	require.NoError(t, err)
	assert.Zero(t, canonicalReset.QuotaForNewUser(), "an explicitly reset canonical option must not fall back")
	assert.False(t, canonicalReset.UsesLegacyQuotaFallback())

	maximum, err := buildRegistrationGroupPolicy(map[string]string{
		QuotaForNewUserOption: strconv.FormatInt(quotamath.MaxQuota, 10),
	})
	require.NoError(t, err)
	assert.Equal(t, int(quotamath.MaxQuota), maximum.QuotaForNewUser())

	for _, options := range []map[string]string{
		{QuotaForNewUserOption: "-1"},
		{QuotaForNewUserOption: strconv.FormatInt(quotamath.MaxQuota+1, 10)},
		{QuotaForNewUserOption: " 1"},
		{QuotaForNewUserOption: "1e3"},
		{QuotaForNewUserOption: "not-a-number", InitialQuotaOption: "9"},
		{InitialQuotaOption: "-1"},
		{DefaultUseAutoGroupOption: "TRUE"},
		{DefaultUseAutoGroupOption: "1"},
		{DefaultUseAutoGroupOption: " false "},
		{RegistrationEnabledOption: "TRUE"},
		{PasswordRegisterEnabledOption: " false "},
		{DefaultGroupOption: " starter"},
		{DefaultGroupOption: "starter\n"},
		{DefaultGroupOption: "starter\u202e"},
		{DefaultGroupOption: strings.Repeat("g", maxGroupPolicyIdentifierBytes+1)},
	} {
		_, err := buildRegistrationGroupPolicy(options)
		require.Error(t, err, options)
	}
}

func TestRegistrationGroupPolicyNormalizesRulesDeterministicallyAndRejectsConflicts(t *testing.T) {
	policy, err := buildRegistrationGroupPolicy(map[string]string{
		GroupSpecialUsableGroupOption: `{
			"team":{"-:vip":"remove","premium":"Premium","+:default":"Default"},
			"default":{"+:vip":"VIP"}
		}`,
	})
	require.NoError(t, err)
	assert.Equal(t, []SpecialUsableGroupRule{
		{Action: SpecialUsableGroupAdd, Group: "default", Description: "Default"},
		{Action: SpecialUsableGroupAdd, Group: "premium", Description: "Premium"},
		{Action: SpecialUsableGroupRemove, Group: "vip", Description: "remove"},
	}, policy.SpecialUsableGroupRules("team"))
	assert.Equal(t, []SpecialUsableGroupRule{
		{Action: SpecialUsableGroupAdd, Group: "vip", Description: "VIP"},
	}, policy.SpecialUsableGroupRules("default"))

	second, err := buildRegistrationGroupPolicy(map[string]string{
		GroupSpecialUsableGroupOption: `{"default":{"+:vip":"VIP"},"team":{"+:default":"Default","premium":"Premium","-:vip":"remove"}}`,
	})
	require.NoError(t, err)
	assert.Equal(t, policy.SpecialUsableGroupRules("team"), second.SpecialUsableGroupRules("team"))

	invalid := []string{
		`null`,
		`[]`,
		`{"default":null}`,
		`{"default":{"vip":1}}`,
		`{"default":{"vip":"one","vip":"two"}}`,
		`{"default":{},"default":{}}`,
		`{"default":{"vip":"one","+:vip":"two"}}`,
		`{"default":{"+:vip":"one","-:vip":"remove"}}`,
		`{"default":{"+:+:vip":"nested prefix"}}`,
		`{" default":{"vip":"bad source trim"}}`,
		`{"default":{" vip":"bad target trim"}}`,
		`{"default":{"vip":"bad\ncontrol"}}`,
		`{"default":{"vip":"bad\u202ebidi"}}`,
		`{"default":{"` + strings.Repeat("g", maxGroupPolicyIdentifierBytes+1) + `":"too long"}}`,
		`{"default":{"vip":"` + strings.Repeat("d", maxGroupPolicyDescriptionBytes+1) + `"}}`,
	}
	for _, raw := range invalid {
		_, err := buildRegistrationGroupPolicy(map[string]string{GroupSpecialUsableGroupOption: raw})
		require.Error(t, err, raw)
	}

	tooManyRules := make(map[string]string, maxSpecialUsableRulesPerGroup+1)
	for index := 0; index <= maxSpecialUsableRulesPerGroup; index++ {
		tooManyRules[fmt.Sprintf("g%03d", index)] = "group"
	}
	raw, err := jsonutil.Marshal(map[string]map[string]string{"default": tooManyRules})
	require.NoError(t, err)
	_, err = buildRegistrationGroupPolicy(map[string]string{GroupSpecialUsableGroupOption: string(raw)})
	require.Error(t, err)

	tooManyUsers := make(map[string]map[string]string, maxSpecialUsableUserGroups+1)
	for index := 0; index <= maxSpecialUsableUserGroups; index++ {
		tooManyUsers[fmt.Sprintf("u%03d", index)] = map[string]string{}
	}
	raw, err = jsonutil.Marshal(tooManyUsers)
	require.NoError(t, err)
	_, err = buildRegistrationGroupPolicy(map[string]string{GroupSpecialUsableGroupOption: string(raw)})
	require.Error(t, err)

	invalidUTF8 := string([]byte{'{', '"', 0xff, '"', ':', '{', '}', '}'})
	_, err = buildRegistrationGroupPolicy(map[string]string{GroupSpecialUsableGroupOption: invalidUTF8})
	require.Error(t, err)
}

func TestRegistrationGroupPolicyPublicationIsImmutableAndConcurrent(t *testing.T) {
	previous := GetRegistrationGroupPolicy()
	t.Cleanup(func() { publishRegistrationGroupPolicy(previous) })

	first, err := buildRegistrationGroupPolicy(map[string]string{
		QuotaForNewUserOption:         "11",
		DefaultUseAutoGroupOption:     "true",
		DefaultGroupOption:            "alpha-default",
		RegistrationEnabledOption:     "true",
		PasswordRegisterEnabledOption: "false",
		GroupSpecialUsableGroupOption: `{"team":{"+:alpha":"A"}}`,
	})
	require.NoError(t, err)
	second, err := buildRegistrationGroupPolicy(map[string]string{
		QuotaForNewUserOption:         "22",
		DefaultUseAutoGroupOption:     "false",
		DefaultGroupOption:            "beta-default",
		RegistrationEnabledOption:     "false",
		PasswordRegisterEnabledOption: "true",
		GroupSpecialUsableGroupOption: `{"team":{"-:beta":"remove"}}`,
	})
	require.NoError(t, err)
	publishRegistrationGroupPolicy(first)

	detached := GetRegistrationGroupPolicy()
	detached.specialUsableByGroup["team"][0].Group = "mutated"
	detached.specialUsableByGroup["extra"] = []SpecialUsableGroupRule{{Group: "injected"}}
	assert.Equal(t, "alpha", GetRegistrationGroupPolicy().SpecialUsableGroupRules("team")[0].Group)
	assert.Empty(t, GetRegistrationGroupPolicy().SpecialUsableGroupRules("extra"))

	const readers = 12
	const iterations = 2_000
	errorsSeen := make(chan string, readers)
	var wait sync.WaitGroup
	wait.Add(readers + 1)
	go func() {
		defer wait.Done()
		for index := 0; index < iterations; index++ {
			if index%2 == 0 {
				publishRegistrationGroupPolicy(first)
			} else {
				publishRegistrationGroupPolicy(second)
			}
		}
	}()
	for reader := 0; reader < readers; reader++ {
		go func() {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				snapshot := GetRegistrationGroupPolicy()
				rules := snapshot.SpecialUsableGroupRules("team")
				switch snapshot.QuotaForNewUser() {
				case 11:
					if !snapshot.DefaultUseAutoGroup() || snapshot.DefaultGroup() != "alpha-default" ||
						!snapshot.RegistrationEnabled() || snapshot.PasswordRegistrationEnabled() ||
						len(rules) != 1 || rules[0].Action != SpecialUsableGroupAdd || rules[0].Group != "alpha" {
						errorsSeen <- "observed a torn first snapshot"
						return
					}
				case 22:
					if snapshot.DefaultUseAutoGroup() || snapshot.DefaultGroup() != "beta-default" ||
						snapshot.RegistrationEnabled() || !snapshot.PasswordRegistrationEnabled() ||
						len(rules) != 1 || rules[0].Action != SpecialUsableGroupRemove || rules[0].Group != "beta" {
						errorsSeen <- "observed a torn second snapshot"
						return
					}
				default:
					errorsSeen <- "observed an unknown snapshot"
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for message := range errorsSeen {
		assert.Fail(t, message)
	}
}

func TestRegistrationGroupPolicyInvalidUpdateAndSyncRetainPublishedSnapshot(t *testing.T) {
	setupAffinitySettingTest(t)
	require.NoError(t, UpdateOptions(map[string]string{
		QuotaForNewUserOption:         "12",
		DefaultGroupOption:            "starter",
		DefaultUseAutoGroupOption:     "true",
		GroupSpecialUsableGroupOption: `{"starter":{ "+:vip":"VIP"}}`,
	}))
	baseline := GetRegistrationGroupPolicy()

	require.Error(t, UpdateOption(QuotaForNewUserOption, "-1"))
	assert.Equal(t, "12", GetOption(QuotaForNewUserOption))
	afterRejectedUpdate := GetRegistrationGroupPolicy()
	assert.Equal(t, baseline.QuotaForNewUser(), afterRejectedUpdate.QuotaForNewUser())
	assert.Equal(t, baseline.DefaultGroup(), afterRejectedUpdate.DefaultGroup())
	assert.Equal(t, baseline.SpecialUsableGroupRules("starter"), afterRejectedUpdate.SpecialUsableGroupRules("starter"))

	require.NoError(t, model.DB.Model(&model.Option{}).Where("key = ?", DefaultGroupOption).
		Update("value", " unsafe").Error)
	require.Error(t, Sync())
	afterRejectedSync := GetRegistrationGroupPolicy()
	assert.Equal(t, baseline.QuotaForNewUser(), afterRejectedSync.QuotaForNewUser())
	assert.Equal(t, baseline.DefaultGroup(), afterRejectedSync.DefaultGroup())
	assert.Equal(t, baseline.SpecialUsableGroupRules("starter"), afterRejectedSync.SpecialUsableGroupRules("starter"))
}
