package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/protocolkit"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

func TestOrdinaryReferenceUsageRatiosSettleAndLogCapturedSnapshotForHTTPAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "http"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRelayAccountingFixture(t, 100_000, 100_000)
			configureOrdinaryReferencePricing(t, `{}`, `{"accounting-model":2}`, `{"accounting-model":3}`)
			previousRatios := setting.GetOptions(
				setting.CacheRatioOption,
				setting.CreateCacheRatioOption,
				setting.ImageRatioOption,
				setting.AudioRatioOption,
				setting.AudioCompletionRatioOption,
			)
			t.Cleanup(func() { _ = setting.UpdateOptions(previousRatios) })
			require.NoError(t, setting.UpdateOptions(map[string]string{
				setting.CacheRatioOption:           `{"accounting-model":0.1}`,
				setting.CreateCacheRatioOption:     `{"accounting-model":1.25}`,
				setting.ImageRatioOption:           `{"accounting-model":2.5}`,
				setting.AudioRatioOption:           `{}`,
				setting.AudioCompletionRatioOption: `{}`,
			}))
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", fixture.channel.Id).
				Update("type", int(constant.ChannelTypeAnthropic)).Error)

			c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
			if stream {
				info.Request.Stream = true
				info.Request.Extra["stream"] = true
			}
			usage := &protocolkit.Usage{
				PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
				PromptTokensDetails: &protocolkit.InputTokenDetails{
					CachedTokens: 10, CachedCreationTokens: 10,
					CacheCreation5mTokens: 5, CacheCreation1hTokens: 5,
					ImageTokens: 4,
				},
			}
			err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
				// All request-time ratios have already been captured. This edit
				// must affect only a later relay, never this settlement or log.
				require.NoError(t, setting.UpdateOptions(map[string]string{
					setting.CacheRatioOption:       `{"accounting-model":9}`,
					setting.CreateCacheRatioOption: `{"accounting-model":9}`,
					setting.ImageRatioOption:       `{"accounting-model":9}`,
				}))
				if stream {
					c.Header("Content-Type", "text/event-stream")
					c.Status(http.StatusOK)
					_, _ = c.Writer.WriteString("data: [DONE]\n\n")
				} else {
					c.JSON(http.StatusOK, gin.H{"ok": true})
				}
				return usage, nil
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			assert.Equal(t, 267, info.Quota)

			var record model.RelayQuotaReservationRecord
			var log model.Log
			require.NoError(t, model.DB.First(&record).Error)
			require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
			assert.Equal(t, 267, record.ActualQuota)
			assert.Equal(t, 267, log.Quota)
			var other map[string]any
			require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
			assert.Equal(t, "anthropic", other["usage_semantic"])
			assert.Equal(t, true, other["claude"])
			assert.Equal(t, 10.0, other["cache_tokens"])
			assert.Equal(t, 0.1, other["cache_ratio"])
			assert.Equal(t, 10.0, other["cache_creation_tokens"])
			assert.Equal(t, 1.25, other["cache_creation_ratio"])
			assert.Equal(t, 5.0, other["cache_creation_tokens_5m"])
			assert.Equal(t, 1.25, other["cache_creation_ratio_5m"])
			assert.Equal(t, 5.0, other["cache_creation_tokens_1h"])
			assert.Equal(t, 2.0, other["cache_creation_ratio_1h"])
			assert.Equal(t, 4.0, other["image_output"])
			assert.Equal(t, 2.5, other["image_ratio"])
		})
	}
}

func TestOrdinaryZeroPriceUsesExplicitFreeModelReservationWhenPolicyDisabled(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	previousPrices := service.ExportedModelPrices()
	service.SetModelPriceRegistry(map[string]service.ModelPrice{"accounting-model": {}})
	t.Cleanup(func() { service.SetModelPriceRegistry(previousPrices) })
	previous := setting.GetOptions(setting.ModelBillingModeOption, setting.EnableFreeModelPreConsumeOption)
	t.Cleanup(func() { _ = setting.UpdateOptions(previous) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:          `{}`,
		setting.EnableFreeModelPreConsumeOption: "false",
	}))

	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		var record model.RelayQuotaReservationRecord
		require.NoError(t, model.DB.First(&record).Error)
		assert.Equal(t, service.BillingSourceFreeModel, record.FundingSource)
		assert.Zero(t, record.ReservedQuota)
		assert.Zero(t, record.TokenReserved)
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return &protocolkit.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}, nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Zero(t, info.Quota)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, service.BillingSourceFreeModel, other["billing_source"])
	assert.Equal(t, true, other["free_model"])
}

