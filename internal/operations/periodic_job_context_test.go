package operations

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authsvc "github.com/tokenrouter/tokenrouter/internal/auth"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"testing"
	"time"
)

func TestCanceledPeriodicContextVariantsDoNotMutate(t *testing.T) {
	db := testutil.OpenPeriodicContextTestDB(t,
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
	require.NoError(t, db.Create(&model.Log{CreatedAt: wallclock.NowTimestamp() - 48*60*60}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPreConsumeRecord{
		RequestId: "periodic-context-ledger", UserId: 1, UserSubscriptionId: 1,
		PreConsumed: 1, Status: billingsvc.SubscriptionPreConsumeStatusSettled,
		CreatedAt: 1, UpdatedAt: 1,
	}).Error)
	require.NoError(t, db.Create(&model.AuditLogOutbox{
		EventID: "periodic-context-audit", Payload: `{}`,
		Status: model.AuditLogOutboxStatusPending, CreatedAt: 1, UpdatedAt: 1,
	}).Error)

	ctx := testutil.CanceledPeriodicContext()
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "auth flows", run: func() error { return authsvc.CleanupAuthFlowsContext(ctx) }},
		{name: "subscription backfill", run: func() error {
			return billingsvc.BackfillLegacySubscriptionEntitlementSnapshotsContext(ctx, 100)
		}},
		{name: "subscription expiry", run: func() error { return billingsvc.ExpireDueSubscriptionsContext(ctx, 100) }},
		{name: "subscription reset", run: func() error { return billingsvc.ResetDueSubscriptionQuotasContext(ctx) }},
		{name: "pre-consume cleanup", run: func() error {
			_, err := billingsvc.CleanupSubscriptionPreConsumeRecordsContext(ctx, 1)
			return err
		}},
		{name: "log cleanup", run: func() error { return CleanupExpiredLogsContext(ctx) }},
		{name: "system task enqueue", run: func() error {
			_, _, err := EnqueueSystemTaskContext(ctx, model.SystemTaskTypeChannelTest, nil)
			return err
		}},
		{name: "instance heartbeat", run: func() error { return RegisterSystemInstanceContext(ctx) }},
		{name: "relay reconciliation", run: func() error { return billingsvc.ReconcileRelayQuotaReservationsContext(ctx) }},
		{name: "audit delivery", run: func() error { return billingsvc.DeliverAuditLogOutboxContext(ctx) }},
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

func TestEnqueueSystemTaskContextPreservesActiveTaskDeduplication(t *testing.T) {
	testutil.OpenPeriodicContextTestDB(t, &model.SystemTask{})
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
