package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	relaycontract "github.com/tokenrouter/tokenrouter/internal/relay/contract"
)

// CaptureRelayRequestState takes an owned snapshot of the authenticated relay
// inputs at the HTTP boundary. Routing output may be published back to the
// context, but the lifecycle never reads authorization from that output.
func CaptureRelayRequestState(c *gin.Context) relaycontract.RequestState {
	groups := GetRelayGroupPolicy(c)
	state := relaycontract.RequestState{
		UserID: requestctx.GetUserId(c), Username: requestctx.GetUsername(c),
		UserGroup: requestctx.GetUserGroup(c), RequestID: requestctx.GetRequestId(c),
		TokenID:   requestctx.GetInt(c, requestctx.ContextKeyTokenId),
		TokenName: requestctx.GetString(c, requestctx.ContextKeyTokenName),
		Groups:    groups.Groups, CrossGroupRetry: groups.CrossGroupRetry,
		ModelPolicy:     GetRelayModelPolicy(c),
		OnGroupSelected: func(group string) { c.Set(requestctx.ContextKeyGroup, group) },
	}
	if token := GetRelayToken(c); token != nil {
		copy := *token
		if token.AllowIps != nil {
			ips := *token.AllowIps
			copy.AllowIps = &ips
		}
		state.Token = &copy
	}
	return state
}
