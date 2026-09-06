package router_test

import (
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	stripepayments "github.com/tokenrouter/tokenrouter/internal/payments/stripe"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Assemble the same provider adapter used by the application explicitly.
	billingsvc.RegisterStripeCheckoutResolver(stripepayments.ResolveCheckoutSnapshot)
	// Controller tests build many independent routers against one process-local
	// counter store. Dedicated rate-limit tests override these values explicitly;
	// all other contract tests must not consume one another's request budgets.
	_ = os.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	_ = os.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	os.Exit(m.Run())
}
