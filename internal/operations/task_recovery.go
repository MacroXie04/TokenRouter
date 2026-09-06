package operations

import (
	"context"
	"errors"
	"sync"
)

var asyncTaskReconciler struct {
	sync.RWMutex
	runs     []func(context.Context) error
	promotes []func(context.Context) error
}

// RegisterAsyncTaskReconciler connects relay-owned provider pollers to the
// service-owned distributed scheduler without introducing an import cycle.
func RegisterAsyncTaskReconciler(run func(context.Context) error) {
	if run == nil {
		return
	}
	asyncTaskReconciler.Lock()
	asyncTaskReconciler.runs = append(asyncTaskReconciler.runs, run)
	asyncTaskReconciler.Unlock()
}

// RegisterAsyncTaskPromoter connects a relay-owned node-local emergency
// journal importer to the scheduler. Promotion runs on every instance before
// cluster-leased recovery because only the source node may see its journal.
func RegisterAsyncTaskPromoter(promote func(context.Context) error) {
	if promote == nil {
		return
	}
	asyncTaskReconciler.Lock()
	asyncTaskReconciler.promotes = append(asyncTaskReconciler.promotes, promote)
	asyncTaskReconciler.Unlock()
}

func runRegisteredAsyncTaskReconciler(ctx context.Context) error {
	asyncTaskReconciler.RLock()
	runs := append([]func(context.Context) error(nil), asyncTaskReconciler.runs...)
	asyncTaskReconciler.RUnlock()
	var callbackErrors []error
	for _, run := range runs {
		if err := run(ctx); err != nil {
			callbackErrors = append(callbackErrors, err)
		}
	}
	return errors.Join(callbackErrors...)
}

func runRegisteredAsyncTaskPromoter(ctx context.Context) error {
	asyncTaskReconciler.RLock()
	promotes := append([]func(context.Context) error(nil), asyncTaskReconciler.promotes...)
	asyncTaskReconciler.RUnlock()
	var callbackErrors []error
	for _, promote := range promotes {
		if err := promote(ctx); err != nil {
			callbackErrors = append(callbackErrors, err)
		}
	}
	return errors.Join(callbackErrors...)
}
