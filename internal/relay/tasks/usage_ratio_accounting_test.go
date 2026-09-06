package tasks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

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
	assert.Equal(t, billingsvc.BillingSourceFreeModel, record.FundingSource)
	assert.Zero(t, record.ReservedQuota)
	assert.Zero(t, record.TokenReserved)
	persisted, err := decodeVideoTaskPrivateData(task.PrivateData)
	require.NoError(t, err)
	assert.True(t, persisted.FreeModel)
	assert.Equal(t, billingsvc.BillingSourceFreeModel, persisted.BillingSource)

	nonzero := task
	nonzero.TaskID = taskID + "x"
	nonzero.Quota = 1
	_, err = createVideoReservedTask(&nonzero, &fixture.token, &videoTaskPrivateData{
		ChannelBaseURL: fixture.channel.BaseURL, EncryptedChannelKey: encryptedKey, FreeModel: true,
	})
	require.ErrorContains(t, err, "requires zero quota")
}
