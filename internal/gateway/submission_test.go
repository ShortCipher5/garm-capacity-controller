package gateway

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestSubmissionIsDurableBeforeWaitAndCannotRedispatch(t *testing.T) {
	ctx := context.Background()
	f, store, b, path := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	entered := make(chan struct{})
	release := make(chan struct{})
	b.waitHook = func(context.Context) error { close(entered); <-release; return nil }
	finished := make(chan error, 1)
	go func() { _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); finished <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("wait not entered")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	other, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	record, err := other.LookupDispatch(ctx, key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "submitted" || record.OperationID != operation || record.ResourceID != "runner-1" {
		t.Fatalf("missing pre-wait durable correlation %+v %v", record, err)
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil {
		t.Fatal("submitted replay accepted")
	}
	if b.calls.Load() != 1 || b.waits.Load() != 1 {
		t.Fatal("submitted replay performed work")
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("submitted capacity released")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	record, err = other.LookupDispatch(ctx, record.ReservationKey)
	if err != nil || record.State != "confirmed" || record.OperationID != operation {
		t.Fatalf("confirmation %+v %v", record, err)
	}
}

func TestSubmissionAuditFailureStopsWaitAndKeepsUnknownCharged(t *testing.T) {
	ctx := context.Background()
	f, store, b, path := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	injected, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer injected.Close()
	if _, err := injected.Exec(`CREATE TRIGGER reject_submission BEFORE INSERT ON audit WHEN NEW.kind='dispatch_submitted' BEGIN SELECT RAISE(ABORT,'private-must-not-echo'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil || strings.Contains(err.Error(), "private-must-not-echo") {
		t.Fatal("failed persistence accepted/leaked")
	}
	record, err := store.LookupDispatch(ctx, key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "unknown" || record.OperationID != operation || record.ResourceID != "runner-1" {
		t.Fatalf("correlation lost %+v %v", record, err)
	}
	if b.calls.Load() != 1 || b.waits.Load() != 0 {
		t.Fatal("wait continued after submission persistence failure")
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("callback failure released capacity")
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil || b.calls.Load() != 1 {
		t.Fatal("callback failure redispatched")
	}
}

func TestLostPostResponseKeepsUnknownWithoutInventedCorrelation(t *testing.T) {
	ctx := context.Background()
	f, store, b, _ := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	b.createFail = true
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil {
		t.Fatal("lost response accepted")
	}
	record, err := store.LookupDispatch(ctx, key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "unknown" || record.OperationID != "" || record.ResourceID != "" {
		t.Fatalf("invented certainty %+v %v", record, err)
	}
	if b.calls.Load() != 1 || b.waits.Load() != 0 {
		t.Fatal("lost POST response retried/waited")
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil || b.calls.Load() != 1 {
		t.Fatal("lost response redispatched")
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("lost response released capacity")
	}
}

func TestGatewaySubmissionCrashHelper(t *testing.T) {
	if os.Getenv("GCC_GATEWAY_SUBMISSION_CHILD") != "1" {
		return
	}
	store, err := admission.OpenSQLite(os.Getenv("GCC_GATEWAY_SUBMISSION_DB"), admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &backendFixture{instances: map[string]incusapi.Instance{}, identity: testsupport.LoadIdentityClient(t, os.Getenv("GCC_GATEWAY_SUBMISSION_IDENTITY"))}
	b.waitHook = func(context.Context) error {
		if err := os.WriteFile(os.Getenv("GCC_GATEWAY_SUBMISSION_MARKER"), []byte("waiting-after-durable-submission"), 0600); err != nil {
			return err
		}
		select {}
	}
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": policy, "pool-b": policy}, map[string]Backend{"host-a": b})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Create(context.Background(), bootstrap("runner-1", "pool-a"))
	t.Fatalf("crash helper unexpectedly returned: %v", err)
}

func TestProcessCrashDuringWaitPreservesSubmissionAndNoRecreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, store, b, path := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "submitted-marker")
	child := exec.CommandContext(ctx, executable, "-test.run=^TestGatewaySubmissionCrashHelper$")
	child.Env = append(os.Environ(), "GCC_GATEWAY_SUBMISSION_CHILD=1", "GCC_GATEWAY_SUBMISSION_DB="+path, "GCC_GATEWAY_SUBMISSION_MARKER="+marker, "GCC_GATEWAY_SUBMISSION_IDENTITY="+b.identityConfiguration)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	deadline := time.Now().Add(7 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("helper exited early %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("child failed to reach wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("child was not killed")
		}
	case <-ctx.Done():
		t.Fatal("child kill timed out")
	}
	record, err := store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "submitted" || record.OperationID != operation || record.ResourceID != "runner-1" {
		t.Fatalf("crash lost committed correlation %+v %v", record, err)
	}
	if _, err := f.Create(context.Background(), bootstrap("runner-1", "pool-a")); err == nil || !errors.Is(err, ErrUnavailable) || b.calls.Load() != 0 {
		t.Fatal("crashed operation redispatched")
	}
	state, err := store.ReadSnapshot(context.Background())
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("crash released capacity")
	}
}

func TestCanceledRequestStillJournalsReceivedSubmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, store, b, _ := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	b.afterCreate = cancel
	b.waitHook = func(waitCtx context.Context) error {
		if waitCtx.Err() == nil {
			return errors.New("expected request cancellation")
		}
		record, err := store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
		if err != nil || record.State != "submitted" || record.OperationID != operation {
			t.Errorf("cancel erased received receipt %+v %v", record, err)
		}
		return waitCtx.Err()
	}
	if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil {
		t.Fatal("cancellation reported confirmation")
	}
	record, err := store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "unknown" || record.OperationID != operation || b.calls.Load() != 1 || b.waits.Load() != 1 {
		t.Fatalf("lost cancelled receipt %+v %v", record, err)
	}
	state, err := store.ReadSnapshot(context.Background())
	if err != nil || state.Allocated.Global.Runners != 1 {
		t.Fatal("cancellation released capacity")
	}
}

func TestSubmissionEdgeFailuresRemainChargedWithoutWaitOrRetry(t *testing.T) {
	for _, mode := range []string{"bad-operation", "wrong-resource", "wait-panic", "both-audits-fail"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, store, b, path := facadeFixture(t)
			preflight(t, f, "runner-1", "pool-a")
			switch mode {
			case "bad-operation":
				b.createReceipt = &incusapi.Created{OperationID: "invalid-private-uuid", ResourceID: "runner-1"}
			case "wrong-resource":
				b.createReceipt = &incusapi.Created{OperationID: operation, ResourceID: "another-resource"}
			case "wait-panic":
				b.waitHook = func(context.Context) error { panic("private-wait-panic") }
			case "both-audits-fail":
				injected, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				defer injected.Close()
				if _, err := injected.Exec(`CREATE TRIGGER reject_outcomes BEFORE INSERT ON audit WHEN NEW.kind IN ('dispatch_submitted','dispatch_unknown') BEGIN SELECT RAISE(ABORT,'private-outcome-fault'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe failure accepted/leaked")
			}
			record, err := store.LookupDispatch(ctx, key("controller-a", "pool-a", "runner-1"))
			if err != nil {
				t.Fatal(err)
			}
			wantState := "unknown"
			if mode == "both-audits-fail" {
				wantState = "intent"
			}
			if record.State != wantState {
				t.Fatalf("unexpected journal %+v", record)
			}
			wantWait := int32(0)
			if mode == "wait-panic" {
				wantWait = 1
				if record.OperationID != operation || record.ResourceID != "runner-1" {
					t.Fatal("panic erased persisted correlation")
				}
			}
			if b.calls.Load() != 1 || b.waits.Load() != wantWait {
				t.Fatal("invalid submission continued waiting or retried")
			}
			if mode == "bad-operation" && record.OperationID != "" {
				t.Fatal("invalid operation identifier persisted")
			}
			state, err := store.ReadSnapshot(ctx)
			if err != nil || state.Allocated.Global.Runners != 1 {
				t.Fatal("edge failure released capacity")
			}
			if _, err := f.Create(ctx, bootstrap("runner-1", "pool-a")); err == nil || b.calls.Load() != 1 {
				t.Fatal("edge failure recreated physical resource")
			}
		})
	}
}
