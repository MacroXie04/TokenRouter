package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http/httptest"
	"testing"
)

func TestCaptureRelayRequestStateOwnsAuthorizationSnapshot(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ips := "192.0.2.1"
	token := &model.Token{Id: 7, UserId: 11, Name: "client", Group: "vip", AllowIps: &ips,
		ModelLimitsEnabled: true, ModelLimits: "gpt-4o"}
	SetupRelayTokenContext(c, token)
	groups := []string{"vip", "default"}
	SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: groups, CrossGroupRetry: true})
	requestctx.SetUserId(c, token.UserId)
	requestctx.SetUsername(c, "alice")
	requestctx.SetUserGroup(c, "vip")
	requestctx.SetRequestId(c, "request-123")

	state := CaptureRelayRequestState(c)
	require.NotNil(t, state.Token)
	token.Name, token.ModelLimits = "changed", "other-model"
	ips = "192.0.2.2"
	groups[0] = "forbidden"
	SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{"forbidden"}})
	requestctx.SetUserId(c, 99)
	assert.Equal(t, 11, state.UserID)
	assert.Equal(t, "alice", state.Username)
	assert.Equal(t, "vip", state.UserGroup)
	assert.Equal(t, 7, state.TokenID)
	assert.Equal(t, "client", state.TokenName)
	assert.Equal(t, "client", state.Token.Name)
	assert.Equal(t, "192.0.2.1", *state.Token.AllowIps)
	assert.Equal(t, "request-123", state.RequestID)
	assert.Equal(t, []string{"vip", "default"}, state.Groups)
	assert.True(t, state.CrossGroupRetry)
	assert.True(t, state.Allows("gpt-4o"))
	assert.False(t, state.Allows("other-model"))

	state.SelectGroup("default")
	assert.Equal(t, "default", GetTokenGroup(c))
	assert.Equal(t, []string{"vip", "default"}, state.Groups)
}

func TestCaptureRelayRequestStateFailsClosedWithoutRelayAuthentication(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(requestctx.ContextKeyToken, &model.Token{Id: 7})
	state := CaptureRelayRequestState(c)
	assert.Nil(t, state.Token)
	assert.False(t, state.Allows("gpt-4o"))
	assert.Empty(t, state.Groups)
}
