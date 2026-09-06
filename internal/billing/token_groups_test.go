package billing

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"gorm.io/gorm"
	"path/filepath"
	"sync"
	"testing"
)

func setupTokenGroupsTest(t *testing.T) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "token-groups.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, setting.Init())
	previousRatios := ExportedGroupRatios()
	t.Cleanup(func() { SetGroupRatios(previousRatios) })
}

func TestUserSelectableGroupsIntersectAllowlistAndRatios(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{
		"default": 1,
		"vip":     2,
		"staff":   0,
		"team":    1,
		"blocked": -1,
	})

	assert.True(t, IsUserSelectableGroup("default", "default"))
	assert.True(t, IsUserSelectableGroup("default", "vip"))
	assert.False(t, IsUserSelectableGroup("default", "staff"), "a ratio alone must not grant access")
	assert.False(t, IsUserSelectableGroup("default", "missing"), "an allowlist entry still needs a ratio")
	assert.Equal(t, []string{"default", "vip"}, GetUserAutoGroups("default"))

	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"staff":"Staff"}`))
	assert.True(t, IsUserSelectableGroup("team", "team"), "the user's own group is always in the allowlist union")
	assert.False(t, IsUserSelectableGroup("team", "staff"), "a zero ratio must not authorize free traffic")
	assert.False(t, IsUserSelectableGroup("team", "default"))
	assert.False(t, IsUserSelectableGroup("blocked", "blocked"), "a negative own-group ratio is not usable")
	assert.Equal(t, []string{"team"}, GetUserAutoGroups("team"))
}

func TestUserUsableGroupsApplySpecialDirectivesWithoutBypassingPositiveRatios(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{
		"default": 1, "vip": 2, "team": 1.5, "premium": 0.75, "zero": 0,
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.UserUsableGroupsOption: `{"default":"Default","vip":"VIP","auto":"Automatic"}`,
		setting.GroupSpecialUsableGroupOption: `{
			"team":{"-:vip":"remove","+:premium":"Premium","+:zero":"Zero","-:team":"cannot remove own","-:auto":"remove"}
		}`,
	}))

	assert.Equal(t, map[string]string{
		"default": "Default", "premium": "Premium", "team": "用户分组",
	}, GetUserUsableGroups("team"))
	assert.True(t, IsUserSelectableGroup("team", "premium"))
	assert.False(t, IsUserSelectableGroup("team", "vip"))
	assert.False(t, IsUserSelectableGroup("team", "zero"), "special visibility must not bypass a non-positive ratio")
	assert.False(t, IsUserSelectableGroup("team", userssvc.GroupAuto))

	require.NoError(t, setting.UpdateOption(setting.GroupSpecialUsableGroupOption,
		`{"team":{"+:auto":"Automatic for team"}}`))
	groups := GetUserUsableGroups("team")
	assert.Equal(t, "Automatic for team", groups[userssvc.GroupAuto])
	assert.False(t, IsUserSelectableGroup("team", userssvc.GroupAuto), "auto remains a presentation marker, never a concrete relay group")
}

func TestValidateUserAutoGroups(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 1})

	ordered := []string{"vip", "default"}
	require.NoError(t, ValidateUserAutoGroups("default", ordered, 2))
	assert.Equal(t, []string{"vip", "default"}, ordered, "validation must retain caller priority")
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{"vip", "default"}, 1), ErrTooManyAutoGroups)
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{"vip", "vip"}, 2), ErrDuplicateAutoGroup)
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{""}, 2), ErrInvalidAutoGroup)
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{"auto"}, 2), ErrInvalidAutoGroup)
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{" vip"}, 2), ErrInvalidAutoGroup)
	assert.ErrorIs(t, ValidateUserAutoGroups("default", []string{"staff"}, 2), ErrUnauthorizedGroup)
}

func TestResolveRelayGroupPolicyRevalidatesAndFailsClosed(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 3})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.UserUsableGroupsOption:   `{"default":"Default","vip":"VIP","staff":"Staff"}`,
		setting.AutoGroupsOption:         `["vip","default"]`,
		setting.MaxTokenAutoGroupsOption: "2",
	}))

	ordinary := &model.Token{Group: "vip"}
	policy, err := ResolveRelayGroupPolicy("default", ordinary)
	require.NoError(t, err)
	assert.Equal(t, []string{"vip"}, policy.Groups)

	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default"}`))
	_, err = ResolveRelayGroupPolicy("default", ordinary)
	assert.ErrorIs(t, err, ErrUnauthorizedGroup, "permission revocation must affect existing tokens immediately")

	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default","vip":"VIP","staff":"Staff"}`))
	auto := &model.Token{Group: userssvc.GroupAuto, AutoGroups: `["staff","vip","default"]`, CrossGroupRetry: true}
	require.NoError(t, setting.UpdateOption(setting.MaxTokenAutoGroupsOption, "2"))
	policy, err = ResolveRelayGroupPolicy("default", auto)
	require.NoError(t, err)
	assert.Equal(t, []string{"staff", "vip"}, policy.Groups)
	assert.True(t, policy.Auto)
	assert.True(t, policy.CrossGroupRetry)

	auto.AutoGroups = `{not-json}`
	_, err = ResolveRelayGroupPolicy("default", auto)
	assert.ErrorIs(t, err, ErrMalformedAutoGroups)
	auto.AutoGroups = `["vip","vip"]`
	_, err = ResolveRelayGroupPolicy("default", auto)
	assert.ErrorIs(t, err, ErrMalformedAutoGroups)
	require.NoError(t, setting.UpdateOption(setting.MaxTokenAutoGroupsOption, "1"))
	auto.AutoGroups = `["vip","vip"]`
	_, err = ResolveRelayGroupPolicy("default", auto)
	assert.ErrorIs(t, err, ErrMalformedAutoGroups, "the runtime limit must not hide malformed trailing token data")
	auto.AutoGroups = `["vip"," auto"]`
	_, err = ResolveRelayGroupPolicy("default", auto)
	assert.ErrorIs(t, err, ErrMalformedAutoGroups, "every stored candidate must be validated before truncation")
}