func TestOrdinaryReferenceUsagePreservesConvertedClaudeBillingSemantic(t *testing.T) {
	fixture := newRelayAccountingFixture(t, 100_000, 100_000)
	configureOrdinaryReferencePricing(t, `{}`, `{"accounting-model":1}`, `{"accounting-model":3}`)
	previousRatios := setting.GetOptions(
		setting.CacheRatioOption, setting.CreateCacheRatioOption, setting.ImageRatioOption,
	)
	t.Cleanup(func() { _ = setting.UpdateOptions(previousRatios) })
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CacheRatioOption:       `{"accounting-model":0.1}`,
		setting.CreateCacheRatioOption: `{"accounting-model":1.25}`,
		setting.ImageRatioOption:       `{}`,
	}))

	c, recorder, info := newRelayAccountingContext(t, &fixture.token, 64)
	err := relayAndSettleWithDispatch(c, info, func(c *gin.Context, _ *RelayInfo) (*protocolkit.Usage, error) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return protocolkit.ClaudeUsageToOpenAIUsage(&protocolkit.ClaudeUsage{
			InputTokens: 70, CacheReadInputTokens: 10, CacheCreationInputTokens: 20,
			CacheCreation: &protocolkit.ClaudeCacheCreation{
				Ephemeral5mInputTokens: 15, Ephemeral1hInputTokens: 5,
			},
			OutputTokens: 10,
		}), nil
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, 130, info.Quota)

	var log model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", service.LogTypeConsume).First(&log).Error)
	var other map[string]any
	require.NoError(t, json.Unmarshal([]byte(log.Other), &other))
	assert.Equal(t, "anthropic", other["usage_semantic"])
	assert.Equal(t, true, other["claude"])
	assert.Equal(t, 15.0, other["cache_creation_tokens_5m"])
	assert.Equal(t, 5.0, other["cache_creation_tokens_1h"])
	assert.Equal(t, 2.0, other["cache_creation_ratio_1h"])
}

func TestGenericVideoZeroPricePersistsExplicitFreeModelSnapshot(t *testing.T) {
	fixture := newVideoTaskFixture(t, "https://video.example.test")
	properties, err := marshalVideoTaskProperties(videoTaskProperties{
		Input: "free video", UpstreamModelName: "upstream-sora", OriginModelName: "sora-2",
		Seconds: 4, Size: "720x1280",
	})
	require.NoError(t, err)
	taskID, err := model.GenerateSecureTaskID()
	require.NoError(t, err)
	now := int64(1_700_000_000)
	task := model.Task{
		CreatedAt: now, UpdatedAt: now, TaskID: taskID, Platform: videoTaskPlatform,
		UserId: fixture.user.Id, Group: "default", ChannelId: fixture.channel.Id, Quota: 0,
		Action: "textGenerate", Status: model.TaskStatusNotStart, SubmitTime: now,
		Progress: "0%", Properties: properties, Data: "null",
	}
	encryptedKey, err := asyncTaskEncryptBound(
		"upstream-video-key",
		videoChannelCredentialBinding(taskID, fixture.user.Id, fixture.channel.Id, fixture.channel.BaseURL),
	)
	require.NoError(t, err)
	privateData := videoTaskPrivateData{
		ChannelBaseURL: fixture.channel.BaseURL, EncryptedChannelKey: encryptedKey, FreeModel: true,
	}
	reservation, err := createVideoReservedTask(&task, &fixture.token, &privateData)
	require.NoError(t, err)

	var record model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&record).Error)
	assert.Equal(t, service.BillingSourceFreeModel, record.FundingSource)
	assert.Zero(t, record.ReservedQuota)
	assert.Zero(t, record.TokenReserved)
	persisted, err := decodeVideoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	assert.True(t, persisted.FreeModel)
	assert.Equal(t, service.BillingSourceFreeModel, persisted.BillingSource)

	nonzero := task
	nonzero.TaskID = taskID + "x"
	nonzero.Quota = 1
	_, err = createVideoReservedTask(&nonzero, &fixture.token, &videoTaskPrivateData{
		ChannelBaseURL: fixture.channel.BaseURL, EncryptedChannelKey: encryptedKey, FreeModel: true,
	})
	require.ErrorContains(t, err, "requires zero quota")
}
