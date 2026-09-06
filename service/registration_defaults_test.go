package service

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestResolveSpecialUsableGroupsIsDeterministicAndRequiresPositiveRatio(t *testing.T) {
	policy, err := setting.ParseRegistrationGroupPolicyOptions(map[string]string{
		setting.GroupSpecialUsableGroupOption: `{
			"team":{
				"-:vip":"remove",
				"+:premium":"Premium",
				"+:zero":"Zero",
				"+:nan":"NaN",
				"+:infinite":"Infinite",
				"-:team":"cannot remove own group"
			}
		}`,
	})
	require.NoError(t, err)
	ratios := map[string]float64{
		"default":  1,
		"vip":      2,
		"premium":  0.5,
		"team":     1.25,
		"zero":     0,
		"negative": -1,
		"nan":      math.NaN(),
		"infinite": math.Inf(1),
		"auto":     1,
	}
	lookup := func(group string) (float64, bool) {
		ratio, present := ratios[group]
		return ratio, present
	}
	base := map[string]string{
		"vip": "VIP", "default": "Default", "missing": "Missing",
		"negative": "Negative", "auto": "Auto", "bad\nname": "Unsafe",
	}

	resolved := ResolveSpecialUsableGroups(policy, "team", base, lookup)
	assert.Equal(t, []ResolvedUsableGroup{
		{Name: "default", Description: "Default"},
		{Name: "premium", Description: "Premium"},
		{Name: "team", Description: "用户分组"},
	}, resolved)

	resolved[0].Name = "mutated"
	assert.Equal(t, "default", ResolveSpecialUsableGroups(policy, "team", base, lookup)[0].Name)
	assert.Empty(t, ResolveSpecialUsableGroups(policy, "team", base, nil))
}

func TestRegistrationMutationPlanCapturesExactQuotaAndDefaultTokenBeforeMutation(t *testing.T) {
	policy, err := setting.ParseRegistrationGroupPolicyOptions(map[string]string{
		setting.QuotaForNewUserOption:     "123",
		setting.DefaultUseAutoGroupOption: "true",
		setting.DefaultGroupOption:        "starter",
	})
	require.NoError(t, err)
	calls := 0
	plan, err := planRegistrationMutation(policy, "alice", true, 1_700_000_000, func(size int) (string, error) {
		calls++
		assert.Equal(t, defaultTokenCredentialBytes, size)
		return strings.Repeat("A", size), nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, 123, plan.QuotaForNewUser())
	assert.True(t, plan.DefaultUseAutoGroup())
	assert.Equal(t, "starter", plan.DefaultGroup())
	assert.True(t, plan.HasDefaultToken())

	token, err := plan.DefaultTokenForUser(42)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, 42, token.UserId)
	assert.Equal(t, "alice的初始令牌", token.Name)
	assert.Equal(t, "sk-"+strings.Repeat("A", defaultTokenCredentialBytes), token.Key)
	assert.Equal(t, TokenStatusEnabled, token.Status)
	assert.Equal(t, int64(1_700_000_000), token.CreatedTime)
	assert.Equal(t, token.CreatedTime, token.AccessedTime)
	assert.Equal(t, int64(-1), token.ExpiredTime)
	assert.Equal(t, common.QuotaPerUnit, token.RemainQuota)
	assert.True(t, token.UnlimitedQuota)
	assert.False(t, token.ModelLimitsEnabled)
	assert.Equal(t, GroupAuto, token.Group)

	// Materialization returns detached rows; callers cannot alter the plan that
	// will be used by the registration transaction.
	token.Key = "mutated"
	token.Group = "mutated"
	again, err := plan.DefaultTokenForUser(42)
	require.NoError(t, err)
	assert.Equal(t, "sk-"+strings.Repeat("A", defaultTokenCredentialBytes), again.Key)
	assert.Equal(t, GroupAuto, again.Group)
	_, err = plan.DefaultTokenForUser(0)
	require.ErrorIs(t, err, ErrDefaultTokenPlanInvalid)

	manualPolicy, err := setting.ParseRegistrationGroupPolicyOptions(map[string]string{
		setting.DefaultUseAutoGroupOption: "false",
	})
	require.NoError(t, err)
	manualPlan, err := planRegistrationMutation(
		manualPolicy, strings.Repeat("u", maxRegistrationUsernameBytes), true, 1, validDefaultTokenCredential,
	)
	require.NoError(t, err)
	manualToken, err := manualPlan.DefaultTokenForUser(1)
	require.NoError(t, err)
	assert.Empty(t, manualToken.Group, "the empty marker inherits the user's group")
	assert.LessOrEqual(t, len(manualToken.Name), maxDefaultTokenNameBytes)
}

