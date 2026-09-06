package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

func openPeriodicContextTestDB(t *testing.T, values ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "periodic-context.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(values...))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})
	return db
}

func canceledPeriodicContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestInitAbilityCacheContextCancellationPreservesPublishedSnapshot(t *testing.T) {
	db := openPeriodicContextTestDB(t, &model.Ability{})
	require.NoError(t, db.Create(&model.Ability{
		Group: "replacement", Model: "new-model", Enabled: true,
	}).Error)
	abilityMu.Lock()
	previous := abilityCache
	abilityCache = map[string][]*model.Ability{
		"sentinel:model": {{Group: "sentinel", Model: "model"}},
	}
	abilityMu.Unlock()
	t.Cleanup(func() {
		abilityMu.Lock()
		abilityCache = previous
		abilityMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	callbackName := "test:cancel_ability_rebuild_after_query"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.Ability{}).TableName() {
			cancel()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	err := InitAbilityCacheContext(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, map[string]bool{"model": true}, GetGroupModels("sentinel"))
}

func TestCanceledPeriodicContextVariantsDoNotMutate(t *testing.T) {
	db := openPeriodicContextTestDB(t,
		&model.AuthFlow{},
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{},
		&model.Log{},
		&model.SystemInstance{},
		&model.SystemTask{},
		&model.AuditLogOutbox{},
	)
	t.Setenv("LOG_RETENTION_DAYS", "1")

	consumedAt := time.Now().Add(-time.Hour)
	flow := model.AuthFlow{
		TokenHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Purpose:   "oauth", ExpiresAt: time.Now().Add(-48 * time.Hour), ConsumedAt: &consumedAt,
	}
	require.NoError(t, db.Create(&flow).Error)
	require.NoError(t, db.Create(&model.Log{CreatedAt: common.NowTimestamp() - 48*60*60}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{
		RequestId: "periodic-context-ledger", UserId: 1, UserSubscriptionId: 1,
		PreConsumed: 1, Status: SubscriptionPreConsumeStatusSettled,
		CreatedAt: 1, UpdatedAt: 1,
	}).Error)
	require.NoError(t, db.Create(&model.AuditLogOutbox{
		EventID: "periodic-context-audit", Payload: `{}`,
		Status: model.AuditLogOutboxStatusPending, CreatedAt: 1, UpdatedAt: 1,
	}).Error)

	ctx := canceledPeriodicContext()
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "auth flows", run: func() error { return CleanupAuthFlowsContext(ctx) }},
		{name: "subscription backfill", run: func() error {
			return BackfillLegacySubscriptionEntitlementSnapshotsContext(ctx, 100)
		}},
		{name: "subscription expiry", run: func() error { return ExpireDueSubscriptionsContext(ctx, 100) }},
		{name: "subscription reset", run: func() error { return ResetDueSubscriptionQuotasContext(ctx) }},
		{name: "pre-consume cleanup", run: func() error {
			_, err := CleanupSubscriptionPreConsumeRecordsContext(ctx, 1)
			return err
		}},
		{name: "log cleanup", run: func() error { return CleanupExpiredLogsContext(ctx) }},
		{name: "system task enqueue", run: func() error {
			_, _, err := EnqueueSystemTaskContext(ctx, model.SystemTaskTypeChannelTest, nil)
			return err
		}},
		{name: "instance heartbeat", run: func() error { return RegisterSystemInstanceContext(ctx) }},
		{name: "relay reconciliation", run: func() error { return ReconcileRelayQuotaReservationsContext(ctx) }},
		{name: "audit delivery", run: func() error { return DeliverAuditLogOutboxContext(ctx) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			require.ErrorIs(t, check.run(), context.Canceled)
		})
	}

	for _, table := range []any{
		&model.AuthFlow{}, &model.Log{}, &model.SubscriptionPreConsumeRecord{}, &model.AuditLogOutbox{},
	} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	}
	for _, table := range []any{&model.SystemTask{}, &model.SystemInstance{}} {
		var count int64
		require.NoError(t, db.Model(table).Count(&count).Error)
		assert.Zero(t, count)
	}
	var queued model.AuditLogOutbox
	require.NoError(t, db.First(&queued).Error)
	assert.Empty(t, queued.LeaseToken)
	assert.Equal(t, model.AuditLogOutboxStatusPending, queued.Status)
}

func TestCleanupAuthFlowsAtUsesCallerDatabaseClockBoundary(t *testing.T) {
	db := openPeriodicContextTestDB(t, &model.AuthFlow{})
	const now int64 = 2_000_000
	cutoff := time.Unix(now, 0).UTC().Add(-24 * time.Hour)
	consumedAt := time.Unix(now-1, 0).UTC()
	flows := []model.AuthFlow{
		{TokenHash: strings.Repeat("a", 64), Purpose: "oauth", ExpiresAt: cutoff.Add(-time.Second)},
		{TokenHash: strings.Repeat("b", 64), Purpose: "oauth", ExpiresAt: cutoff},
		{TokenHash: strings.Repeat("c", 64), Purpose: "oauth", ExpiresAt: time.Unix(now-1, 0).UTC()},
		{TokenHash: strings.Repeat("d", 64), Purpose: "oauth", ExpiresAt: time.Unix(now+60, 0).UTC(), ConsumedAt: &consumedAt},
	}
	require.NoError(t, db.Create(&flows).Error)
	require.NoError(t, cleanupAuthFlowsAt(context.Background(), now))

	var hashes []string
	require.NoError(t, db.Model(&model.AuthFlow{}).Order("token_hash asc").Pluck("token_hash", &hashes).Error)
	assert.Equal(t, []string{strings.Repeat("b", 64), strings.Repeat("c", 64)}, hashes,
		"only flows strictly older than the database-clock cutoff and consumed flows are removed")
}

func TestEnqueueSystemTaskContextPreservesActiveTaskDeduplication(t *testing.T) {
	openPeriodicContextTestDB(t, &model.SystemTask{})
	first, created, err := EnqueueSystemTaskContext(context.Background(), model.SystemTaskTypeChannelTest, map[string]any{"mode": "all"})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first)

	second, created, err := EnqueueSystemTaskContext(context.Background(), model.SystemTaskTypeChannelTest, nil)
	require.NoError(t, err)
	assert.False(t, created)
	require.NotNil(t, second)
	assert.Equal(t, first.TaskID, second.TaskID)
}

func TestDeliverClaimedAuditLogContextPropagatesCancellationToSink(t *testing.T) {
	db := openPeriodicContextTestDB(t, &model.Log{}, &model.AuditLogOutbox{})
	eventID := "periodic-context-sink"
	payload, err := common.Marshal(auditLogOutboxPayload{
		EventID: eventID,
		Log:     model.Log{AuditEventId: &eventID, CreatedAt: common.NowTimestamp()},
	})
	require.NoError(t, err)
	record := &model.AuditLogOutbox{
		EventID: eventID, Payload: string(payload), Status: model.AuditLogOutboxStatusPending,
	}

	err = deliverClaimedAuditLogContext(canceledPeriodicContext(), record)
	require.True(t, errors.Is(err, context.Canceled), "unexpected cancellation error: %v", err)
	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}
