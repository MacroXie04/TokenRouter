package app

import (
	"context"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	operationssvc "github.com/tokenrouter/tokenrouter/internal/operations"
	stripepayments "github.com/tokenrouter/tokenrouter/internal/payments/stripe"
	"github.com/tokenrouter/tokenrouter/internal/relay/tasks"
)

// configureBackgroundRecovery runs after runtime initialization and before any
// scheduler goroutine. All node-local promotions remain outside leased database
// reconciliation, preserving emergency journals on every application instance.
func configureBackgroundRecovery() {
	billingsvc.RegisterStripeCheckoutResolver(stripepayments.ResolveCheckoutSnapshot)
	jobs := tasks.AsyncRecoveryJobs()
	promotes := make([]func(context.Context) error, 0, len(jobs))
	reconciles := make([]func(context.Context) error, 0, len(jobs))
	for _, job := range jobs {
		promotes = append(promotes, job.Promote)
		reconciles = append(reconciles, job.Reconcile)
	}
	operationssvc.ConfigureAsyncTaskRecovery(promotes, reconciles)
	jimeng := tasks.JimengRecoveryJob()
	operationssvc.RegisterJimengTaskPromoter(jimeng.Promote)
	operationssvc.RegisterJimengTaskReconciler(jimeng.Reconcile)
}
