package policy

import (
	"github.com/stretchr/testify/assert"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func TestTokenModelPolicy(t *testing.T) {
	tests := []struct {
		name        string
		token       *model.Token
		model       string
		wantLimited bool
		wantValid   bool
		wantAllowed bool
	}{
		{name: "unrestricted", token: &model.Token{}, model: "gpt-4", wantValid: true, wantAllowed: true},
		{name: "explicitly allowed", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4, claude-3"}, model: "gpt-4", wantLimited: true, wantValid: true, wantAllowed: true},
		{name: "not listed", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4"}, model: "gpt-4o", wantLimited: true, wantValid: true},
		{name: "case sensitive", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4"}, model: "GPT-4", wantLimited: true, wantValid: true},
		{name: "empty", token: &model.Token{ModelLimitsEnabled: true}, model: "gpt-4", wantLimited: true},
		{name: "whitespace only", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "  "}, model: "gpt-4", wantLimited: true},
		{name: "empty member", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4,,claude-3"}, model: "gpt-4", wantLimited: true},
		{name: "trailing delimiter", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4,"}, model: "gpt-4", wantLimited: true},
		{name: "embedded whitespace", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt 4"}, model: "gpt 4", wantLimited: true},
		{name: "invalid utf8", token: &model.Token{ModelLimitsEnabled: true, ModelLimits: string([]byte{'g', 0xff})}, model: "gpt-4", wantLimited: true},
		{name: "missing token", model: "gpt-4", wantLimited: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := NewTokenModelPolicy(test.token)
			assert.Equal(t, test.wantLimited, policy.Limited())
			assert.Equal(t, test.wantValid, policy.Valid())
			assert.Equal(t, test.wantAllowed, policy.Allows(test.model))
		})
	}
}

func TestTokenModelPolicyFilter(t *testing.T) {
	catalog := map[string]bool{"gpt-4": true, "gpt-4o": true, "disabled": false}

	restricted := NewTokenModelPolicy(&model.Token{ModelLimitsEnabled: true, ModelLimits: "gpt-4"})
	assert.Equal(t, map[string]bool{"gpt-4": true}, restricted.Filter(catalog))
	assert.Equal(t, map[string]bool{"gpt-4": true, "gpt-4o": true}, NewTokenModelPolicy(&model.Token{}).Filter(catalog))
	assert.Empty(t, NewTokenModelPolicy(&model.Token{ModelLimitsEnabled: true, ModelLimits: ","}).Filter(catalog))

	assert.Equal(t, map[string]bool{"gpt-4": true, "gpt-4o": true, "disabled": false}, catalog, "filter must not mutate its input")
}
