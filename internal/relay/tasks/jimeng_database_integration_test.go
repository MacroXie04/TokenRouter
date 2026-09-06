package tasks

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"os"
	"strings"
	"testing"
	"time"
)

// TestJimengExternalDatabaseAtomicLifecycle exercises the actual MySQL or
// PostgreSQL transaction/CAS syntax selected by TOKENROUTER_TEST_SQL_DSN. The
// database must be isolated and disposable; ordinary unit runs skip this gate
// rather than presenting SQLite as server-database evidence.
func TestJimengExternalDatabaseAtomicLifecycle(t *testing.T) {
	dsn := os.Getenv("TOKENROUTER_TEST_SQL_DSN")
	if dsn == "" {
		t.Skip("set TOKENROUTER_TEST_SQL_DSN to an isolated MySQL or PostgreSQL database")
	}

	previousDB, previousLogDB := model.DB, model.LOG_DB
	t.Setenv("SQL_DSN", dsn)
	t.Setenv("SQLITE_PATH", "")
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitDB())
	externalDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := externalDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	require.Contains(t, []string{"mysql", "postgres"}, model.DB.Dialector.Name())

	before := time.Now().Unix()
	databaseNow, err := model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	after := time.Now().Unix()
	assert.GreaterOrEqual(t, databaseNow, before-5)
	assert.LessOrEqual(t, databaseNow, after+5)

	suffix := cryptoutil.BestEffortRandomAlphanumeric(12)
	user := model.User{
		Username: "jimeng-db-int-" + suffix,
		Status:   model.UserStatusEnabled,
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-jimeng-db-int-" + suffix,
		Status: billingsvc.TokenStatusEnabled, RemainQuota: 100,
	}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{
		Name: "jimeng-db-int-" + suffix, Key: "access|secret",
		Type: int(channelcatalog.ChannelTypeJimeng), Status: channelcatalog.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)

	task, privateData := newAtomicJimengTask(user, channel)
	task.TaskID = "task_jimeng_db_int_" + suffix
	task.Properties = `{"upstream_model_name":"jimeng-db","origin_model_name":"jimeng-db"}`
	privateData.EncryptedChannelKey, err = cryptoutil.EncryptByAES(channel.Key)
	require.NoError(t, err)
	reservation, err := createJimengReservedTask(&task, &token, &privateData)
	require.NoError(t, err)

	var operation model.JimengTaskOperation
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	var ledger model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.JimengTaskOperationPrepared, operation.State)
	assert.Equal(t, model.RelayQuotaReservationStatusHeld, ledger.Status)
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	assert.Equal(t, 90, user.Quota)
	assert.Equal(t, 90, token.RemainQuota)

	dispatchPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, markJimengTaskDispatching(
		&task, reservation, channel.Id, task.Properties, dispatchPrivate,
	))
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	assert.Equal(t, model.JimengTaskOperationDispatching, operation.State)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, model.RelayQuotaReservationStatusDispatched, ledger.Status)

	databaseNow, err = model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.JimengTaskOperation{}).Where("id = ?", operation.ID).
		Update("next_attempt_at", databaseNow).Error)
	claimed, ok, err := claimJimengTaskOperation(operation.ID, databaseNow)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = claimJimengTaskOperation(operation.ID, databaseNow)
	require.NoError(t, err)
	assert.False(t, ok, "the server database must fence a competing operation claim")

	privateData.UpstreamTaskID = "provider-db-int-" + suffix
	acceptedPrivate, err := marshalJimengTaskPrivateData(privateData)
	require.NoError(t, err)
	require.NoError(t, settleJimengAcceptedTask(
		&task, reservation, acceptedPrivate, nil, model.TaskStatusSubmitted, "",
		model.JimengTaskOperationDispatching, claimed.LeaseOwner,
	))
	claimed.State = model.JimengTaskOperationSubmitted
	claimed.SettlementPending = false
	// MySQL defaults to changed-row semantics. This exact same-value provider
	// update must be accepted by read-back verification even when RowsAffected
	// is zero, while the operation lease is still released atomically.
	sameValue := task
	require.NoError(t, persistJimengTaskResultWithOperation(
		&sameValue, model.TaskStatusSubmitted, claimed,
	))
	databaseNow, err = model.DatabaseUnixTimestamp(model.DB)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.JimengTaskOperation{}).Where("id = ?", operation.ID).
		Update("next_attempt_at", databaseNow).Error)
	claimed, ok, err = claimJimengTaskOperation(operation.ID, databaseNow)
	require.NoError(t, err)
	require.True(t, ok)
	desired := task
	desired.Status = model.TaskStatusSuccess
	desired.Progress = "100%"
	desired.FinishTime = databaseNow
	desired.UpdatedAt = databaseNow
	require.NoError(t, persistJimengTaskResultWithOperation(
		&desired, model.TaskStatusSubmitted, claimed,
	))

	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&operation).Error)
	require.NoError(t, model.DB.Where("reservation_id = ?", reservation.ReservationID()).First(&ledger).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 1, user.RequestCount)
	assert.Equal(t, 10, token.UsedQuota)
	assert.Equal(t, int64(10), channel.UsedQuota)
	assert.Equal(t, model.JimengTaskOperationTerminal, operation.State)
	assert.Empty(t, operation.LeaseOwner)
	assert.Equal(t, model.RelayQuotaReservationStatusSettled, ledger.Status)

	_, ok, err = claimJimengTaskOperation(operation.ID, databaseNow+jimengOperationLeaseSeconds+1)
	require.NoError(t, err)
	assert.False(t, ok, "a terminal operation cannot be reclaimed")
	require.NoError(t, ReconcileJimengTaskOperations())
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	assert.Equal(t, 10, user.UsedQuota, "terminal replay must not charge twice")
	var auditCount int64
	require.NoError(t, model.DB.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", jimengAuditEventID(reservation.ReservationID())).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)

	// The application cap is deliberately below MySQL TEXT's byte ceiling and
	// remains portable to PostgreSQL. Prove the near-limit payload reaches the
	// real server, while the next byte is rejected before any persistence path.
	nearLimit := strings.Repeat("x", jimengTaskTextMaxBytes)
	require.NoError(t, validateJimengDurableText("provider response", nearLimit, jimengTaskTextMaxBytes))
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
		UpdateColumn("data", nearLimit).Error)
	overLimit := nearLimit + "x"
	require.ErrorContains(t,
		validateJimengDurableText("provider response", overLimit, jimengTaskTextMaxBytes),
		"durable storage limit",
	)
}
