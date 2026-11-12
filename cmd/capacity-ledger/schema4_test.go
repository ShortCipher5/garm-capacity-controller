package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestCLIBinaryPreservesSchemaFourBindingAndDispatchProvenance(t *testing.T) {
	ctx := context.Background()
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	if output, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", output, err)
	}
	path, source := fixture(t)
	fence, err := source.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := admission.HostBinding{Host: "host", ServerKeyFingerprint: strings.Repeat("a", 64), OriginFingerprint: strings.Repeat("b", 64), ScopeFingerprint: strings.Repeat("c", 64), ServerName: "disposable-daemon", ServerVersion: "6.0.4", ObservedAt: time.Now()}
	if err := source.BindHostIdentities(ctx, fence, []admission.HostBinding{observed}); err != nil {
		t.Fatal(err)
	}
	if err := source.CompleteReconciliation(ctx, fence, inventory()); err != nil {
		t.Fatal(err)
	}
	binding, err := source.LookupHostBinding(ctx, "host")
	if err != nil || binding.Sequence <= 0 {
		t.Fatal("missing durable binding provenance")
	}
	cancelled := request("cancelled-before-dispatch")
	if _, err := source.Admit(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CancelUndispatched(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	req := request("bound-unknown")
	if _, err := source.Admit(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecuteDispatchBound(ctx, req, binding, func(context.Context, admission.DispatchPermit) (admission.DispatchReceipt, error) {
		return admission.DispatchReceipt{State: "unknown", OperationID: "12345678-1234-1234-1234-123456789abc", ResourceID: "disposable-resource"}, errors.New("disposable unknown")
	}); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	record, err := source.LookupDispatch(ctx, req.Key)
	if err != nil || record.HostBindingSequence != binding.Sequence {
		t.Fatal("dispatch not bound")
	}
	active, err := source.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := source.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(privateDir(t), "schema4-backup.db")
	restored := filepath.Join(privateDir(t), "schema4-restored.db")
	for _, args := range [][]string{{"backup", "--source", path, "--destination", backup}, {"restore", "--source", backup, "--destination", restored, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil || !strings.Contains(string(out), "admission blocked") {
			t.Fatalf("CLI %s %v", out, err)
		}
	}
	after, err := source.ReadSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("export changed live source")
	}
	for _, artifact := range []string{backup, restored} {
		copy, err := admission.OpenSQLite(artifact, admission.Config{})
		if err != nil {
			t.Fatal(err)
		}
		state, err := copy.ReadSnapshot(ctx)
		if err != nil || state.Ready || !reflect.DeepEqual(state.HostBindings, before.HostBindings) || state.CancellationCount != 1 || state.Allocated.Global != before.Allocated.Global {
			t.Fatal("artifact lost binding/history/accounting or became ready")
		}
		got, err := copy.LookupDispatch(ctx, req.Key)
		if err != nil || !reflect.DeepEqual(got, record) {
			t.Fatal("artifact lost exact binding-sequenced dispatch")
		}
		if err := copy.CompleteReconciliation(ctx, active, inventory()); !errors.Is(err, admission.ErrFence) {
			t.Fatal("source fence reopened artifact")
		}
		if _, err := copy.Admit(ctx, cancelled); !errors.Is(err, admission.ErrCancelled) {
			t.Fatal("artifact erased permanent tombstone")
		}
		fresh, err := copy.BeginReconciliation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		changed := observed
		changed.ObservedAt = time.Now()
		changed.ScopeFingerprint = strings.Repeat("d", 64)
		if err := copy.BindHostIdentities(ctx, fresh, []admission.HostBinding{changed}); !errors.Is(err, admission.ErrHostBinding) {
			t.Fatal("restore permitted project rebinding")
		}
		copy.Close()
	}
}
