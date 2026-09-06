package router_test

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/router"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSystemTaskLogCleanupRequiresRoot(t *testing.T) {
	unauthenticated := router.SetUpRouter()
	recorder := httptest.NewRecorder()
	unauthenticated.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/system-task/log-cleanup?target_timestamp=1", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)

	_, adminRequest, _ := setupDashboardSession(t, roles.RoleAdminUser)
	recorder = adminRequest(http.MethodPost, "/api/system-task/log-cleanup?target_timestamp=1", "")
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestSystemTaskLogCleanupContract(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	now := wallclock.NowTimestamp()
	target := now - 3600

	// Enough old rows (30 batches of 100) to keep the cleanup task active for
	// at least tens of milliseconds: the shared background runner may already
	// be running (started by other tests in this package), so the dedupe and
	// current-task assertions need the task to stay pending/running while they
	// execute.
	const oldCount = 3000
	oldLogs := make([]model.Log, 0, oldCount)
	for i := 0; i < oldCount; i++ {
		oldLogs = append(oldLogs, model.Log{
			UserId: 1, Username: "chreader", ModelName: "gpt-4o",
			Type: billingsvc.LogTypeConsume, Quota: 10, CreatedAt: target - 1000,
		})
	}
	require.NoError(t, model.LOG_DB.CreateInBatches(&oldLogs, 100).Error)

	// The non-ClickHouse cleanup primitive must honor its batch limit on every
	// SQL dialect; selecting IDs first avoids dialect-specific DELETE LIMIT.
	deleted, err := model.DeleteOldLogBatch(context.Background(), target, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(100), deleted)
	remainingAfterBatch, err := model.CountOldLog(context.Background(), target)
	require.NoError(t, err)
	assert.Equal(t, int64(oldCount-100), remainingAfterBatch)
	replacements := make([]model.Log, 100)
	copy(replacements, oldLogs[:100])
	for i := range replacements {
		replacements[i].Id = 0
	}
	require.NoError(t, model.LOG_DB.CreateInBatches(&replacements, 100).Error)

	require.NoError(t, model.LOG_DB.Create(&model.Log{
		UserId: 1, Username: "chreader", ModelName: "gpt-4o",
		Type: billingsvc.LogTypeConsume, Quota: 10, CreatedAt: now,
	}).Error)

	// Missing target timestamp.
	rec := do(http.MethodPost, "/api/system-task/log-cleanup", "")
	body := decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "target timestamp is required", body["message"])
	rec = do(http.MethodPost, fmt.Sprintf("/api/system-task/log-cleanup?target_timestamp=%d", now+3600), "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "target timestamp cannot be in the future", body["message"])

	// Enqueue: pending task with the reference payload shape.
	rec = do(http.MethodPost, fmt.Sprintf("/api/system-task/log-cleanup?target_timestamp=%d", target), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, model.SystemTaskTypeLogCleanup, data["type"])
	assert.Equal(t, model.SystemTaskStatusPending, data["status"])
	payload, ok := data["payload"].(map[string]any)
	require.True(t, ok, "payload decoded as JSON: %s", rec.Body.String())
	assert.Equal(t, float64(target), payload["target_timestamp"])
	assert.Equal(t, float64(100), payload["batch_size"])
	taskID := data["task_id"].(string)

	// A second enqueue returns the same active task (dedupe).
	rec = do(http.MethodPost, fmt.Sprintf("/api/system-task/log-cleanup?target_timestamp=%d", target), "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, taskID, data["task_id"])

	// Current task: the pending task is active.
	rec = do(http.MethodGet, "/api/system-task/current?type=log_cleanup", "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, taskID, data["task_id"])

	// Missing type.
	rec = do(http.MethodGet, "/api/system-task/current", "")
	assert.Equal(t, "type is required", decodeBody(t, rec)["message"])

	// Get by task id before completion.
	rec = do(http.MethodGet, "/api/system-task/"+taskID, "")
	data = decodeBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, taskID, data["task_id"])

	// A controlled one-shot runner executes the cleanup without leaving a
	// process-global goroutine attached to this test database.
	operationssvc.RunPendingSystemTasksOnce()
	var task model.SystemTask
	deadline := time.Now().Add(15 * time.Second)
	for {
		require.NoError(t, model.DB.Where("task_id = ?", taskID).First(&task).Error)
		if task.Status == model.SystemTaskStatusSucceeded || task.Status == model.SystemTaskStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup task %s did not finish; status=%s error=%s", taskID, task.Status, task.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, model.SystemTaskStatusSucceeded, task.Status, "task error: %s", task.Error)
	assert.Nil(t, task.ActiveKey, "active key cleared on completion")
	var result map[string]any
	require.NoError(t, jsonutil.UnmarshalJsonStr(task.Result, &result))
	assert.Equal(t, float64(oldCount), result["deleted_count"])
	var oldLeft, freshCount int64
	model.LOG_DB.Model(&model.Log{}).Where("created_at < ?", target).Count(&oldLeft)
	model.LOG_DB.Model(&model.Log{}).Where("created_at >= ?", target).Count(&freshCount)
	assert.Zero(t, oldLeft, "old logs deleted")
	assert.Equal(t, int64(1), freshCount, "fresh logs kept")

	// Completed task is no longer active: current returns null.
	rec = do(http.MethodGet, "/api/system-task/current?type=log_cleanup", "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Nil(t, body["data"])

	// List contains the completed task with decoded result.
	rec = do(http.MethodGet, "/api/system-task/list", "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	list := body["data"].([]any)
	require.NotEmpty(t, list)
	found := false
	for _, item := range list {
		m := item.(map[string]any)
		if m["task_id"] == taskID {
			found = true
			assert.Equal(t, model.SystemTaskStatusSucceeded, m["status"])
			resultMap, ok := m["result"].(map[string]any)
			require.True(t, ok, "result decoded as JSON")
			assert.Equal(t, float64(oldCount), resultMap["deleted_count"])
		}
	}
	assert.True(t, found, "task present in list")

	// Unknown task id → 404 with the reference message.
	rec = do(http.MethodGet, "/api/system-task/unknown-task", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "task not found", body["message"])
}

func TestSystemInfoInstancesContract(t *testing.T) {
	_, do, _ := setupDashboardSession(t, roles.RoleRootUser)
	now := wallclock.NowTimestamp()
	fresh := model.SystemInstance{NodeName: "node-fresh", Info: `{"version":"1.0"}`,
		StartedAt: now - 1000, LastSeenAt: now - 10}
	stale := model.SystemInstance{NodeName: "node-stale", Info: "not-json",
		StartedAt: now - 10000, LastSeenAt: now - 200}
	require.NoError(t, model.DB.Create(&fresh).Error)
	require.NoError(t, model.DB.Create(&stale).Error)

	rec := do(http.MethodGet, "/api/system-info/instances", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	instances := body["data"].([]any)
	require.Len(t, instances, 2)
	first := instances[0].(map[string]any)
	assert.Equal(t, "node-fresh", first["node_name"], "newest heartbeat first")
	assert.Equal(t, "online", first["status"])
	assert.Equal(t, float64(90), first["stale_after_seconds"])
	infoMap, ok := first["info"].(map[string]any)
	require.True(t, ok, "info decoded as JSON")
	assert.Equal(t, "1.0", infoMap["version"])
	second := instances[1].(map[string]any)
	assert.Equal(t, "stale", second["status"])
	assert.Equal(t, "not-json", second["info"], "unparseable info returned verbatim")

	// Delete one stale node.
	rec = do(http.MethodDelete, "/api/system-info/instances/node-stale", "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(1), body["data"].(map[string]any)["deleted_count"])

	// Fresh nodes are protected from single deletion.
	rec = do(http.MethodDelete, "/api/system-info/instances/node-fresh", "")
	body = decodeBody(t, rec)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "instance is not stale or no longer exists", body["message"])

	// Bulk stale sweep removes the remainder of stale nodes only.
	require.NoError(t, model.DB.Create(&model.SystemInstance{
		NodeName: "node-stale-2", Info: "", StartedAt: now - 10000, LastSeenAt: now - 300,
	}).Error)
	rec = do(http.MethodDelete, "/api/system-info/stale-instances", "")
	body = decodeBody(t, rec)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, float64(1), body["data"].(map[string]any)["deleted_count"])
	var remaining []model.SystemInstance
	require.NoError(t, model.DB.Find(&remaining).Error)
	require.Len(t, remaining, 1)
	assert.Equal(t, "node-fresh", remaining[0].NodeName)
}
