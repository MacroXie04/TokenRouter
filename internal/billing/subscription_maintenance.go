package billing

import (
	"context"
)

// BackfillLegacySubscriptionEntitlementSnapshots copies reset cadence once
// for legacy rows while their source plan still exists. Every examined row is
// moved out of pending (backfilled or review), preventing an orphan at the
// front of the table from starving all later subscriptions.
func BackfillLegacySubscriptionEntitlementSnapshots(limit int) error {
	return BackfillLegacySubscriptionEntitlementSnapshotsContext(context.Background(), limit)
}

// ExpireDueSubscriptions drains all due users in bounded query batches. Each
// user's complete due set is reconciled under one user-row lock, so overlapping
// subscriptions cannot leave the group at an order-dependent intermediate
// baseline.
func ExpireDueSubscriptions(limit int) error {
	return ExpireDueSubscriptionsContext(context.Background(), limit)
}

// ResetDueSubscriptionQuotas resets quota for subscriptions whose reset time has
// passed and are still active.
func ResetDueSubscriptionQuotas() error {
	return ResetDueSubscriptionQuotasContext(context.Background())
}
