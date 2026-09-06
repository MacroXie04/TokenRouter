package router_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Controller tests build many independent routers against one process-local
	// counter store. Dedicated rate-limit tests override these values explicitly;
	// all other contract tests must not consume one another's request budgets.
	_ = os.Setenv("GLOBAL_API_RATE_LIMIT", "1000000")
	_ = os.Setenv("CRITICAL_RATE_LIMIT", "1000000")
	os.Exit(m.Run())
}
