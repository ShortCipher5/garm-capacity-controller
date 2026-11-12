package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancellationExactIdentityPermanentReplayAndCharge(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "cancel")
	keep := reserveDispatch(t, s, "keep")
	result, err := s.CancelUndispatched(context.Background(), r)
	if err != nil || result.Replay || result.Request != r || result.CancelledAt.IsZero() {
		t.Fatalf("cancellation: %+v %v", result, err)
	}
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 1 || state.Reservations[keep.Key].Request != keep || state.Allocated.Global.Runners != 1 || state.RecentCancellations[r.Key].Request != r || state.CancellationCount != 1 {
		t.Fatal("wrong charge removed or missing tombstone")
	}
	for i := 0; i < 3; i++ {
		replay, err := s.CancelUndispatched(context.Background(), r)
		if err != nil || !replay.Replay || replay.CancelledAt != result.CancelledAt {
			t.Fatal("cancellation retry changed result")
		}
		if _, err := s.Admit(context.Background(), r); !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled admission accepted: %v", err)
		}
		called := false
		if _, err := s.ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
			called = true
			return confirmedReceipt(), nil
		}); !errors.Is(err, ErrCancelled) || called {
			t.Fatal("cancelled key dispatched")
		}
	}
	bad := r
	bad.Resources.CPU++
	for _, call := range []func() error{
		func() error { _, err := s.Admit(context.Background(), bad); return err },
		func() error { _, err := s.ClaimDispatch(context.Background(), bad); return err },
		func() error { _, err := s.CancelUndispatched(context.Background(), bad); return err },
	} {
		if err := call(); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed identity accepted: %v", err)
		}
	}
	for _, query := range []string{"DELETE FROM cancellations", "UPDATE cancellations SET cpu=cpu+1", "INSERT INTO reservations VALUES('cancel','app','a',1000,2048,1,'creating','2026-01-01T00:00:00Z')", "UPDATE reservations SET request_key='cancel' WHERE request_key='keep'"} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("immutable identity bypass: %s", query)
		}
	}
}

func TestCancellationRejectsAllDispatchStatesAndOtherReservationStates(t *testing.T) {
	for _, status := range []string{"intent", "submitted", "confirmed", "unknown"} {
		t.Run("dispatch_"+status, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			r := reserveDispatch(t, s, "dispatch")
			permit, err := s.ClaimDispatch(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if status != "intent" {
				receipt := confirmedReceipt()
				receipt.State = status
				if err := s.RecordDispatchOutcome(context.Background(), *permit, receipt); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrDispatchClaimed) {
				t.Fatalf("dispatch released: %v", err)
			}
			if state := sqliteSnapshot(t, s); state.Allocated.Global.Runners != 1 || state.CancellationCount != 0 {
				t.Fatal("dispatch charge changed")
			}
		})
	}
	for _, status := range []string{"idle", "running", "failed", "unknown", "deleting"} {
		t.Run("reservation_"+status, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			r := reserveDispatch(t, s, "state")
			if err := s.RecordState(context.Background(), r.Key, status); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrDenied) {
				t.Fatal("noncreating reservation released")
			}
			if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
				t.Fatal("state charge lost")
			}
		})
	}
}

func TestCancellationLinkedResourceInvalidRequestAndAuditRollback(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "request")
	bad := r
	bad.Resources.CPU++
	for _, tc := range []struct {
		req  Request
		want error
	}{{Request{}, ErrDenied}, {request("missing"), ErrDenied}, {bad, ErrConflict}} {
		result, err := s.CancelUndispatched(context.Background(), tc.req)
		if !errors.Is(err, tc.want) || !reflect.DeepEqual(result, Cancellation{}) {
			t.Fatalf("bad request accepted: %+v %v", result, err)
		}
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_cancel_audit BEFORE INSERT ON audit WHEN NEW.kind='cancelled_undispatched' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(context.Background(), r); err == nil {
		t.Fatal("audit failure committed")
	}
	if state := sqliteSnapshot(t, s); state.Allocated.Global.Runners != 1 || state.CancellationCount != 0 || len(state.RecentCancellations) != 0 {
		t.Fatal("failed audit changed tombstone/charge/counter")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_cancel_audit"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CancelUndispatched(ctx, r); err == nil {
		t.Fatal("cancelled context committed")
	}
	reconcileSQLite(t, s, []InventoryResource{resource("runner", r.Key, r.Resources.CPU, r.Resources.RAM)})
	if _, err := s.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal("linked resource released")
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("linked charge lost")
	}
}

