package operations

import (
	"context"
	"sync"
)

var jimengTaskReconciler struct {
	sync.RWMutex
	run     func(context.Context) error
	promote func(context.Context) error
}

// RegisterJimengTaskReconciler connects the relay-owned provider poller to the
// service-owned periodic scheduler without introducing an import cycle.
func RegisterJimengTaskReconciler(run func(context.Context) error) {
	jimengTaskReconciler.Lock()
	jimengTaskReconciler.run = run
	jimengTaskReconciler.Unlock()
}

// RegisterJimengTaskPromoter connects the relay-owned node-local emergency
// journal importer to the scheduler. Unlike database reconciliation, this
// callback must run on every node because its encrypted directory may not be
// shared with the node that wins the cluster-wide lease.
func RegisterJimengTaskPromoter(promote func(context.Context) error) {
	jimengTaskReconciler.Lock()
	jimengTaskReconciler.promote = promote
	jimengTaskReconciler.Unlock()
}

func runRegisteredJimengTaskReconciler(ctx context.Context) error {
	jimengTaskReconciler.RLock()
	run := jimengTaskReconciler.run
	jimengTaskReconciler.RUnlock()
	if run == nil {
		return nil
	}
	return run(ctx)
}

func runRegisteredJimengTaskPromoter(ctx context.Context) error {
	jimengTaskReconciler.RLock()
	promote := jimengTaskReconciler.promote
	jimengTaskReconciler.RUnlock()
	if promote == nil {
		return nil
	}
	return promote(ctx)
}
