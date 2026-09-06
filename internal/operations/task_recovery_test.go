package operations

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAsyncTaskRecoveryRegistriesComposeAndDoNotStarve(t *testing.T) {
	asyncTaskReconciler.Lock()
	oldRuns := asyncTaskReconciler.runs
	oldPromotes := asyncTaskReconciler.promotes
	asyncTaskReconciler.runs = nil
	asyncTaskReconciler.promotes = nil
	asyncTaskReconciler.Unlock()
	t.Cleanup(func() {
		asyncTaskReconciler.Lock()
		asyncTaskReconciler.runs = oldRuns
		asyncTaskReconciler.promotes = oldPromotes
		asyncTaskReconciler.Unlock()
	})

	wantErr := errors.New("first callback failed")
	var runs []string
	RegisterAsyncTaskReconciler(func(context.Context) error {
		runs = append(runs, "first")
		return wantErr
	})
	RegisterAsyncTaskReconciler(func(context.Context) error {
		runs = append(runs, "second")
		return nil
	})
	RegisterAsyncTaskReconciler(nil)
	if err := runRegisteredAsyncTaskReconciler(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("joined reconciler error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(runs, []string{"first", "second"}) {
		t.Fatalf("reconciler order = %v", runs)
	}

	var promotes []string
	RegisterAsyncTaskPromoter(func(context.Context) error {
		promotes = append(promotes, "first")
		return wantErr
	})
	RegisterAsyncTaskPromoter(func(context.Context) error {
		promotes = append(promotes, "second")
		return nil
	})
	RegisterAsyncTaskPromoter(nil)
	if err := runRegisteredAsyncTaskPromoter(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("joined promoter error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(promotes, []string{"first", "second"}) {
		t.Fatalf("promoter order = %v", promotes)
	}
}
