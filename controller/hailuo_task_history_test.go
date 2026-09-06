package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestHailuoTaskHistoryListExposesOnlyNormalizedPublicState(t *testing.T) {
	task := model.Task{
		ID: 1, TaskID: "task_12345678901234567890123456789012",
		Platform: model.TaskOperationPlatformHailuo, UserId: 7, ChannelId: 9,
		Status: model.TaskStatusSuccess, Action: "generate", Progress: "100%",
		Properties:  `{"version":1,"family":"hailuo","input":"scene","origin_model_name":"MiniMax-Hailuo-2.3","upstream_model_name":"MiniMax-Hailuo-2.3-Fast","pricing":{"secret":"must-not-escape"}}`,
		PrivateData: `hailuo-task-v1:{"encrypted_channel_key":"secret-cipher","encrypted_provider_task_id":"provider-cipher"}`,
		Data:        `{"state":"succeeded","result_url":"https://cdn.example/hailuo.mp4","video_width":1920,"video_height":1080}`,
	}
	items := taskHistoryDTOs([]model.Task{task}, nil)
	require.Len(t, items, 1)
	assert.Equal(t, "https://cdn.example/hailuo.mp4", items[0].ResultURL)
	assert.Equal(t, "scene", items[0].Properties.Input)
	encoded := string(items[0].Data)
	assert.Contains(t, encoded, "https://cdn.example/hailuo.mp4")
	assert.NotContains(t, encoded, "secret-cipher")
	assert.NotContains(t, encoded, "provider-cipher")
	assert.NotContains(t, encoded, "must-not-escape")
}

func TestHailuoTaskHistoryRejectsCorruptProviderStateAndControls(t *testing.T) {
	task := model.Task{
		TaskID: "task_12345678901234567890123456789012", Platform: model.TaskOperationPlatformHailuo,
		FailReason: "bad\x00provider", Data: `{"state":"succeeded","result_url":"file:///tmp/video"}`,
	}
	items := taskHistoryDTOs([]model.Task{task}, nil)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].ResultURL)
	assert.Equal(t, "null", string(items[0].Data))
	assert.Equal(t, "Hailuo task failed", items[0].FailReason)
	assert.False(t, strings.ContainsRune(items[0].FailReason, '\x00'))
}
