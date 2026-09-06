package engine

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/relay/policy"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"testing"
)

func TestRelayLifecycleDoesNotRecoverAuthorizationFromRoutingOutput(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	state := middleware.CaptureRelayRequestState(c)
	state.Groups = nil
	info.Group = "default"
	called := false
	require.NoError(t, relayAndSettleWithDispatch(c, state, info, func(*gin.Context, *RelayInfo) (*protocolkit.Usage, error) {
		called = true
		return nil, nil
	}))
	assert.False(t, called)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "group_not_allowed")
	var reservations int64
	require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).Count(&reservations).Error)
	assert.Zero(t, reservations)
}

func TestRelayLifecycleBillsCapturedIdentityAfterTransportStateChanges(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"accounting-model": {Prompt: 1, Completion: 1}})
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	requestctx.SetRequestId(c, "original-request")
	state := middleware.CaptureRelayRequestState(c)
	middleware.SetupRelayTokenContext(c, &model.Token{Id: 999, UserId: 999, Name: "changed", ModelLimitsEnabled: true, ModelLimits: "denied"})
	middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{"forbidden"}})
	requestctx.SetUserId(c, 999)
	requestctx.SetUsername(c, "changed")
	requestctx.SetRequestId(c, "changed-request")
	require.NoError(t, relayAndSettleWithDispatch(c, state, info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		assert.Equal(t, fixture.user.Id, selected.UserID)
		assert.Equal(t, "default", selected.Group)
		c.JSON(http.StatusOK, gin.H{"id": "captured-state"})
		return &protocolkit.Usage{PromptTokens: 31, CompletionTokens: 29, TotalTokens: 60}, nil
	}))
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	assert.Equal(t, fixture.user.Id, log.UserId)
	assert.Equal(t, fixture.token.Id, log.TokenId)
	assert.Equal(t, "accounting-user", log.Username)
	assert.Equal(t, "accounting-token", log.TokenName)
	assert.Equal(t, "original-request", log.RequestId)
	assert.Equal(t, "default", log.Group)
	assert.Positive(t, log.Quota)
}

func TestRelayLifecycleRejectsMissingModelPolicyBeforeReservation(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		name := "unrestricted-token"
		if restricted {
			name = "restricted-token"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRelayAccountingFixture(t, 100_000, 100_000)
			billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{"accounting-model": {Prompt: 1, Completion: 1}})
			fixture.token.ModelLimitsEnabled = restricted
			fixture.token.ModelLimits = "different-model"
			c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
			state := middleware.CaptureRelayRequestState(c)
			state.ModelPolicy = policy.TokenModelPolicy{}
			called := false
			require.NoError(t, relayAndSettleWithDispatch(c, state, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
				called = true
				c.JSON(http.StatusOK, gin.H{"id": "unexpected-dispatch"})
				return &protocolkit.Usage{PromptTokens: 31, CompletionTokens: 29, TotalTokens: 60}, nil
			}))
			assert.False(t, called, "missing authorization policy must be rejected before any provider call")
			assert.Equal(t, http.StatusForbidden, recorder.Code)
			assert.Contains(t, recorder.Body.String(), "model_not_allowed")
			var reservations int64
			require.NoError(t, model.DB.Model(&model.RelayQuotaReservationRecord{}).Count(&reservations).Error)
			assert.Zero(t, reservations, "missing policy must not reserve token or user quota")
		})
	}
}
