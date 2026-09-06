package engine

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"testing"
)

func TestOrdinaryRelayRejectsModelQuotaClampAfterAcceptedWork(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 3, Completion: 3},
	})
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 16)
	err := relayAndSettleWithDispatch(c, middleware.CaptureRelayRequestState(c), info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		c.JSON(http.StatusOK, gin.H{"upstream": "accepted"})
		return &protocolkit.Usage{PromptTokens: int(quotamath.MaxQuota), TotalTokens: int(quotamath.MaxQuota)}, nil
	})

	require.ErrorContains(t, err, "model quota conversion was not exact")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "settlement_failed")
	require.NotNil(t, info.QuotaClamp)
	assert.Equal(t, "overflow", info.QuotaClamp.Reason)
	assertAcceptedWorkRemainsUnsettled(t, fixture, 100_000, 100_000)
}

func TestOrdinaryRelayRejectsToolQuotaClampAfterAcceptedWork(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ToolPriceOption: `{"expensive":1000000000000}`,
	}))
	t.Cleanup(func() { _ = setting.UpdateOptions(map[string]string{setting.ToolPriceOption: `{}`}) })
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 16)
	err := relayAndSettleWithDispatch(c, middleware.CaptureRelayRequestState(c), info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		require.NotNil(t, selected.ToolUsageHooks)
		require.NotNil(t, selected.ToolUsageHooks.ObserveChatToolCall)
		require.NoError(t, selected.ToolUsageHooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
			ChoiceIndex: 0, ArrayIndex: 0, ID: "call-expensive", Name: "expensive",
		}))
		c.JSON(http.StatusOK, gin.H{"upstream": "accepted"})
		return &protocolkit.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
	})

	require.ErrorContains(t, err, "tool surcharge quota overflow")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "settlement_failed")
	require.NotNil(t, info.ToolQuotaClamp)
	assert.Equal(t, "overflow", info.ToolQuotaClamp.Reason)
	assertAcceptedWorkRemainsUnsettled(t, fixture, 100_000, 100_000)
}

func TestOrdinaryRelayRejectsCombinedModelAndToolQuotaOverflow(t *testing.T) {
	fixture := newRelayAccountingFixture(t, int(quotamath.MaxQuota), int(quotamath.MaxQuota))
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 2, Completion: 2},
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ToolPriceOption: `{"one_quota":0.002}`,
	}))
	t.Cleanup(func() { _ = setting.UpdateOptions(map[string]string{setting.ToolPriceOption: `{}`}) })
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 16)
	err := relayAndSettleWithDispatch(c, middleware.CaptureRelayRequestState(c), info, func(c *gin.Context, selected *RelayInfo) (*protocolkit.Usage, error) {
		require.NoError(t, selected.ToolUsageHooks.ObserveChatToolCall(relaycommon.ToolChatObservation{
			ChoiceIndex: 0, ArrayIndex: 0, ID: "call-one", Name: "one_quota",
		}))
		c.JSON(http.StatusOK, gin.H{"upstream": "accepted"})
		return &protocolkit.Usage{PromptTokens: int(quotamath.MaxQuota), TotalTokens: int(quotamath.MaxQuota)}, nil
	})

	require.ErrorContains(t, err, "combined model and tool quota exceeds accounting bounds")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "settlement_failed")
	assert.Nil(t, info.QuotaClamp, "the model conversion itself remains exact")
	assert.Nil(t, info.ToolQuotaClamp, "the tool conversion itself remains exact")
	assertAcceptedWorkRemainsUnsettled(t, fixture, int(quotamath.MaxQuota), int(quotamath.MaxQuota))
}

func assertAcceptedWorkRemainsUnsettled(
	t *testing.T,
	fixture relayAccountingFixture,
	initialUserQuota int,
	initialTokenQuota int,
) {
	t.Helper()
	var user model.User
	var token model.Token
	var channel model.Channel
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(&channel, fixture.channel.Id).Error)
	require.NoError(t, model.DB.Order("id desc").First(&reservation).Error)
	assert.LessOrEqual(t, user.Quota, initialUserQuota)
	assert.Zero(t, user.UsedQuota)
	assert.LessOrEqual(t, token.RemainQuota, initialTokenQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Zero(t, channel.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, reservation.Status)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("type = ?", billingsvc.LogTypeConsume).Count(&logs).Error)
	assert.Zero(t, logs)
}
