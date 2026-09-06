package tasks

import (
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/jimeng"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJimengEncryptionKeyringSupportsRotationAndLegacyCiphertext(t *testing.T) {
	oldSessionSecret := strings.Repeat("old-session-secret-", 3)
	newSessionSecret := strings.Repeat("new-session-secret-", 3)
	oldJimengKey := strings.Repeat("old-jimeng-key-", 3)
	newJimengKey := strings.Repeat("new-jimeng-key-", 3)
	t.Setenv("SESSION_SECRET", oldSessionSecret)
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "old="+oldJimengKey)

	oldCiphertext, err := jimengEncrypt("provider-secret-value")
	require.NoError(t, err)
	assert.Contains(t, oldCiphertext, "jimeng-key-v1:old:")

	// Every node can switch to the new current key while retaining the old key
	// for in-flight rows and journals.
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newJimengKey+",old="+oldJimengKey)
	plaintext, err := jimengDecrypt(oldCiphertext)
	require.NoError(t, err)
	assert.Equal(t, "provider-secret-value", plaintext)
	newCiphertext, err := jimengEncrypt("new-provider-secret")
	require.NoError(t, err)
	assert.Contains(t, newCiphertext, "jimeng-key-v1:new:")

	// Legacy unversioned AES data remains readable across a SESSION_SECRET
	// rotation when that old secret is explicitly retained in the keyring.
	t.Setenv("SESSION_SECRET", oldSessionSecret)
	legacyCiphertext, err := cryptoutil.EncryptByAES("legacy-provider-secret")
	require.NoError(t, err)
	t.Setenv("SESSION_SECRET", newSessionSecret)
	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newJimengKey+",legacy="+oldSessionSecret)
	plaintext, err = jimengDecrypt(legacyCiphertext)
	require.NoError(t, err)
	assert.Equal(t, "legacy-provider-secret", plaintext)

	t.Setenv("JIMENG_ENCRYPTION_KEYS", "new="+newJimengKey)
	_, err = jimengDecrypt(oldCiphertext)
	require.ErrorContains(t, err, "key is not configured")
}

func TestJimengProviderPayloadPersistenceIsCanonicalAndPrivate(t *testing.T) {
	raw := []byte(`{"code":10000,"message":"echo access|secret","request_id":"provider-request-id","data":{"task_id":"provider-task-id","status":"done","video_url":"https://cdn.example.test/secret"}}`)
	stored := durableJimengProviderPayload(raw)
	require.NotEmpty(t, stored)
	assert.NotContains(t, string(stored), "access|secret")
	assert.NotContains(t, string(stored), "provider-task-id")
	assert.NotContains(t, string(stored), "cdn.example.test")
	assert.Contains(t, string(stored), cryptoutil.NormalizeProviderCorrelationID("provider-request-id"))
	assert.Contains(t, string(stored), `"status":"done"`)

	privateData := jimengTaskPrivateData{
		RelayReservationID: cryptoutil.BestEffortUUID(), UpstreamTaskID: "provider-task-id",
	}
	privateJSON, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	assert.NotContains(t, privateJSON, "provider-task-id")
	assert.Contains(t, privateJSON, "encrypted_upstream_task_id")
	decoded, err := decodeJimengTaskPrivateData(privateJSON)
	require.NoError(t, err)
	assert.Equal(t, "provider-task-id", decoded.UpstreamTaskID)
}