func TestResolveRelayGroupPolicyInheritsOrderedGlobalAndRejectsEmpty(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{"default": 1, "vip": 2})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.UserUsableGroupsOption: `{"default":"Default","vip":"VIP"}`,
		setting.AutoGroupsOption:       `["vip","default"]`,
	}))
	policy, err := ResolveRelayGroupPolicy("default", &model.Token{Group: userssvc.GroupAuto})
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, policy.Groups)

	require.NoError(t, setting.UpdateOption(setting.AutoGroupsOption, `[]`))
	_, err = ResolveRelayGroupPolicy("default", &model.Token{Group: userssvc.GroupAuto})
	assert.ErrorIs(t, err, ErrNoAuthorizedGroup)
}

func TestResolveRelayGroupPolicyRejectsNonPositiveRatios(t *testing.T) {
	setupTokenGroupsTest(t)
	require.NoError(t, setting.UpdateOption(setting.UserUsableGroupsOption, `{"default":"Default","vip":"VIP","staff":"Staff"}`))
	SetGroupRatios(map[string]float64{"default": 1, "vip": 0, "staff": -1})

	_, err := ResolveRelayGroupPolicy("default", &model.Token{Group: "vip"})
	assert.ErrorIs(t, err, ErrUnauthorizedGroup, "an explicit zero-ratio group must fail closed")

	policy, err := ResolveRelayGroupPolicy("default", &model.Token{
		Group:      userssvc.GroupAuto,
		AutoGroups: `["vip","staff","default"]`,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, policy.Groups, "auto routing must skip every non-positive candidate")

	_, err = ResolveRelayGroupPolicy("default", &model.Token{
		Group:      userssvc.GroupAuto,
		AutoGroups: `["vip","staff"]`,
	})
	assert.ErrorIs(t, err, ErrNoAuthorizedGroup)
}

func TestUserUsableGroupsConcurrentPublication(t *testing.T) {
	setupTokenGroupsTest(t)
	SetGroupRatios(map[string]float64{"default": 1, "vip": 1, "staff": 1})

	const iterations = 50
	var wait sync.WaitGroup
	errs := make(chan error, iterations)
	wait.Add(2)
	go func() {
		defer wait.Done()
		for i := 0; i < iterations; i++ {
			value := `{"default":"Default","vip":"VIP"}`
			if i%2 == 1 {
				value = `{"staff":"Staff"}`
			}
			errs <- setting.UpdateOption(setting.UserUsableGroupsOption, value)
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < iterations; i++ {
			_ = GetUserAutoGroups("default")
			_ = IsUserSelectableGroup("default", "vip")
		}
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	stored := setting.GetOption(setting.UserUsableGroupsOption)
	groups := setting.GetUserUsableGroups()
	if stored == `{"staff":"Staff"}` {
		assert.Contains(t, groups, "staff")
		assert.NotContains(t, groups, "vip")
	} else if stored == `{"default":"Default","vip":"VIP"}` {
		assert.Contains(t, groups, "vip")
		assert.NotContains(t, groups, "staff")
	} else {
		t.Fatalf("unexpected stored setting: %s", stored)
	}
}
