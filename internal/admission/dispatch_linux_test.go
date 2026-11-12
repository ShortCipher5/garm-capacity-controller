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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testOperationID = "12345678-1234-1234-1234-123456789abc"

func confirmedReceipt() DispatchReceipt {
	return DispatchReceipt{State: "confirmed", OperationID: testOperationID, ResourceID: "project/runner-1"}
}

func reserveDispatch(t *testing.T, s *SQLiteStore, key string) Request {
	t.Helper()
	r := request(key)
	if _, err := s.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDispatchConcurrentHandlesInvokeBackendOnce(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "one")
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*SQLiteStore{s, other}
	var calls, successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%len(stores)].ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				calls.Add(1)
				return confirmedReceipt(), nil
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrDispatchClaimed) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("calls=%d successes=%d", calls.Load(), successes.Load())
	}
	if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
		t.Fatal("dispatch changed allocation count")
	}
}

func TestDispatchCrashIntentSurvivesReopenAndNeverReplays(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "crashed")
	permit, err := s.ClaimDispatch(context.Background(), r)
	if err != nil || permit == nil || permit.ID() == "" {
		t.Fatalf("claim: %v", err)
	}
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var called bool
	if _, err := reopened.ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		called = true
		return confirmedReceipt(), nil
	}); !errors.Is(err, ErrDispatchClaimed) || called {
		t.Fatalf("crashed intent replayed: called=%v err=%v", called, err)
	}
	record, err := reopened.LookupDispatch(context.Background(), r.Key)
	if err != nil || record.State != "intent" || sqliteSnapshot(t, reopened).Allocated.Global.Runners != 1 {
		t.Fatal("crashed intent lost or uncharged")
	}
}

func TestDispatchErrorPanicAndInvalidReceiptRemainCharged(t *testing.T) {
	for _, mode := range []string{"error", "panic", "invalid-receipt", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			r := reserveDispatch(t, s, "request")
			calls := 0
			execute := func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				calls++
				switch mode {
				case "error":
					return DispatchReceipt{}, errors.New("bootstrap-secret-must-not-persist")
				case "panic":
					panic("bootstrap-secret-must-not-persist")
				case "unknown":
					return DispatchReceipt{State: "unknown"}, nil
				default:
					return DispatchReceipt{State: "confirmed", OperationID: "https://example.invalid/?token=secret"}, nil
				}
			}
			_, err := s.ExecuteDispatch(context.Background(), r, execute)
			if !errors.Is(err, ErrDispatchUnknown) || strings.Contains(err.Error(), "bootstrap-secret") {
				t.Fatalf("unsafe result: %v", err)
			}
			if _, err := s.ExecuteDispatch(context.Background(), r, execute); !errors.Is(err, ErrDispatchClaimed) || calls != 1 {
				t.Fatal("uncertain operation was retried")
			}
			state := sqliteSnapshot(t, s)
			if state.Allocated.Global.Runners != 1 {
				t.Fatal("uncertainty freed capacity")
			}
			for _, event := range state.Audit {
				if strings.Contains(event.Kind+event.RequestKey, "bootstrap-secret") {
					t.Fatal("secret persisted in audit")
				}
			}
		})
	}
}

