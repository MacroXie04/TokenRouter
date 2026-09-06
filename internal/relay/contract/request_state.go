package contract

import (
	"github.com/tokenrouter/tokenrouter/internal/relay/policy"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

// RequestState is the authenticated input to a relay lifecycle. The HTTP
// boundary captures it once after authentication; the engine and durable task
// implementations never recover authorization from transport context keys.
// Token and Groups are owned snapshots and must not be mutated by the caller.
type RequestState struct {
	UserID          int
	Username        string
	UserGroup       string
	TokenID         int
	TokenName       string
	RequestID       string
	Token           *model.Token
	Groups          []string
	CrossGroupRetry bool
	ModelPolicy     policy.TokenModelPolicy

	// OnGroupSelected publishes routing output to the transport's observers.
	// It is never consulted for an authorization decision.
	OnGroupSelected func(string)
}

func (state RequestState) Allows(modelName string) bool {
	return state.Token != nil && state.ModelPolicy.Valid() && state.ModelPolicy.Allows(modelName)
}

func (state RequestState) FirstGroup() string {
	if len(state.Groups) == 0 {
		return ""
	}
	return state.Groups[0]
}

func (state RequestState) SelectGroup(group string) {
	if state.OnGroupSelected != nil {
		state.OnGroupSelected(group)
	}
}
