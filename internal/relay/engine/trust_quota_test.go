package engine

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	quotamath "github.com/tokenrouter/tokenrouter/internal/billing/quota"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"net/http"
	"testing"
)

func TestOrdinaryRelayTrustQuotaUsesDurableZeroHoldAndSettlesActual(t *testing.T) {
	const (
		maxTokens    = 24
		initialQuota = 10*quotamath.QuotaPerUnit + 100_000
	)
	fixture := newRelayAccountingFixture(t, initialQuota, initialQuota)
	c, recorder, info := newRelayAccountingContext(t, &fixture.token, maxTokens)
	usage := &protocolkit.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}

	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		var record model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.Order("id desc").First(&record).Error)
		assert.Equal(t, model.RelayQuotaReservationStatusDispatched, record.Status)
		assert.True(t, record.TrustQuotaBypassed)
		assert.Positive(t, record.RequestedQuota)
		assert.Zero(t, record.ReservedQuota)
		assert.Zero(t, record.TokenReserved)
		assert.Equal(t, record.RequestedQuota, record.ActualQuota,
			"crash recovery retains the estimate even though no hold was created")
		var user model.User
		var token model.Token
		require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
		require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
		assert.Equal(t, initialQuota, user.Quota)
		assert.Equal(t, initialQuota, token.RemainQuota)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return usage, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	actual := billingsvc.ComputeQuota("accounting-model", "default", usage.PromptTokens, usage.CompletionTokens)
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, initialQuota-actual, user.Quota)
	assert.Equal(t, actual, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, initialQuota-actual, token.RemainQuota)
	assert.Equal(t, actual, token.UsedQuota)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&record).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, record.Status)
	assert.Equal(t, actual, record.ActualQuota)
	assert.True(t, record.TrustQuotaBypassed)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", billingsvc.LogTypeConsume).First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, true, other["trust_quota_bypassed"])
	assert.Equal(t, record.ReservationID, other["relay_reservation_id"])
}

func TestOrdinaryRelayTrustQuotaRetryFailureRefundsZeroHoldExactlyOnce(t *testing.T) {
	t.Setenv("RETRY_TIMES", "1")
	const initialQuota = 10*quotamath.QuotaPerUnit + 100_000
	fixture := newRelayAccountingFixture(t, initialQuota, initialQuota)
	weight := uint(1)
	secondChannel := model.Channel{
		Name: "accounting-retry-channel", Type: fixture.channel.Type, Key: "upstream-key-2",
		Status: fixture.channel.Status, BaseURL: fixture.channel.BaseURL,
		Models: fixture.channel.Models, Group: fixture.channel.Group, Weight: &weight,
	}
	require.NoError(t, model.DB.Create(&secondChannel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "accounting-model", ChannelId: secondChannel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())

	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 24)
	dispatches := 0
	err := relayAndSettleWithDispatch(c, info, func(_ *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		dispatches++
		return nil, errors.New("upstream unavailable")
	})
	require.NoError(t, err)
	assert.Equal(t, 2, dispatches)
	assert.GreaterOrEqual(t, recorder.Code, http.StatusInternalServerError)

	var user model.User
	var token model.Token
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	assert.Equal(t, initialQuota, user.Quota)
	assert.Equal(t, initialQuota, token.RemainQuota)
	assert.Zero(t, user.UsedQuota)
	assert.Zero(t, user.RequestCount)
	assert.Zero(t, token.UsedQuota)
	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&record).Error)
	assert.True(t, record.TrustQuotaBypassed)
	assert.Zero(t, record.ReservedQuota)
	assert.Zero(t, record.TokenReserved)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, record.Status)
	var logs int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Count(&logs).Error)
	assert.Zero(t, logs)
}
