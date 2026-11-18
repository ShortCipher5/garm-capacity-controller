package brokerinput

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capacitybroker"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceRuntimeCannotStartCycle(t *testing.T) {
	state, _, _ := ledgerFixture()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "ledger.db")
	id := filepath.Join(dir, "identity")
	if err := admission.InitializeSQLite(db, id, state.Config); err != nil {
		t.Fatal(err)
	}
	store, err := admission.OpenRuntimeSQLite(db, id)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, nil, capacitybroker.Options{}, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if service.Snapshot().State != "not_started" {
		t.Fatal("configured is not running")
	}
	if err = service.Run(context.Background()); !errors.Is(err, ErrRuntimeAuthority) {
		t.Fatal(err)
	}
	got := service.Snapshot()
	if got.State != "blocked" || got.InFlight || got.AttemptCount != 0 || !got.LastAttemptAt.IsZero() || !got.LastSuccessAt.IsZero() || got.LastBatchID != "" {
		t.Fatalf("%+v", got)
	}
	after, err := store.Snapshot(context.Background())
	if err != nil || len(before.Audit) != len(after.Audit) || before.Epoch != after.Epoch {
		t.Fatal("blocked loop changed ledger", err)
	}
}
func TestServiceActualActivityAndConcurrentRun(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := &Service{status: LoopStatus{State: "not_started"}, interval: time.Hour, cycle: func(ctx context.Context) (GrantCycleResult, error) {
		close(entered)
		select {
		case <-release:
			return GrantCycleResult{BatchID: "actual-batch"}, nil
		case <-ctx.Done():
			return GrantCycleResult{}, ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-entered
	got := s.Snapshot()
	if got.State != "running" || !got.InFlight || got.AttemptCount != 1 || got.SuccessCount != 0 || got.LastAttemptAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	if !errors.Is(s.Run(ctx), ErrServiceRunning) {
		t.Fatal("parallel loop accepted")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().State != "waiting" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got = s.Snapshot()
	if got.State != "waiting" || got.SuccessCount != 1 || got.LastBatchID != "actual-batch" || got.LastSuccessAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("stop")
	}
	got = s.Snapshot()
	if got.State != "stopped" || got.InFlight || got.StoppedAt.IsZero() {
		t.Fatalf("%+v", got)
	}
}
func TestServiceFailureNeverExposesResultOrRawError(t *testing.T) {
	s := &Service{status: LoopStatus{State: "not_started"}, interval: time.Hour, cycle: func(context.Context) (GrantCycleResult, error) {
		return GrantCycleResult{BatchID: "must-not-show"}, errors.New("secret")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().State != "failed" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := s.Snapshot()
	cancel()
	<-done
	if got.State != "failed" || got.LastBatchID != "" || got.SuccessCount != 0 || got.ReasonCode != "cycle_failed" || !got.LastSuccessAt.IsZero() {
		t.Fatalf("%+v", got)
	}
}
