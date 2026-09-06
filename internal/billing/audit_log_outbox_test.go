package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func openAuditTestDB(t *testing.T, name string, models ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), name) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	return db
}

func installAuditTestDatabases(t *testing.T, primary, sink *gorm.DB) {
	t.Helper()
	t.Setenv("LOG_SQL_DSN", "")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = primary, sink
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
}

func TestCheckedAuditWriteFallsBackAndWorkerDeliversExactlyOnce(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	failedSink := openAuditTestDB(t, "failed-sink.db", &model.Log{})
	healthySink := openAuditTestDB(t, "healthy-sink.db", &model.Log{})
	failedSQL, err := failedSink.DB()
	require.NoError(t, err)
	require.NoError(t, failedSQL.Close())
	installAuditTestDatabases(t, primary, failedSink)

	require.NoError(t, RecordSystemLogChecked(42, LogTypeManage, "security.change"),
		"a durable primary outbox record handles a log-sink outage")
	var queued model.AuditLogOutbox
	require.NoError(t, primary.First(&queued).Error)
	assert.Equal(t, model.AuditLogOutboxStatusPending, queued.Status)
	assert.NotEmpty(t, queued.EventID)
	assert.Contains(t, queued.Payload, "security.change")
	serialized, err := jsonutil.Marshal(&queued)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), "security.change", "outbox payload must be hidden from JSON views")

	model.LOG_DB = healthySink
	now := wallclock.NowTimestamp()
	require.NoError(t, deliverAuditLogOutboxAt(now, 10, "worker-one"))
	require.NoError(t, primary.First(&queued, queued.ID).Error)
	assert.Equal(t, model.AuditLogOutboxStatusDelivered, queued.Status)
	assert.True(t, queued.PayloadScrubbed)
	assert.Equal(t, 1, queued.Attempts)
	assert.Equal(t, now, queued.DeliveredAt)
	assert.True(t, isAuditLogPayloadReceipt(queued.Payload))
	assert.NotContains(t, queued.Payload, "security.change",
		"successful delivery must remove the primary database's copy of log content")

	var delivered model.Log
	require.NoError(t, healthySink.First(&delivered).Error)
	require.NotNil(t, delivered.AuditEventId)
	assert.Equal(t, queued.EventID, *delivered.AuditEventId)
	assert.Equal(t, "security.change", delivered.Content)
	assert.Equal(t, queued.CreatedAt, delivered.CreatedAt,
		"outbox delivery must materialize the primary-database timestamp")

	require.NoError(t, deliverAuditLogOutboxAt(now+1, 10, "worker-two"))
	var count int64
	require.NoError(t, healthySink.Model(&model.Log{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "a delivered event is never replayed")
}

func TestAuditOutboxReconcilesAlreadyDeliveredEventID(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	sink := openAuditTestDB(t, "sink.db", &model.Log{})
	installAuditTestDatabases(t, primary, sink)

	eventID := cryptoutil.BestEffortUUID()
	entry := &model.Log{AuditEventId: &eventID, UserId: 7, Type: LogTypeTopup, Content: "trade-safe", CreatedAt: 11}
	require.NoError(t, enqueueAuditLog(entry))
	require.NoError(t, sink.Create(entry).Error)

	require.NoError(t, deliverAuditLogOutboxAt(20, 10, "worker"))
	var count int64
	require.NoError(t, sink.Model(&model.Log{}).Where("audit_event_id = ?", eventID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "event-id reconciliation avoids duplicate delivery after an ambiguous commit")
	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	assert.Equal(t, model.AuditLogOutboxStatusDelivered, queued.Status)
}

func TestAuditOutboxEnqueueIsIdempotentButRejectsEventCollision(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	sink := openAuditTestDB(t, "sink.db", &model.Log{})
	installAuditTestDatabases(t, primary, sink)
	eventID := cryptoutil.BestEffortUUID()
	entry := &model.Log{AuditEventId: &eventID, Type: LogTypeSystem, Content: "first", CreatedAt: 1}
	require.NoError(t, enqueueAuditLog(entry))
	require.NoError(t, enqueueAuditLog(entry))
	var count int64
	require.NoError(t, primary.Model(&model.AuditLogOutbox{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	collision := *entry
	collision.Content = "different"
	require.Error(t, enqueueAuditLog(&collision), "one event id cannot identify two different audit records")
	require.NoError(t, primary.Model(&model.AuditLogOutbox{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	require.NoError(t, deliverAuditLogOutboxAt(queued.CreatedAt, 10, "receipt-worker"))
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	assert.True(t, isAuditLogPayloadReceipt(queued.Payload))
	require.NoError(t, enqueueAuditLog(entry), "the receipt must retain identical-replay idempotency")
	require.Error(t, enqueueAuditLog(&collision), "the receipt must retain collision detection")
}

func TestCheckedAuditWriteRequiresPrimaryClockEvenWithHealthySink(t *testing.T) {
	sink := openAuditTestDB(t, "sink.db", &model.Log{})
	installAuditTestDatabases(t, nil, sink)
	err := RecordSystemLogChecked(9, LogTypeSystem, "healthy-sink")
	require.ErrorContains(t, err, "read database clock for audit write")
	var count int64
	require.NoError(t, sink.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count, "a process-clock timestamp must never leak into the log sink")
}

func TestCheckedAuditWriteOverridesPositiveAndNegativeProcessClockSkew(t *testing.T) {
	for _, skewed := range []int64{-1, 1<<62 - 1} {
		t.Run(fmt.Sprintf("caller_timestamp_%d", skewed), func(t *testing.T) {
			db := openAuditTestDB(t, "combined.db", &model.Log{}, &model.AuditLogOutbox{})
			installAuditTestDatabases(t, db, db)
			before, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
			require.NoError(t, err)
			entry := &model.Log{UserId: 9, Type: LogTypeSystem, Content: "clock-skew", CreatedAt: skewed}
			require.NoError(t, persistAuditLogWithOutbox(entry))
			after, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
			require.NoError(t, err)

			var stored model.Log
			require.NoError(t, db.First(&stored).Error)
			assert.GreaterOrEqual(t, stored.CreatedAt, before)
			assert.LessOrEqual(t, stored.CreatedAt, after)
			assert.Equal(t, stored.CreatedAt, entry.CreatedAt)
			assert.NotEqual(t, skewed, stored.CreatedAt)
		})
	}
}

func TestAuditOutboxDeliveryUsesDurableClockWithoutRewritingReplayPayload(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	sink := openAuditTestDB(t, "sink.db", &model.Log{})
	installAuditTestDatabases(t, primary, sink)
	eventID := cryptoutil.BestEffortUUID()
	entry := &model.Log{
		AuditEventId: &eventID, UserId: 7, Type: LogTypeSystem,
		Content: "durable-clock", CreatedAt: -1,
	}
	require.NoError(t, enqueueAuditLog(entry))
	// An identical replay remains idempotent because the immutable payload is
	// compared exactly; only the local materialized sink copy is restamped.
	require.NoError(t, enqueueAuditLog(entry))

	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	var payload auditLogOutboxPayload
	require.NoError(t, jsonutil.UnmarshalJsonStr(queued.Payload, &payload))
	assert.Equal(t, int64(-1), payload.Log.CreatedAt)

	require.NoError(t, deliverAuditLogOutboxAt(queued.CreatedAt, 10, "clock-worker"))
	var delivered model.Log
	require.NoError(t, sink.Where("audit_event_id = ?", eventID).First(&delivered).Error)
	assert.Equal(t, queued.CreatedAt, delivered.CreatedAt)
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	assert.Equal(t, auditLogPayloadReceipt(string(mustMarshalAuditPayload(t, payload))), queued.Payload)
	assert.NotContains(t, queued.Payload, "durable-clock",
		"delivery must replace the immutable replay payload with a content-free receipt")
}

func mustMarshalAuditPayload(t *testing.T, payload auditLogOutboxPayload) []byte {
	t.Helper()
	encoded, err := jsonutil.Marshal(&payload)
	require.NoError(t, err)
	return encoded
}

func TestScrubDeliveredAuditLogPayloadsMigratesLegacyRowsAndPreservesPending(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	installAuditTestDatabases(t, primary, nil)
	legacyPayload := `{"event_id":"legacy","log":{"content":"legacy-private-content"}}`
	pendingPayload := `{"event_id":"pending","log":{"content":"pending-required-content"}}`
	require.NoError(t, primary.Create(&[]model.AuditLogOutbox{
		{EventID: "legacy", Payload: legacyPayload, Status: model.AuditLogOutboxStatusDelivered, CreatedAt: 1, UpdatedAt: 2, DeliveredAt: 2},
		{EventID: "already-receipt", Payload: auditLogPayloadReceipt("already"), Status: model.AuditLogOutboxStatusDelivered, CreatedAt: 1, UpdatedAt: 2, DeliveredAt: 2},
		{EventID: "pending", Payload: pendingPayload, Status: model.AuditLogOutboxStatusPending, CreatedAt: 1, UpdatedAt: 1},
	}).Error)

	require.NoError(t, ScrubDeliveredAuditLogPayloads())
	require.NoError(t, ScrubDeliveredAuditLogPayloads(), "the startup scrub must be idempotent")

	var records []model.AuditLogOutbox
	require.NoError(t, primary.Order("id asc").Find(&records).Error)
	require.Len(t, records, 3)
	assert.Equal(t, auditLogPayloadReceipt(legacyPayload), records[0].Payload)
	assert.True(t, records[0].PayloadScrubbed)
	assert.Equal(t, auditLogPayloadReceipt("already"), records[1].Payload)
	assert.True(t, records[1].PayloadScrubbed)
	assert.Equal(t, pendingPayload, records[2].Payload,
		"undelivered content must remain available for retry")
	assert.False(t, records[2].PayloadScrubbed)
}

func TestPeriodicAuditDeliveryScrubsLegacyPayloadEvenWhenDeliveryFails(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	installAuditTestDatabases(t, primary, nil)
	legacyPayload := `{"event_id":"legacy","log":{"content":"legacy-private-content"}}`
	require.NoError(t, primary.Create(&[]model.AuditLogOutbox{
		{EventID: "legacy", Payload: legacyPayload, Status: model.AuditLogOutboxStatusDelivered, CreatedAt: 1, UpdatedAt: 2, DeliveredAt: 2},
		{EventID: "malformed-pending", Payload: `{`, Status: model.AuditLogOutboxStatusPending, CreatedAt: 1, UpdatedAt: 1},
	}).Error)

	require.Error(t, DeliverAuditLogOutboxContext(context.Background()),
		"a malformed pending event must still surface as a delivery failure")
	var legacy model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", "legacy").First(&legacy).Error)
	assert.Equal(t, auditLogPayloadReceipt(legacyPayload), legacy.Payload)
	assert.True(t, legacy.PayloadScrubbed,
		"the periodic lease must scrub rows left by an older binary even when another delivery fails")
}

func TestCheckedAuditWriteReconcilesErrorReportedAfterSinkCommit(t *testing.T) {
	db := openAuditTestDB(t, "combined.db", &model.Log{}, &model.AuditLogOutbox{})
	installAuditTestDatabases(t, db, db)
	var inject atomic.Bool
	inject.Store(true)
	const callback = "test:audit_sink_ambiguous_commit"
	require.NoError(t, db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Log{}).TableName() && inject.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected error after sink commit"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })

	require.NoError(t, RecordSystemLogChecked(9, LogTypeManage, "ambiguous-commit"))
	var logs, queued int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).Count(&queued).Error)
	assert.Equal(t, int64(1), logs)
	assert.Zero(t, queued, "verification by event id resolves an error returned after commit")
}

func TestAuditOutboxLeaseContentionAndStaleWorkerFencing(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	installAuditTestDatabases(t, primary, nil)
	eventID := cryptoutil.BestEffortUUID()
	require.NoError(t, enqueueAuditLog(&model.Log{AuditEventId: &eventID, Type: LogTypeSystem, CreatedAt: 1}))
	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)

	first, claimed, err := claimAuditLogOutbox(queued.ID, "worker-one", 100)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = claimAuditLogOutbox(queued.ID, "worker-two", 100)
	require.NoError(t, err)
	assert.False(t, claimed, "an unexpired row lease excludes a second worker")

	takeoverAt := first.LeaseExpiresAt
	second, claimed, err := claimAuditLogOutbox(queued.ID, "worker-two", takeoverAt)
	require.NoError(t, err)
	require.True(t, claimed, "an expired row lease is recoverable")
	assert.Greater(t, second.LeaseVersion, first.LeaseVersion)
	assert.Equal(t, 2, second.Attempts)
	assert.ErrorIs(t, completeClaimedAuditLog(first, takeoverAt), ErrAuditLogOutboxLeaseLost,
		"the old fencing token cannot complete after takeover")
	require.NoError(t, completeClaimedAuditLog(second, takeoverAt))
}

func TestAuditOutboxRefreshesAuthoritativeClockForEachLeaseTransition(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	sink := openAuditTestDB(t, "sink.db", &model.Log{})
	installAuditTestDatabases(t, primary, sink)
	eventID := cryptoutil.BestEffortUUID()
	require.NoError(t, enqueueAuditLog(&model.Log{AuditEventId: &eventID, Type: LogTypeSystem, CreatedAt: 1}))

	times := []int64{100, 200, 201} // scan, claim, completion
	var calls atomic.Int32
	clock := func() (int64, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(times) {
			return 0, errors.New("clock called too many times")
		}
		return times[index], nil
	}
	require.NoError(t, deliverAuditLogOutboxWithClock(10, "db-clock-worker", clock))
	assert.Equal(t, int32(3), calls.Load())

	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	assert.Equal(t, model.AuditLogOutboxStatusDelivered, queued.Status)
	assert.Equal(t, int64(201), queued.DeliveredAt,
		"completion must use a fresh authoritative timestamp rather than the scan or process clock")
	assert.Equal(t, int64(201), queued.UpdatedAt)
}

func TestAuditOutboxFailurePersistsBoundedBackoffWithoutPayload(t *testing.T) {
	primary := openAuditTestDB(t, "primary.db", &model.AuditLogOutbox{})
	installAuditTestDatabases(t, primary, nil)
	eventID := cryptoutil.BestEffortUUID()
	secret := "never-copy-this-payload-to-last-error"
	require.NoError(t, enqueueAuditLog(&model.Log{
		AuditEventId: &eventID, Type: LogTypeManage, Content: secret, CreatedAt: 1,
	}))

	err := deliverAuditLogOutboxAt(100, 10, "worker")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	var queued model.AuditLogOutbox
	require.NoError(t, primary.Where("event_id = ?", eventID).First(&queued).Error)
	assert.Equal(t, model.AuditLogOutboxStatusPending, queued.Status)
	assert.Equal(t, 1, queued.Attempts)
	assert.Equal(t, int64(101), queued.NextAttemptAt)
	assert.Zero(t, queued.LeaseExpiresAt)
	assert.Empty(t, queued.LeaseToken)
	assert.Contains(t, queued.LastError, "log database is nil")
	assert.NotContains(t, queued.LastError, secret)
}

func TestAuditOutboxLeaseCoversAllBoundedSinkOperations(t *testing.T) {
	t.Setenv("AUDIT_LOG_SINK_TIMEOUT_SECONDS", "60")
	t.Setenv("AUDIT_LOG_OUTBOX_LEASE_SECONDS", "1")
	assert.Equal(t, int64(185), auditLogOutboxLeaseSeconds())
}

func TestCheckedAuditWriteFailsClosedBeforeSinkWhenPrimaryClockIsUnavailable(t *testing.T) {
	t.Setenv("LOG_SQL_DSN", "")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = nil, nil
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	err := RecordTopupLog(1, 10, 1.5, "trade")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read database clock for audit write")
	assert.Contains(t, err.Error(), "primary database is nil")
}

func TestConsumeHistogramFailureDoesNotEnqueueOrDuplicateAuditLog(t *testing.T) {
	db := openAuditTestDB(t, "combined.db", &model.Log{}, &model.AuditLogOutbox{}, &model.Option{})
	installAuditTestDatabases(t, db, db)
	previousDataExport := setting.GetOption(DataExportEnabledOption)
	require.NoError(t, setting.UpdateOption(DataExportEnabledOption, "true"))
	t.Cleanup(func() { _ = setting.UpdateOption(DataExportEnabledOption, previousDataExport) })
	ResetQuotaDataCache()
	t.Cleanup(ResetQuotaDataCache)

	err := RecordConsumeLogChecked(
		0, "user", "token", "model", 1, 1, 2, 3, false,
		0, "default", "127.0.0.1", "request", "response", 0, nil,
	)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "histogram") || errors.Is(err, ErrQuotaDataOverflow))
	var logs, queued int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logs).Error)
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).Count(&queued).Error)
	assert.Equal(t, int64(1), logs, "the audit write succeeds before the secondary histogram failure")
	assert.Zero(t, queued, "secondary cache failures must not replay an already stored audit event")
}
