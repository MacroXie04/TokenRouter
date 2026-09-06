package billing

import (
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
)

func TestResetDueSubscriptionQuotasReturnsFailuresAndContinues(t *testing.T) {
	initSubDB(t)
	u := subUser(t, "due-reset-errors", 0, "default")
	plan := seedRawPlan(t, func(plan *model.SubscriptionPlan) {
		plan.QuotaResetPeriod = SubscriptionResetDaily
	})
	now := wallclock.NowTimestamp()
	first := seedSub(t, u.Id, plan.Id, 100, 80, func(sub *model.UserSubscription) {
		sub.StartTime = now - 3*86400
		sub.LastResetTime = now - 3*86400
		sub.NextResetTime = now - 2*86400
	})
	second := seedSub(t, u.Id, plan.Id, 100, 70, func(sub *model.UserSubscription) {
		sub.StartTime = now - 3*86400
		sub.LastResetTime = now - 3*86400
		sub.NextResetTime = now - 2*86400
	})

	var failFirst atomic.Bool
	failFirst.Store(true)
	callbackName := "test:fail_one_due_subscription_reset"
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.UserSubscription{}).TableName() && failFirst.CompareAndSwap(true, false) {
			tx.AddError(errors.New("injected subscription reset failure"))
		}
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Update().Remove(callbackName) })

	err := ResetDueSubscriptionQuotas()
	require.ErrorContains(t, err, "injected subscription reset failure")
	require.ErrorContains(t, err, fmt.Sprintf("subscription %d", first.Id))
	assert.Equal(t, int64(80), loadSub(t, first.Id).AmountUsed)
	assert.Equal(t, int64(0), loadSub(t, second.Id).AmountUsed, "later resets continue after a per-item failure")

	require.NoError(t, ResetDueSubscriptionQuotas())
	assert.Equal(t, int64(0), loadSub(t, first.Id).AmountUsed, "the failed item remains due and succeeds on retry")
}
