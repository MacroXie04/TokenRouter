package relay

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
)

func TestPersistJimengTaskResultCannotRegressTerminalState(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-result.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	model.DB = db

	original := model.Task{
		TaskID: "task_terminal_monotonic", Status: model.TaskStatusSubmitted,
		Progress: "0%", PrivateData: `{}`, Data: `null`,
	}
	require.NoError(t, db.Create(&original).Error)
	stale := original

	doneResult := &jimeng.TaskResult{Message: "success"}
	doneResult.Data.Status = "done"
	doneResult.Data.VideoURL = "https://cdn.example.test/done.mp4"
	donePrivate := jimengTaskPrivateData{UpstreamTaskID: "upstream-1"}
	applyJimengTaskResult(&original, doneResult, []byte(`{"status":"done"}`), &donePrivate)
	require.NoError(t, persistJimengTaskResult(&original))
	assert.Equal(t, model.TaskStatusSuccess, original.Status)

	runningResult := &jimeng.TaskResult{Message: "running"}
	runningResult.Data.Status = "running"
	runningPrivate := jimengTaskPrivateData{UpstreamTaskID: "upstream-1"}
	applyJimengTaskResult(&stale, runningResult, []byte(`{"status":"running"}`), &runningPrivate)
	require.NoError(t, persistJimengTaskResult(&stale))
	assert.Equal(t, model.TaskStatusSuccess, stale.Status)
	assert.Equal(t, "100%", stale.Progress)
	assert.Contains(t, stale.PrivateData, "https://cdn.example.test/done.mp4")
	assert.NotZero(t, stale.FinishTime)

	var stored model.Task
	require.NoError(t, db.First(&stored, original.ID).Error)
	assert.Equal(t, model.TaskStatusSuccess, stored.Status)
	assert.Equal(t, "100%", stored.Progress)
}
