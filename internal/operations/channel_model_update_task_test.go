package operations

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	userssvc "github.com/tokenrouter/tokenrouter/internal/users"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupModelUpdateTaskDB(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_DISABLE", "true")
	httpx.InitSSRF()
	t.Cleanup(httpx.InitSSRF)
	initTaskDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
	require.NoError(t, channelssvc.InitAbilityCache())
}

func TestChannelUpstreamModelUpdateDurableTaskLifecycle(t *testing.T) {
	setupModelUpdateTaskDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"old"},{"id":"new"}]}`))
	}))
	t.Cleanup(server.Close)
	priority, weight := int64(0), uint(1)
	channel := model.Channel{
		Type: int(channelcatalog.ChannelTypeOpenAI), Key: "secret-task",
		BaseURL: server.URL, Name: "task", Models: "old", Group: userssvc.GroupDefault,
		Status: channelcatalog.ChannelStatusEnabled, Priority: &priority, Weight: &weight,
		OtherSettings: `{"upstream_model_update_check_enabled":true,"upstream_model_update_auto_sync_enabled":true}`,
	}
	require.NoError(t, channelssvc.CreateChannelWithAbilities(&channel))

	task, created, err := EnqueueSystemTask(model.SystemTaskTypeModelUpdate, modelUpdateTaskPayload{Manual: true})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, RunPendingSystemTasksOnce())

	reloadedTask, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedTask)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloadedTask.Status, reloadedTask.Error)
	var summary channelssvc.ChannelUpstreamUpdateSummary
	require.NoError(t, jsonutil.UnmarshalJsonStr(reloadedTask.Result, &summary))
	assert.Equal(t, 1, summary.CheckedChannels)
	assert.Equal(t, 1, summary.ChangedChannels)
	assert.Equal(t, 1, summary.DetectedAddModels)
	assert.Equal(t, 0, summary.AutoAddedModels, "manual task must stage rather than auto-apply")
	assert.Contains(t, reloadedTask.State, `"progress":100`)

	var reloadedChannel model.Channel
	require.NoError(t, model.DB.First(&reloadedChannel, channel.Id).Error)
	assert.Equal(t, "old", reloadedChannel.Models)
	var persisted channelssvc.ChannelUpstreamModelSettings
	require.NoError(t, jsonutil.UnmarshalJsonStr(reloadedChannel.OtherSettings, &persisted))
	assert.Equal(t, []string{"new"}, persisted.LastDetectedModels)
}

func TestModelUpdateTaskWithNoEligibleChannelsCompletesAtOneHundredPercent(t *testing.T) {
	setupModelUpdateTaskDB(t)
	task, created, err := EnqueueSystemTask(model.SystemTaskTypeModelUpdate, modelUpdateTaskPayload{Manual: true})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, RunPendingSystemTasksOnce())

	reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloaded.Status, reloaded.Error)
	assert.JSONEq(t, `{"processed":0,"total":0,"progress":100}`, reloaded.State)
	assert.JSONEq(t, `{"checked_channels":0,"changed_channels":0,"detected_add_models":0,"detected_remove_models":0,"failed_channels":0,"auto_added_models":0}`, reloaded.Result)
}
