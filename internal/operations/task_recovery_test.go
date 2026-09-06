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

func TestConfigureAsyncTaskRecoveryOwnsSnapshotAndReplacesRegistrations(t *testing.T) {
	asyncTaskReconciler.RLock()
	oldRuns := append([]func(context.Context) error(nil), asyncTaskReconciler.runs...)
	oldPromotes := append([]func(context.Context) error(nil), asyncTaskReconciler.promotes...)
	asyncTaskReconciler.RUnlock()
	t.Cleanup(func() { ConfigureAsyncTaskRecovery(oldPromotes, oldRuns) })
	var calls []string
	promoted := []func(context.Context) error{func(context.Context) error { calls = append(calls, "promote"); return nil }}
	reconciled := []func(context.Context) error{func(context.Context) error { calls = append(calls, "reconcile"); return nil }}
	ConfigureAsyncTaskRecovery(promoted, reconciled)
	ConfigureAsyncTaskRecovery(promoted, reconciled)
	promoted[0] = func(context.Context) error { t.Fatal("caller mutated installed promotion"); return nil }
	reconciled[0] = func(context.Context) error { t.Fatal("caller mutated installed reconciliation"); return nil }
	if err := runRegisteredAsyncTaskPromoter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runRegisteredAsyncTaskReconciler(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"promote", "reconcile"}) {
		t.Fatalf("callbacks duplicated or reordered: %v", calls)
	}
}