func TestDispatchAuditFailureRollsBackPermit(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "audit")
	if _, err := s.db.Exec(`CREATE TRIGGER inject_dispatch_audit BEFORE INSERT ON audit WHEN NEW.kind='dispatch_claimed' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := s.ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		called = true
		return confirmedReceipt(), nil
	}); err == nil || called {
		t.Fatal("callback ran without durable claim/audit")
	}
	if _, err := s.LookupDispatch(context.Background(), r.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claim survived rollback: %v", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER inject_dispatch_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) { return confirmedReceipt(), nil }); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRechecksReservationAndFreshness(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		r := reserveDispatch(t, s, "mismatch")
		r.Resources.CPU++
		if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrConflict) || permit != nil {
			t.Fatal("changed profile was authorized")
		}
	})
	t.Run("expired", func(t *testing.T) {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		r := reserveDispatch(t, s, "expired")
		s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
		if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrReconciliation) || permit != nil {
			t.Fatal("stale reservation receipt authorized dispatch")
		}
		s.now = time.Now
		if sqliteSnapshot(t, s).Ready {
			t.Fatal("expired dispatch did not consume readiness")
		}
	})
	t.Run("barrier", func(t *testing.T) {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		r := reserveDispatch(t, s, "barrier")
		if _, err := s.BeginReconciliation(context.Background()); err != nil {
			t.Fatal(err)
		}
		if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrReconciliation) || permit != nil {
			t.Fatal("dispatch ignored active barrier")
		}
	})
	t.Run("already observed", func(t *testing.T) {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		r := reserveDispatch(t, s, "observed")
		reconcileSQLite(t, s, []InventoryResource{resource("known-resource", r.Key, 1000, 2048)})
		if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrDispatchClaimed) || permit != nil {
			t.Fatal("observed instance received new create permit")
		}
	})
	t.Run("overcommit", func(t *testing.T) {
		s := sqliteFixtureStore(t, fixture(time.Now()), true)
		r := reserveDispatch(t, s, "overcommit")
		reconcileSQLite(t, s, []InventoryResource{resource("orphan", "", 4000, 8192)})
		if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrDenied) || permit != nil {
			t.Fatal("overcommitted fleet dispatched another create")
		}
	})
}

func TestDispatchOutcomeIdentityCannotChangeOrDowngrade(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	r := reserveDispatch(t, s, "record")
	permit, err := s.ClaimDispatch(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchOutcome(context.Background(), *permit, DispatchReceipt{State: "submitted", OperationID: testOperationID, ResourceID: "project/runner-1"}); err != nil {
		t.Fatal(err)
	}
	bad := confirmedReceipt()
	bad.ResourceID = "different-instance"
	if err := s.RecordDispatchOutcome(context.Background(), *permit, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("resource identity changed")
	}
	if err := s.RecordDispatchOutcome(context.Background(), *permit, confirmedReceipt()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchOutcome(context.Background(), *permit, DispatchReceipt{State: "unknown"}); !errors.Is(err, ErrConflict) {
		t.Fatal("confirmed result downgraded")
	}
	if err := s.RecordDispatchOutcome(context.Background(), DispatchPermit{}, confirmedReceipt()); !errors.Is(err, ErrConflict) {
		t.Fatal("zero permit accepted")
	}
}

func TestDispatchMigrationPreservesOwnedVersionOneLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
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
	config := fixture(time.Now())
	encoded, _ := json.Marshal(config)
	if _, err := db.Exec("INSERT INTO metadata(id,config,ready,epoch,nonce,began_at) VALUES(1,?,1,1,'',?)", encoded, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO reservations VALUES('existing','app','a',1000,2048,1,'unknown',?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := sqliteSnapshot(t, s)
	if len(state.Reservations) != 1 || state.Reservations["existing"].State != "unknown" || state.Allocated.Global.Runners != 1 {
		t.Fatal("migration reset existing allocation")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("migration version: %d %v", version, err)
	}
	if _, _, err := s.lookupDispatchForTest("existing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("migration invented dispatch history")
	}
}

func (s *SQLiteStore) lookupDispatchForTest(key string) (DispatchRecord, string, error) {
	var record DispatchRecord
	var permit string
	err := s.transaction(context.Background(), func(tx *sql.Tx) error { var err error; record, permit, err = dispatchRecord(tx, key); return err })
	return record, permit, err
}

func TestDispatchProcessWorker(t *testing.T) {
	path := os.Getenv("CAPACITY_DISPATCH_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.ExecuteDispatch(context.Background(), request("process"), func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		f, err := os.OpenFile(os.Getenv("CAPACITY_DISPATCH_TEST_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return DispatchReceipt{}, err
		}
		_, err = fmt.Fprintln(f, "backend called")
		f.Close()
		return confirmedReceipt(), err
	})
	if err != nil && !errors.Is(err, ErrDispatchClaimed) {
		t.Fatal(err)
	}
}

func TestDispatchSeparateProcessesInvokeBackendOnce(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	reserveDispatch(t, s, "process")
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "backend.log")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDispatchProcessWorker$")
			cmd.Env = append(os.Environ(), "CAPACITY_DISPATCH_TEST_PATH="+path, "CAPACITY_DISPATCH_TEST_LOG="+log)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("worker: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(log)
	if err != nil || strings.Count(string(data), "backend called") != 1 {
		t.Fatalf("backend invocation count: %q %v", data, err)
	}
}

func TestDispatchCallbackErrorRetainsOnlyValidatedCorrelation(t *testing.T) {
	for _, invalidResource := range []bool{false, true} {
		t.Run(fmt.Sprint(invalidResource), func(t *testing.T) {
			s := sqliteFixtureStore(t, fixture(time.Now()), true)
			r := reserveDispatch(t, s, "partial-result")
			receipt := confirmedReceipt()
			receipt.State = "submitted"
			if invalidResource {
				receipt.ResourceID = "https://example.invalid/?token=must-not-persist"
			}
			_, err := s.ExecuteDispatch(context.Background(), r, func(context.Context, DispatchPermit) (DispatchReceipt, error) {
				return receipt, errors.New("lost follow-up response with bootstrap-secret")
			})
			if !errors.Is(err, ErrDispatchUnknown) {
				t.Fatalf("unknown result expected: %v", err)
			}
			record, err := s.LookupDispatch(context.Background(), r.Key)
			if err != nil || record.State != "unknown" || record.OperationID != testOperationID {
				t.Fatalf("valid operation correlation lost: %+v %v", record, err)
			}
			if invalidResource && record.ResourceID != "" {
				t.Fatal("invalid provider field retained")
			}
			if !invalidResource && record.ResourceID != receipt.ResourceID {
				t.Fatal("valid resource correlation lost")
			}
			if sqliteSnapshot(t, s).Allocated.Global.Runners != 1 {
				t.Fatal("partial result freed capacity")
			}
			if permit, err := s.ClaimDispatch(context.Background(), r); !errors.Is(err, ErrDispatchClaimed) || permit != nil {
				t.Fatal("partial result authorized redispatch")
			}
		})
	}
}
