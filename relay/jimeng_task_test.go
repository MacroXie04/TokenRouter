package relay

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	"github.com/tokenrouter/tokenrouter/service"
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

func TestConcurrentJimengPendingSettlementCommitsOnce(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-pending.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}))
	model.DB = db

	user := model.User{Username: "jimeng-pending", Status: model.UserStatusEnabled, Quota: 95}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-jimeng-pending", Status: service.TokenStatusEnabled, RemainQuota: 95}
	require.NoError(t, db.Create(&token).Error)
	privateData := jimengTaskPrivateData{
		UpstreamTaskID: "upstream-pending", BillingSource: service.BillingSourceWallet,
		FundingReserved: 5, TokenID: token.Id, TokenReserved: true, SettlementPending: true,
	}
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	task := model.Task{
		TaskID: "task_concurrent_pending", UserId: user.Id, ChannelId: 7, Quota: 5,
		Status: model.TaskStatusSubmitted, PrivateData: privateJSON, Data: `{"accepted":true}`,
	}
	require.NoError(t, db.Create(&task).Error)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var taskCopy model.Task
			if err := db.First(&taskCopy, task.ID).Error; err != nil {
				errs <- err
				return
			}
			_, pending := decodeJimengTaskMetadata(taskCopy)
			<-start
			errs <- settlePendingJimengTask(&taskCopy, &pending)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	assert.NotContains(t, task.PrivateData, "settlement_pending")
	assert.Equal(t, 5, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 5, token.UsedQuota)
}