func TestJimengRecoveryRecordRoundTripAndRemoval(t *testing.T) {
	recoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	envelope := jimengRecoveryEnvelope{
		UserID: 7, TaskID: "task_recovery_round_trip", ChannelID: 11,
		UpstreamTaskID: "upstream-11", Status: model.TaskStatusSubmitted,
	}
	require.NoError(t, persistJimengRecovery(envelope))
	storedBytes, err := os.ReadFile(filepath.Join(recoveryDirectory, envelope.TaskID+".json"))
	require.NoError(t, err)
	assert.NotContains(t, string(storedBytes), envelope.UpstreamTaskID,
		"the emergency journal must encrypt provider-controlled identifiers")
	assert.Contains(t, string(storedBytes), `"version":2`)
	info, err := os.Stat(filepath.Join(recoveryDirectory, envelope.TaskID+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	loaded, err := loadJimengRecovery(envelope.TaskID)
	require.NoError(t, err)
	assert.Equal(t, envelope, loaded)
	assert.True(t, hasMatchingJimengRecovery(
		envelope.TaskID, envelope.UserID, envelope.ChannelID, envelope.UpstreamTaskID,
	))
	assert.False(t, hasMatchingJimengRecovery(
		envelope.TaskID, envelope.UserID, envelope.ChannelID, "different-upstream-task",
	))
	legacy := envelope
	legacy.TaskID = "task_legacy_plaintext_upgrade"
	legacy.UpstreamTaskID = "legacy-provider-id"
	legacyBytes, err := marshalJimengRecoveryEnvelope(legacy)
	require.NoError(t, err)
	legacyPath := filepath.Join(recoveryDirectory, legacy.TaskID+".json")
	require.NoError(t, os.WriteFile(legacyPath, legacyBytes, 0o644))
	loadedLegacy, err := loadJimengRecovery(legacy.TaskID)
	require.NoError(t, err)
	assert.Equal(t, legacy, loadedLegacy)
	upgradedBytes, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.NotContains(t, string(upgradedBytes), legacy.UpstreamTaskID)
	assert.Contains(t, string(upgradedBytes), `"version":2`)
	legacyInfo, err := os.Stat(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), legacyInfo.Mode().Perm())
	require.NoError(t, removeJimengRecovery(envelope.TaskID))
	require.NoError(t, removeJimengRecovery(envelope.TaskID), "recovery removal must be idempotent")
	_, err = os.Stat(filepath.Join(recoveryDirectory, envelope.TaskID+".json"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	invalid := envelope
	invalid.UpstreamTaskID = ""
	assert.ErrorContains(t, persistJimengRecovery(invalid), "missing upstream task ID")
}

func TestJimengV2PrivateDataFailsClosedForLegacyJSONReaders(t *testing.T) {
	privateData := jimengTaskPrivateData{
		RelayReservationID: cryptoutil.BestEffortUUID(), BillingSource: billingsvc.BillingSourceWallet,
		FundingReserved: 7, UpstreamTaskID: "provider-id",
	}
	encoded, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encoded, jimengTaskPrivateDataV2Prefix))
	var legacy jimengTaskPrivateData
	require.Error(t, jsonutil.UnmarshalJsonStr(encoded, &legacy),
		"pre-upgrade nodes must reject modern tasks instead of dropping unknown accounting fields")
	decoded, err := decodeJimengTaskPrivateData(encoded)
	require.NoError(t, err)
	assert.Equal(t, privateData.UpstreamTaskID, decoded.UpstreamTaskID)
	assert.NotEmpty(t, decoded.EncryptedUpstreamTaskID)
	privateData.EncryptedUpstreamTaskID = decoded.EncryptedUpstreamTaskID
	assert.Equal(t, privateData, decoded)

	legacyJSON, err := jsonutil.Marshal(privateData)
	require.NoError(t, err)
	decoded, err = decodeJimengTaskPrivateData(string(legacyJSON))
	require.NoError(t, err)
	assert.Equal(t, privateData, decoded, "modern nodes retain read compatibility with legacy JSON")
}

func TestJimengProviderTerminalAndUnknownStatusesAreBounded(t *testing.T) {
	for _, status := range []string{"not_found", "expired"} {
		t.Run(status, func(t *testing.T) {
			task := model.Task{Status: model.TaskStatusSubmitted, Progress: "10%", Data: `null`}
			privateData := jimengTaskPrivateData{UpstreamTaskID: "provider-id"}
			result := &jimeng.TaskResult{Message: "provider-controlled detail"}
			result.Data.Status = status
			require.NoError(t, applyJimengTaskResult(&task, result, []byte(`{"code":10000}`), &privateData))
			assert.Equal(t, model.TaskStatusFailure, task.Status)
			assert.Equal(t, "100%", task.Progress)
			assert.NotZero(t, task.FinishTime)
			assert.NotContains(t, task.FailReason, "provider-controlled",
				"documented terminal states use a stable failure reason")
		})
	}

	task := model.Task{Status: model.TaskStatusRunning, Progress: "50%", Data: `old`}
	privateData := jimengTaskPrivateData{UpstreamTaskID: "provider-id"}
	result := &jimeng.TaskResult{}
	result.Data.Status = "future_provider_state"
	require.ErrorContains(t, applyJimengTaskResult(&task, result, []byte(`{"status":"future"}`), &privateData),
		"unsupported Jimeng provider task status")
	assert.Equal(t, model.TaskStatusRunning, task.Status)
	assert.Equal(t, "50%", task.Progress)
	assert.Equal(t, `old`, task.Data,
		"unknown states must not overwrite progress before the bounded retry path runs")

	for _, code := range []int{50429, 50430, 50500, 50501, 50511} {
		assert.False(t, isPermanentJimengRecoveryError(&jimeng.ProviderError{Code: code}),
			"documented transient provider code %d must consume the bounded retry budget", code)
	}
}

func TestJimengDurableTextBoundsPreventMySQLRecoveryDeadEnds(t *testing.T) {
	_, err := marshalJimengTaskProperties(jimengTaskProperties{
		Input: strings.Repeat("p", jimengTaskTextMaxBytes),
	})
	require.ErrorContains(t, err, "task properties exceeds durable storage limit")

	_, err = marshalJimengTaskPrivateData(jimengTaskPrivateData{
		UpstreamTaskID: strings.Repeat("i", jimeng.MaxProviderTaskIDBytes+1),
	})
	require.ErrorContains(t, err, "provider task id exceeds durable storage limit")

	task := model.Task{Status: model.TaskStatusSubmitted, Progress: "50%", Data: `null`}
	privateData := jimengTaskPrivateData{UpstreamTaskID: "provider-id"}
	result := &jimeng.TaskResult{}
	result.Data.Status = "done"
	result.Data.VideoURL = strings.Repeat("u", jimengProviderResultURLMaxBytes+1)
	require.NoError(t, applyJimengTaskResult(&task, result, []byte(`{"code":10000}`), &privateData))
	assert.Equal(t, model.TaskStatusFailure, task.Status)
	assert.Equal(t, "provider result exceeds durable storage limit", task.FailReason)
	assert.Empty(t, privateData.ResultURL)

	combinedTask := model.Task{Status: model.TaskStatusSubmitted, Progress: "50%", Data: `null`}
	combinedPrivate := jimengTaskPrivateData{
		UpstreamTaskID:     "provider-id",
		RelayReservationID: cryptoutil.BestEffortUUID(),
		BillingSource:      billingsvc.BillingSourceSubscription,
		SubscriptionID:     123,
		FundingReserved:    1,
		TokenID:            456,
		TokenReserved:      true,
	}
	combinedResult := &jimeng.TaskResult{}
	combinedResult.Data.Status = "done"
	// Choose the largest escaped URL whose provider response still fits. The
	// modern private envelope has slightly more fixed metadata, so its complete
	// JSON crosses the portable TEXT bound even though the source URL remains
	// within the component cap and the provider body is valid.
	low, high := 0, jimengProviderResultURLMaxBytes
	for low < high {
		mid := (low + high + 1) / 2
		combinedResult.Data.VideoURL = strings.Repeat(`\`, mid)
		candidateRaw, marshalErr := jsonutil.Marshal(combinedResult)
		require.NoError(t, marshalErr)
		if len(candidateRaw) <= jimengTaskTextMaxBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	combinedResult.Data.VideoURL = strings.Repeat(`\`, low)
	combinedRaw, err := jsonutil.Marshal(combinedResult)
	require.NoError(t, err)
	require.LessOrEqual(t, len(combinedRaw), jimengTaskTextMaxBytes,
		"the provider body itself is within the durable Task.Data bound")
	encodedCandidate := combinedPrivate
	encodedCandidate.ResultURL = combinedResult.Data.VideoURL
	stripJimengTerminalRecoverySecrets(&encodedCandidate)
	_, err = marshalJimengTaskPrivateData(encodedCandidate)
	require.ErrorContains(t, err, "task private data exceeds durable storage limit",
		"the fixture must exercise complete private-envelope expansion")
	require.NoError(t, applyJimengTaskResult(&combinedTask, combinedResult, combinedRaw, &combinedPrivate))
	assert.Equal(t, model.TaskStatusFailure, combinedTask.Status)
	assert.Equal(t, "provider result exceeds durable storage limit", combinedTask.FailReason)
	assert.Empty(t, combinedPrivate.ResultURL,
		"a fully encoded private blob that exceeds TEXT must be replaced by a durable terminal failure")

	unchanged := model.Task{Status: model.TaskStatusRunning, Progress: "50%", Data: `old`}
	err = applyJimengTaskResult(&unchanged, &jimeng.TaskResult{},
		[]byte(strings.Repeat("x", jimengTaskTextMaxBytes+1)), &jimengTaskPrivateData{})
	require.ErrorContains(t, err, "provider response exceeds durable storage limit")
	assert.Equal(t, model.TaskStatusRunning, unchanged.Status)
	assert.Equal(t, `old`, unchanged.Data)
}

func TestJimengRecoveryCleanupPrioritizesOldAndTerminalRecords(t *testing.T) {
	recoveryDirectory := t.TempDir()
	t.Setenv("JIMENG_RECOVERY_DIR", recoveryDirectory)
	t.Setenv("JIMENG_RECOVERY_RETENTION_DAYS", "1")
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "cleanup.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.JimengTaskOperation{}))
	model.DB = db
	now, err := model.DatabaseUnixTimestamp(db)
	require.NoError(t, err)

	// More recent lexically-earlier rows cannot starve an older later row when
	// the bounded cleanup limit is one.
	for index := 0; index < 3; index++ {
		taskID := fmt.Sprintf("task_aaa_recent_%d", index)
		require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
			UserID: 1, TaskID: taskID, ChannelID: 1,
			UpstreamTaskID: "recent", Status: model.TaskStatusSubmitted,
		}))
	}
	oldTaskID := "task_zzz_expired"
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: 1, TaskID: oldTaskID, ChannelID: 1,
		UpstreamTaskID: "expired", Status: model.TaskStatusSubmitted,
	}))
	oldPath, err := jimengRecoveryPath(oldTaskID)
	require.NoError(t, err)
	oldTime := time.Unix(now-2*24*60*60, 0)
	require.NoError(t, os.Chtimes(oldPath, oldTime, oldTime))
	require.NoError(t, cleanupJimengRecoveryRecords(now, 1))
	_, err = os.Stat(oldPath)
	assert.ErrorIs(t, err, os.ErrNotExist)

	activeTaskID := "task_active_old_recovery"
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: 1, TaskID: activeTaskID, ChannelID: 1,
		UpstreamTaskID: "accepted-active", Status: model.TaskStatusSubmitted,
	}))
	activePath, err := jimengRecoveryPath(activeTaskID)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(activePath, oldTime, oldTime))
	require.NoError(t, db.Create(&model.JimengTaskOperation{
		TaskID: activeTaskID, ReservationID: cryptoutil.BestEffortUUID(), UserID: 1, ChannelID: 1,
		State: model.JimengTaskOperationDispatching, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, cleanupJimengRecoveryRecords(now, 100))
	_, err = os.Stat(activePath)
	assert.NoError(t, err, "age cleanup must retain a journal backing an active database operation")

	crashTemporaryPath := filepath.Join(recoveryDirectory, ".jimeng-recovery-crash-orphan")
	require.NoError(t, os.WriteFile(crashTemporaryPath, []byte(`{"version":2,"ciphertext":"partial"}`), 0o600))
	require.NoError(t, os.Chtimes(crashTemporaryPath, oldTime, oldTime))
	require.NoError(t, cleanupJimengRecoveryRecords(now, 100))
	_, err = os.Stat(crashTemporaryPath)
	assert.ErrorIs(t, err, os.ErrNotExist,
		"old crash-orphaned temporary ciphertext must not accumulate forever")

	terminalTaskID := "task_terminal_cleanup"
	require.NoError(t, persistJimengRecovery(jimengRecoveryEnvelope{
		UserID: 1, TaskID: terminalTaskID, ChannelID: 1,
		UpstreamTaskID: "terminal", Status: model.TaskStatusSubmitted,
	}))
	require.NoError(t, db.Create(&model.JimengTaskOperation{
		TaskID: terminalTaskID, ReservationID: cryptoutil.BestEffortUUID(), UserID: 1, ChannelID: 1,
		State: model.JimengTaskOperationTerminal, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, cleanupJimengRecoveryRecords(now, 100))
	terminalPath, err := jimengRecoveryPath(terminalTaskID)
	require.NoError(t, err)
	_, err = os.Stat(terminalPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestPreparedJimengRecoveryCannotAssumeProviderAcceptance(t *testing.T) {
	privateJSON, err := marshalJimengTaskPrivateData(jimengTaskPrivateData{
		BillingSource: billingsvc.BillingSourceWallet, FundingReserved: 5,
		TokenID: 9, TokenReserved: true,
	})
	require.NoError(t, err)
	task := model.Task{
		TaskID: "task_prepared_not_accepted", UserId: 7, ChannelId: 11,
		Quota: 5, Status: model.TaskStatusNotStart, PrivateData: privateJSON,
	}

	_, err = restoreJimengRecovery(&task, task.UserId, jimengRecoveryEnvelope{
		UserID: task.UserId, TaskID: task.TaskID, ChannelID: task.ChannelId,
		Status: model.TaskStatusUnknown,
	})
	require.ErrorContains(t, err, "does not prove provider acceptance")
}

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
	require.NoError(t, applyJimengTaskResult(&original, doneResult, []byte(`{"status":"done"}`), &donePrivate))
	require.NoError(t, persistJimengTaskResult(&original, model.TaskStatusSubmitted))
	assert.Equal(t, model.TaskStatusSuccess, original.Status)

	runningResult := &jimeng.TaskResult{Message: "running"}
	runningResult.Data.Status = "running"
	runningPrivate := jimengTaskPrivateData{UpstreamTaskID: "upstream-1"}
	require.NoError(t, applyJimengTaskResult(&stale, runningResult, []byte(`{"status":"running"}`), &runningPrivate))
	require.NoError(t, persistJimengTaskResult(&stale, model.TaskStatusSubmitted))
	assert.Equal(t, model.TaskStatusSuccess, stale.Status)
	assert.Equal(t, "100%", stale.Progress)
	assert.Contains(t, stale.PrivateData, "https://cdn.example.test/done.mp4")
	assert.NotZero(t, stale.FinishTime)

	var stored model.Task
	require.NoError(t, db.First(&stored, original.ID).Error)
	assert.Equal(t, model.TaskStatusSuccess, stored.Status)
	assert.Equal(t, "100%", stored.Progress)
}

func TestPersistJimengTaskResultCannotRegressConcurrentNonTerminalState(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-result-nonterminal.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	model.DB = db

	task := model.Task{
		TaskID: "task_nonterminal_monotonic", Status: model.TaskStatusSubmitted,
		Progress: "0%", PrivateData: `{}`, Data: `null`,
	}
	require.NoError(t, db.Create(&task).Error)
	stale := task
	task.Status = model.TaskStatusRunning
	task.Progress = "50%"
	require.NoError(t, persistJimengTaskResult(&task, model.TaskStatusSubmitted))

	stale.Status = model.TaskStatusQueued
	stale.Progress = "10%"
	require.NoError(t, persistJimengTaskResult(&stale, model.TaskStatusSubmitted))
	assert.Equal(t, model.TaskStatusRunning, stale.Status)
	assert.Equal(t, "50%", stale.Progress)

	var stored model.Task
	require.NoError(t, db.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatusRunning, stored.Status)
	assert.Equal(t, "50%", stored.Progress)
}

func TestPersistJimengTaskResultRetriesVisibleStorageFailure(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-result-retry.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	model.DB = db

	task := model.Task{
		TaskID: "task_result_retry", Status: model.TaskStatusSubmitted,
		Progress: "0%", PrivateData: `{}`, Data: `null`,
	}
	require.NoError(t, db.Create(&task).Error)
	task.Status = model.TaskStatusRunning
	task.Progress = "50%"
	task.UpdatedAt = 123

	var failures atomic.Int32
	const callbackName = "test:jimeng_result_retry"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "tasks" && failures.Add(1) <= 2 {
			tx.AddError(errors.New("injected Jimeng task result failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	require.NoError(t, persistJimengTaskResult(&task, model.TaskStatusSubmitted))
	assert.Equal(t, int32(3), failures.Load())
	var stored model.Task
	require.NoError(t, db.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatusRunning, stored.Status)
	assert.Equal(t, "50%", stored.Progress)
}

func TestConcurrentJimengPendingSettlementCommitsOnce(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "jimeng-pending.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Task{}))
	model.DB = db

	user := model.User{Username: "jimeng-pending", Status: model.UserStatusEnabled, Quota: 95}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, Key: "sk-jimeng-pending", Status: billingsvc.TokenStatusEnabled, RemainQuota: 95}
	require.NoError(t, db.Create(&token).Error)
	channel := model.Channel{Id: 7, Name: "jimeng-pending", Key: "upstream"}
	require.NoError(t, db.Create(&channel).Error)
	privateData := jimengTaskPrivateData{
		UpstreamTaskID: "upstream-pending", BillingSource: billingsvc.BillingSourceWallet,
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
	require.NoError(t, db.First(&channel, channel.Id).Error)
	assert.NotContains(t, task.PrivateData, "settlement_pending")
	assert.Equal(t, 5, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 5, token.UsedQuota)
	assert.Equal(t, int64(5), channel.UsedQuota)
}

func TestLegacyJimengSubscriptionSettlementUsesCapturedFundingEpoch(t *testing.T) {
	for _, test := range []struct {
		name           string
		capturedEpoch  int64
		currentEpoch   int64
		amountUsed     int64
		expectedAmount int64
	}{
		{
			name: "matching epoch releases the legacy one-unit hold", capturedEpoch: 7,
			currentEpoch: 7, amountUsed: 1, expectedAmount: 0,
		},
		{
			name: "advanced epoch leaves the current subscription window untouched", capturedEpoch: 7,
			currentEpoch: 8, amountUsed: 37, expectedAmount: 37,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "jimeng-legacy-epoch.db")), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(
				&model.User{}, &model.Task{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
			))
			model.DB = db

			user := model.User{Username: "jimeng-legacy-epoch", Status: model.UserStatusEnabled, Quota: 100}
			require.NoError(t, db.Create(&user).Error)
			subscription := model.UserSubscription{
				UserId: user.Id, AmountTotal: 100, AmountUsed: test.amountUsed,
				UsageEpoch: test.currentEpoch,
			}
			require.NoError(t, db.Create(&subscription).Error)
			fundingRequestID := "legacy-" + cryptoutil.BestEffortUUID()
			preConsume := model.SubscriptionPreConsumeRecord{
				RequestId: fundingRequestID, UserId: user.Id, UserSubscriptionId: subscription.Id,
				PreConsumed: 1, UsageEpoch: test.capturedEpoch,
				Status: billingsvc.SubscriptionPreConsumeStatusConsumed,
			}
			require.NoError(t, db.Create(&preConsume).Error)
			privateData := jimengTaskPrivateData{
				UpstreamTaskID: "legacy-provider-id", BillingSource: billingsvc.BillingSourceSubscription,
				SubscriptionID: subscription.Id, FundingUsageEpoch: test.capturedEpoch,
				FundingRequestID: fundingRequestID,
				FundingReserved:  1, SettlementPending: true,
			}
			privateJSON, err := marshalJimengTaskPrivateData(privateData)
			require.NoError(t, err)
			task := model.Task{
				TaskID: "task_legacy_epoch", UserId: user.Id, Quota: 0,
				Status: model.TaskStatusSubmitted, PrivateData: privateJSON, Data: `{"accepted":true}`,
			}
			require.NoError(t, db.Create(&task).Error)

			require.NoError(t, settlePendingJimengTask(&task, &privateData))
			require.NoError(t, db.First(&subscription, subscription.Id).Error)
			assert.Equal(t, test.expectedAmount, subscription.AmountUsed)
			require.NoError(t, db.First(&user, user.Id).Error)
			assert.Equal(t, 1, user.RequestCount,
				"epoch fencing must not lose accepted-task lifetime accounting")
			require.NoError(t, db.First(&preConsume, preConsume.Id).Error)
			assert.Equal(t, billingsvc.SubscriptionPreConsumeStatusSettled, preConsume.Status,
				"legacy recovery must terminalize the original preconsume ledger")
			assert.NotContains(t, task.PrivateData, "settlement_pending")
		})
	}
}