func TestRegistrationMutationPlanIsOptionalAndFailsClosedOnEntropyOrMetadata(t *testing.T) {
	policy, err := setting.ParseRegistrationGroupPolicyOptions(map[string]string{
		setting.QuotaForNewUserOption: "9",
	})
	require.NoError(t, err)

	disabled, err := planRegistrationMutation(policy, "alice", false, 10, func(int) (string, error) {
		panic("entropy must not be requested when generation is disabled")
	})
	require.NoError(t, err)
	assert.Equal(t, 9, disabled.QuotaForNewUser())
	assert.False(t, disabled.HasDefaultToken())
	token, err := disabled.DefaultTokenForUser(1)
	require.NoError(t, err)
	assert.Nil(t, token)

	failure := errors.New("entropy offline")
	_, err = planRegistrationMutation(policy, "alice", true, 10, func(int) (string, error) {
		return "", failure
	})
	require.ErrorIs(t, err, ErrDefaultTokenEntropy)
	require.ErrorContains(t, err, "entropy offline")

	for _, test := range []struct {
		name      string
		username  string
		now       int64
		generator func(int) (string, error)
		wantErr   error
	}{
		{name: "empty username", username: "", now: 1, generator: validDefaultTokenCredential, wantErr: ErrRegistrationUsernameInvalid},
		{name: "trimmed username", username: " alice", now: 1, generator: validDefaultTokenCredential, wantErr: ErrRegistrationUsernameInvalid},
		{name: "control username", username: "ali\nce", now: 1, generator: validDefaultTokenCredential, wantErr: ErrRegistrationUsernameInvalid},
		{name: "bidi username", username: "ali\u202ece", now: 1, generator: validDefaultTokenCredential, wantErr: ErrRegistrationUsernameInvalid},
		{name: "long username", username: strings.Repeat("u", maxRegistrationUsernameBytes+1), now: 1, generator: validDefaultTokenCredential, wantErr: ErrRegistrationUsernameInvalid},
		{name: "negative time", username: "alice", now: -1, generator: validDefaultTokenCredential, wantErr: ErrDefaultTokenPlanInvalid},
		{name: "nil generator", username: "alice", now: 1, wantErr: ErrDefaultTokenPlanInvalid},
		{name: "short credential", username: "alice", now: 1, generator: func(int) (string, error) { return "short", nil }, wantErr: ErrDefaultTokenPlanInvalid},
		{name: "unsafe credential", username: "alice", now: 1, generator: func(size int) (string, error) { return strings.Repeat("-", size), nil }, wantErr: ErrDefaultTokenPlanInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := planRegistrationMutation(policy, test.username, true, test.now, test.generator)
			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestRegistrationMutationPlanMaterializationIsConcurrentAndDetached(t *testing.T) {
	policy, err := setting.ParseRegistrationGroupPolicyOptions(map[string]string{
		setting.QuotaForNewUserOption:     "17",
		setting.DefaultUseAutoGroupOption: "true",
	})
	require.NoError(t, err)
	plan, err := planRegistrationMutation(policy, "parallel", true, 99, validDefaultTokenCredential)
	require.NoError(t, err)

	const workers = 32
	var wait sync.WaitGroup
	errorsSeen := make(chan error, workers)
	wait.Add(workers)
	for worker := 1; worker <= workers; worker++ {
		go func(userID int) {
			defer wait.Done()
			token, tokenErr := plan.DefaultTokenForUser(userID)
			if tokenErr != nil {
				errorsSeen <- tokenErr
				return
			}
			if token.UserId != userID || token.Group != GroupAuto || token.Key != "sk-"+strings.Repeat("A", defaultTokenCredentialBytes) {
				errorsSeen <- errors.New("materialized a torn default-token plan")
			}
			token.Key = "caller mutation"
		}(worker)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		require.NoError(t, err)
	}
	final, err := plan.DefaultTokenForUser(1)
	require.NoError(t, err)
	assert.Equal(t, "sk-"+strings.Repeat("A", defaultTokenCredentialBytes), final.Key)
}

func TestGenerateDefaultTokenEnvironmentDefaultsFalse(t *testing.T) {
	t.Setenv(GenerateDefaultTokenEnvironment, "")
	assert.False(t, GenerateDefaultTokenEnabled())
	t.Setenv(GenerateDefaultTokenEnvironment, "not-a-boolean")
	assert.False(t, GenerateDefaultTokenEnabled())
	t.Setenv(GenerateDefaultTokenEnvironment, "true")
	assert.True(t, GenerateDefaultTokenEnabled())
	t.Setenv(GenerateDefaultTokenEnvironment, "false")
	assert.False(t, GenerateDefaultTokenEnabled())
}

func validDefaultTokenCredential(size int) (string, error) {
	return strings.Repeat("A", size), nil
}