func TestCancellationReopenBackupRestoreAndHistoricalBaseline(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "durable")
	before := filepath.Join(sqliteBackupDir(t), "before.db")
	if err := s.BackupSQLite(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.Close()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	view, err := s.ReadSnapshot(context.Background())
	if err != nil || view.RecentCancellations[r.Key].Request != r {
		t.Fatalf("read view lost tombstone: %v", err)
	}
	after := filepath.Join(sqliteBackupDir(t), "after.db")
	if err := s.BackupSQLite(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path      string
		cancelled bool
	}{{before, false}, {after, true}} {
		dest := filepath.Join(sqliteBackupDir(t), "restored.db")
		if err := RestoreSQLite(context.Background(), tc.path, dest); err != nil {
			t.Fatal(err)
		}
		restored, err := OpenSQLite(dest, Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer restored.Close()
		if tc.cancelled {
			if _, err := restored.Admit(context.Background(), r); !errors.Is(err, ErrCancelled) {
				t.Fatal("included tombstone lost")
			}
			if _, err := restored.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrCancelled) {
				t.Fatal("restored cancelled key dispatched")
			}
		} else {
			if _, err := restored.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrReconciliation) {
				t.Fatal("old backup treated as history proof")
			}
			if sqliteSnapshot(t, restored).Allocated.Global.Runners != 1 {
				t.Fatal("historical charge released")
			}
			reconcileSQLite(t, restored, []InventoryResource{resource("post-backup", r.Key, r.Resources.CPU, r.Resources.RAM)})
			if _, err := restored.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrDispatchClaimed) {
				t.Fatal("postbackup resource released")
			}
		}
	}
}

func TestCancellationFreshnessDrainAndReappearance(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "fenced")
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := s.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrReconciliation) {
		t.Fatal("stale observations allowed cancellation")
	}
	s.now = time.Now
	if sqliteSnapshot(t, s).Ready {
		t.Fatal("expiry did not consume readiness")
	}
	if err := s.SetHostMode(context.Background(), r.Host, "draining"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelUndispatched(context.Background(), r); !errors.Is(err, ErrReconciliation) {
		t.Fatal("mode change treated as absence proof")
	}
	reconcileSQLite(t, s, nil)
	if _, err := s.CancelUndispatched(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := sqliteSnapshot(t, s)
	if err := s.CompleteReconciliation(context.Background(), fence, InventorySnapshot{Hosts: observations(state.Config, time.Now()), Resources: []InventoryResource{resource("reappearing", r.Key, r.Resources.CPU, r.Resources.RAM)}}); err == nil {
		t.Fatal("cancelled key relinked")
	}
	if sqliteSnapshot(t, s).Ready {
		t.Fatal("failed reappearance reopened barrier")
	}
	reconcileSQLite(t, s, []InventoryResource{resource("reappearing", "", r.Resources.CPU, r.Resources.RAM)})
	if state := sqliteSnapshot(t, s); state.Allocated.Global.Runners != 1 || state.CancellationCount != 1 || state.Config.Hosts[r.Host].Mode != "draining" {
		t.Fatal("orphan not charged, tombstone lost or host enabled")
	}
	if _, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrCancelled) {
		t.Fatal("reappearance enabled redispatch")
	}
}

func TestCancellationClaimRaceAcrossHandles(t *testing.T) {
	for i := 0; i < 25; i++ {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		other, err := OpenSQLite(s.path, Config{})
		if err != nil {
			t.Fatal(err)
		}
		r := reserveDispatch(t, s, fmt.Sprintf("race-%d", i))
		start := make(chan struct{})
		var wg sync.WaitGroup
		var cancelled, claimed atomic.Int32
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.CancelUndispatched(context.Background(), r)
			if err == nil {
				cancelled.Add(1)
			} else if !errors.Is(err, ErrDispatchClaimed) {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := other.ClaimDispatch(context.Background(), r)
			if err == nil {
				claimed.Add(1)
			} else if !errors.Is(err, ErrCancelled) {
				t.Error(err)
			}
		}()
		close(start)
		wg.Wait()
		if cancelled.Load()+claimed.Load() != 1 {
			t.Fatal("claim/cancel had other than one winner")
		}
		state := sqliteSnapshot(t, s)
		if claimed.Load() == 1 && state.Allocated.Global.Runners != 1 {
			t.Fatal("claim lost charge")
		}
		if cancelled.Load() == 1 && state.Allocated.Global.Runners != 0 {
			t.Fatal("cancel retained charge")
		}
		other.Close()
		s.Close()
	}
}

func TestCancellationProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_CANCELLATION_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if os.Getenv("CAPACITY_CANCELLATION_TEST_MODE") == "cancel" {
		_, err = s.CancelUndispatched(context.Background(), request("process-cancel"))
		if err != nil && !errors.Is(err, ErrDispatchClaimed) {
			t.Fatal(err)
		}
	} else {
		_, err = s.ClaimDispatch(context.Background(), request("process-cancel"))
		if err != nil && !errors.Is(err, ErrCancelled) && !errors.Is(err, ErrDispatchClaimed) {
			t.Fatal(err)
		}
	}
}
func TestCancellationClaimRaceAcrossProcesses(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "process-cancel")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := "claim"
			if i%2 == 0 {
				mode = "cancel"
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCancellationProcessWorker$")
			cmd.Env = append(os.Environ(), "CAPACITY_CANCELLATION_TEST_PATH="+s.path, "CAPACITY_CANCELLATION_TEST_MODE="+mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("worker %s: %v %s", mode, err, out)
			}
		}(i)
	}
	wg.Wait()
	_, cancelErr := s.LookupCancellation(context.Background(), r.Key)
	_, claimErr := s.LookupDispatch(context.Background(), r.Key)
	if (cancelErr == nil) == (claimErr == nil) {
		t.Fatal("processes did not serialize to exactly one winner")
	}
	count := sqliteSnapshot(t, s).Allocated.Global.Runners
	if cancelErr == nil && count != 0 || claimErr == nil && count != 1 {
		t.Fatal("wrong winner charge")
	}
}

func TestCancellationMigrationExcludesAllOldV1V2Reservations(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(sqliteSchema); err != nil {
				t.Fatal(err)
			}
			if version == 2 {
				if _, err := db.Exec(sqliteDispatchSchema); err != nil {
					t.Fatal(err)
				}
			}
			encoded, _ := json.Marshal(fixture(time.Now()))
			if _, err := db.Exec("INSERT INTO metadata VALUES(1,?,1,1,'','')", encoded); err != nil {
				t.Fatal(err)
			}
			legacy := request("legacy")
			if _, err := db.Exec("INSERT INTO reservations VALUES(?,?,?,?,?,?,'creating',?)", legacy.Key, legacy.Pool, legacy.Host, legacy.Resources.CPU, legacy.Resources.RAM, legacy.Resources.Runners, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			db.Close()
			s, err := OpenSQLite(path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.CancelUndispatched(context.Background(), legacy); !errors.Is(err, ErrDenied) {
				t.Fatalf("legacy history treated as proof: %v", err)
			}
			if _, err := s.db.Exec("DELETE FROM cancellation_history_exclusions"); err == nil {
				t.Fatal("legacy exclusion mutable")
			}
			if state := sqliteSnapshot(t, s); state.Allocated.Global.Runners != 1 || state.CancellationCount != 0 {
				t.Fatal("legacy charge changed")
			}
			fresh := reserveDispatch(t, s, "new-history")
			if _, err := s.CancelUndispatched(context.Background(), fresh); err != nil {
				t.Fatal(err)
			}
			if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
				t.Fatal("fresh cancellation changed legacy charge")
			}
		})
	}
}

func TestCancellationBoundedHistoryDoesNotAuthorizeMissingOldIdentity(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	for i := 0; i < 105; i++ {
		r := reserveDispatch(t, s, fmt.Sprintf("history-%03d", i))
		if _, err := s.CancelUndispatched(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, read := range []func(context.Context) (SQLiteSnapshot, error){s.Snapshot, s.ReadSnapshot} {
		view, err := read(context.Background())
		if err != nil || len(view.RecentCancellations) != 100 || view.CancellationCount != 105 {
			t.Fatal("history/count not bounded/complete")
		}
		if _, ok := view.RecentCancellations["history-000"]; ok {
			t.Fatal("oldest history retained")
		}
		if _, ok := view.RecentCancellations["history-104"]; !ok {
			t.Fatal("newest history omitted")
		}
	}
	old := request("history-000")
	if _, err := s.LookupCancellation(context.Background(), old.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(context.Background(), old); !errors.Is(err, ErrCancelled) {
		t.Fatal("bounded history authorized admission replay")
	}
	if _, err := s.ClaimDispatch(context.Background(), old); !errors.Is(err, ErrCancelled) {
		t.Fatal("bounded history authorized dispatch replay")
	}
	if retry, err := s.CancelUndispatched(context.Background(), old); err != nil || !retry.Replay {
		t.Fatal("old retry not idempotent")
	}
}
