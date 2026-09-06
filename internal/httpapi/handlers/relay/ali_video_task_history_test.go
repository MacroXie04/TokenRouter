package relay

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func TestAliWanTaskHistoryUsesOnlyBoundedNormalizedPublicState(t *testing.T) {
	task := model.Task{
		TaskID:   "task_0123456789abcdef0123456789abcdef",
		Platform: model.TaskOperationPlatformAliWan,
		Status:   model.TaskStatusSuccess,
		Data:     `{"state":"succeeded","result_url":"https://cdn.example/video.mp4","provider_secret":"must-not-escape"}`,
	}
	items := taskHistoryDTOs([]model.Task{task}, nil)
	require.Len(t, items, 1)
	assert.Equal(t, "https://cdn.example/video.mp4", items[0].ResultURL)
	assert.JSONEq(t, `{"state":"succeeded","result_url":"https://cdn.example/video.mp4"}`, string(items[0].Data))
	assert.NotContains(t, string(items[0].Data), "provider_secret")

	task.Data = `{"state":"succeeded","result_url":"file:///private/video.mp4"}`
	items = taskHistoryDTOs([]model.Task{task}, nil)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].ResultURL)
	assert.Equal(t, "null", string(items[0].Data))

	task.Data = strings.Repeat("x", (1<<20)+1)
	items = taskHistoryDTOs([]model.Task{task}, nil)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].ResultURL)
	assert.Equal(t, "null", string(items[0].Data))
}
