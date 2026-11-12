package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// This runs the built operator executable, not just run() or the library. All
// state/credentials/operations are disposable local fixtures; no live endpoints.
func TestCLIBinarySchemaThreeRetainsPermanentHistoryAndRecoveryBarrier(t *testing.T) {
	ctx := context.Background()
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", out, err)
	}
	call := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil || !strings.Contains(string(out), "admission blocked") {
			t.Fatalf("CLI: %s %v", out, err)
		}
	}
	path, source := fixture(t)
	// More history than the bounded read view exposes. Old keys must still deny
	// admission and dispatch after copying and even before a new ready baseline.
	for i := 0; i < 105; i++ {
		r := request(fmt.Sprintf("cancelled-%03d", i))
		if _, err := source.Admit(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := source.CancelUndispatched(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"unknown-dispatch", "waiting", "excluded-history"} {
		if _, err := source.Admit(ctx, request(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.ExecuteDispatch(ctx, request("unknown-dispatch"), func(context.Context, admission.DispatchPermit) (admission.DispatchReceipt, error) {
		return admission.DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: "resource-preserved"}, errors.New("disposable ambiguous operation")
	}); err == nil {
		t.Fatal("unknown dispatch fixture unexpectedly confirmed")
	}
	// Model an already persisted migration exclusion. Separate v1/v2 binary tests
	// construct real historical schemas and verify their conservative migration.
	location := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO cancellation_history_exclusions(request_key) VALUES('excluded-history')"); err != nil {
		t.Fatal(err)
	}
	record, err := source.LookupDispatch(ctx, "unknown-dispatch")
	if err != nil || record.State != "unknown" || record.OperationID != "12345678-1234-1234-1234-123456789abc" || record.ResourceID != "resource-preserved" {
		t.Fatalf("dispatch fixture: %+v %v", record, err)
	}
	before, err := source.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.RecentCancellations["cancelled-000"]; ok || before.CancellationCount != 105 {
		t.Fatal("bounded history fixture incomplete")
	}
	// Export while an actual collector fence is active. Its nonce must remain on
	// source, but never authorize completing either exported artifact.
	activeFence, err := source.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err = source.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(privateDir(t), "schema3-backup.db")
	call("backup", "--source", path, "--destination", backup)
	after, err := source.ReadSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("backup changed source: %v", err)
	}
	// Taking a backup must not consume the original collector's fence.
	if err := source.CompleteReconciliation(ctx, activeFence, inventory()); err != nil {
		t.Fatalf("source fence was invalidated: %v", err)
	}
	restoredPath := filepath.Join(privateDir(t), "schema3-restored.db")
	call("restore", "--source", backup, "--destination", restoredPath, "--old-writers-fenced")
	for _, artifact := range []string{backup, restoredPath} {
		t.Run(filepath.Base(artifact), func(t *testing.T) {
			restored, err := admission.OpenSQLite(artifact, admission.Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			state, err := restored.ReadSnapshot(ctx)
			if err != nil || state.Ready || state.CancellationCount != 105 || len(state.RecentCancellations) != 100 || state.Allocated.Global != before.Allocated.Global {
				t.Fatalf("history/accounting changed: %+v %v", state, err)
			}
			got, err := restored.LookupDispatch(ctx, "unknown-dispatch")
			if err != nil || !reflect.DeepEqual(got, record) {
				t.Fatalf("dispatch history changed: %+v %v", got, err)
			}
			if err := restored.CompleteReconciliation(ctx, activeFence, inventory()); !errors.Is(err, admission.ErrFence) {
				t.Fatalf("source fence reopened artifact: %v", err)
			}
			old := request("cancelled-000")
			for _, key := range []string{old.Key, "cancelled-104"} {
				r := request(key)
				if _, err := restored.LookupCancellation(ctx, key); err != nil {
					t.Fatal(err)
				}
				if _, err := restored.Admit(ctx, r); !errors.Is(err, admission.ErrCancelled) {
					t.Fatalf("included tombstone admitted: %v", err)
				}
				if _, err := restored.ClaimDispatch(ctx, r); !errors.Is(err, admission.ErrCancelled) {
					t.Fatalf("included tombstone dispatched: %v", err)
				}
				if receipt, err := restored.CancelUndispatched(ctx, r); err != nil || !receipt.Replay {
					t.Fatalf("blocked exact cancellation retry: %+v %v", receipt, err)
				}
			}
			changed := old
			changed.Resources.CPU++
			if _, err := restored.Admit(ctx, changed); !errors.Is(err, admission.ErrConflict) {
				t.Fatalf("changed tombstone identity admitted: %v", err)
			}
			if _, err := restored.CancelUndispatched(ctx, request("waiting")); !errors.Is(err, admission.ErrReconciliation) {
				t.Fatalf("historical snapshot authorized cleanup: %v", err)
			}
			if _, err := restored.ClaimDispatch(ctx, request("waiting")); !errors.Is(err, admission.ErrReconciliation) {
				t.Fatalf("historical snapshot authorized dispatch: %v", err)
			}
			if _, err := restored.Admit(ctx, request("new")); !errors.Is(err, admission.ErrReconciliation) {
				t.Fatalf("historical snapshot authorized admission: %v", err)
			}
			staleInventory := inventory()
			freshFence, err := restored.BeginReconciliation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := restored.CompleteReconciliation(ctx, freshFence, staleInventory); !errors.Is(err, admission.ErrReconciliation) {
				t.Fatalf("pre-barrier observation accepted: %v", err)
			}
			if err := restored.CompleteReconciliation(ctx, freshFence, admission.InventorySnapshot{}); !errors.Is(err, admission.ErrReconciliation) {
				t.Fatalf("incomplete inventory accepted: %v", err)
			}
			fresh := inventory()
			fresh.Resources = []admission.InventoryResource{{ID: "post-backup-operation", Host: "host", Pool: "pool", State: "creating", Resources: request("orphan").Resources}}
			if err := restored.CompleteReconciliation(ctx, freshFence, fresh); err != nil {
				t.Fatal(err)
			}
			if _, err := restored.ClaimDispatch(ctx, request("unknown-dispatch")); !errors.Is(err, admission.ErrDispatchClaimed) {
				t.Fatalf("unknown dispatch repeated: %v", err)
			}
			if _, err := restored.CancelUndispatched(ctx, request("unknown-dispatch")); !errors.Is(err, admission.ErrDispatchClaimed) {
				t.Fatalf("unknown dispatch released: %v", err)
			}
			if _, err := restored.CancelUndispatched(ctx, request("excluded-history")); !errors.Is(err, admission.ErrDenied) {
				t.Fatalf("persisted migration exclusion lost: %v", err)
			}
			if _, err := restored.ClaimDispatch(ctx, old); !errors.Is(err, admission.ErrCancelled) {
				t.Fatalf("new baseline erased tombstone: %v", err)
			}
			final, err := restored.ReadSnapshot(ctx)
			if err != nil || final.Allocated.Global.Runners != before.Allocated.Global.Runners+1 || final.CancellationCount != 105 {
				t.Fatalf("orphan/unknown charge lost: %+v %v", final, err)
			}
		})
	}
}
