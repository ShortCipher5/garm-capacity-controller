package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const poolID = "00000000-0000-4000-8000-000000000001"
const repoID = "00000000-0000-4000-8000-000000000002"
const requestID = "00000000-0000-4000-8000-000000000003"
const nextID = "00000000-0000-4000-8000-000000000004"

var fingerprint = strings.Repeat("a", 64)

type fakeRemote struct {
	mu            sync.Mutex
	pool          garmapi.ManagedPool
	repo          garmapi.ManagedRepository
	reads, writes int
	fail          error
	apply         bool
	hook          func()
}

func (f *fakeRemote) ManagementIdentity() string { return strings.Repeat("c", 64) }
func remoteFixture() *fakeRemote {
	return &fakeRemote{pool: garmapi.ManagedPool{ID: poolID, RepositoryID: repoID, ProviderName: "capacity", Tags: []string{"linux"}, MaxRunners: 4, MinIdleRunners: 1, Enabled: true, Generation: 7, Fingerprint: fingerprint}, repo: garmapi.ManagedRepository{ID: repoID, Owner: "example", Name: "app", Balancer: "roundrobin", Fingerprint: fingerprint}, apply: true}
}
func (f *fakeRemote) Pool(context.Context, string) (garmapi.ManagedPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.pool, nil
}
func (f *fakeRemote) Repository(context.Context, string) (garmapi.ManagedRepository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.repo, nil
}
func (f *fakeRemote) Snapshot(context.Context) (garmapi.ManagementSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return garmapi.ManagementSnapshot{WritesEnabled: true, Pools: []garmapi.ManagedPool{f.pool}, Repositories: []garmapi.ManagedRepository{f.repo}}, nil
}
func (f *fakeRemote) UpdatePool(_ context.Context, _ string, expected string, update garmapi.PoolUpdate) (garmapi.ManagedPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if expected != f.pool.Fingerprint {
		return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
	}
	if f.hook != nil {
		f.hook()
	}
	if f.apply {
		if update.Priority != nil {
			f.pool.Priority = *update.Priority
		}
		if update.Tags != nil {
			f.pool.Tags = *update.Tags
		}
		if update.Enabled != nil {
			f.pool.Enabled = *update.Enabled
		}
		if update.MaxRunners != nil {
			f.pool.MaxRunners = *update.MaxRunners
		}
		if update.MinIdleRunners != nil {
			f.pool.MinIdleRunners = *update.MinIdleRunners
		}
		f.pool.Fingerprint = strings.Repeat("b", 64)
	}
	return f.pool, f.fail
}
func (f *fakeRemote) UpdateRepositoryBalancer(_ context.Context, _, expected, value string) (garmapi.ManagedRepository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if expected != f.repo.Fingerprint {
		return garmapi.ManagedRepository{}, garmapi.ErrManagementConflict
	}
	if f.hook != nil {
		f.hook()
	}
	if f.apply {
		f.repo.Balancer = value
		f.repo.Fingerprint = strings.Repeat("b", 64)
	}
	return f.repo, f.fail
}
func setup(t *testing.T) (*Manager, *Journal, *fakeRemote, string) {
	t.Helper()
	path := filepath.Join(privateJournalDir(t), "journal.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	f := remoteFixture()
	m, err := New(f, j)
	if err != nil {
		t.Fatal(err)
	}
	return m, j, f, path
}
func ptr[T any](value T) *T { return &value }
func TestIntentAndAuditDurableBeforeRemoteWrite(t *testing.T) {
	m, j, f, _ := setup(t)
	f.hook = func() {
		pending, err := j.Pending(context.Background())
		if err != nil || len(pending) != 1 || pending[0].State != "pending" || pending[0].RequestID != requestID {
			t.Fatalf("write preceded durable intent: %+v %v", pending, err)
		}
		audit, err := j.Audit(context.Background(), 0, 100)
		if err != nil || len(audit) != 1 || audit[0].Kind != "intent_committed" {
			t.Fatal("missing atomic audit")
		}
	}
	update := garmapi.PoolUpdate{Priority: ptr(uint64(10))}
	after, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, update, "raise pool priority")
	if err != nil || after.Priority != 10 {
		t.Fatalf("write %+v: %v", after, err)
	}
	status, err := m.Status(context.Background())
	if err != nil || status.WriteBlocked || len(status.Pending) != 0 {
		t.Fatal("confirmed operation left blocked")
	}
	history, err := j.History(context.Background(), 0, 10)
	if err != nil || len(history) != 1 || history[0].State != "confirmed" {
		t.Fatalf("history %+v %v", history, err)
	}
	beforeCalls := f.reads + f.writes
	replay, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, update, "raise pool priority")
	if err != nil || replay.Fingerprint != after.Fingerprint || beforeCalls != f.reads+f.writes {
		t.Fatal("confirmed ID replay contacted remote")
	}
	if _, err = m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(11))}, "raise pool priority"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed-key accepted: %v", err)
	}
	if f.writes != 1 {
		t.Fatal("request ID duplicated write")
	}
	audit, _ := j.Audit(context.Background(), 0, 100)
	if len(audit) != 2 || audit[1].Kind != "confirmed" {
		t.Fatalf("audit %+v", audit)
	}
}
func TestUncertainWriteBlocksAcrossRestartAndObservationNeverClears(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprint(apply), func(t *testing.T) {
			m, j, f, path := setup(t)
			f.fail = garmapi.ErrManagementOutcomeUnknown
			f.apply = apply
			_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(10))}, "set priority")
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("uncertain write: %v", err)
			}
			_ = j.Close()
			reopened, err := OpenJournal(path, j.Lineage())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			m, _ = New(f, reopened)
			status, err := m.Status(context.Background())
			if err != nil || !status.WriteBlocked || len(status.Pending) != 1 || status.Pending[0].State != "unknown" {
				t.Fatalf("lost uncertainty %+v %v", status, err)
			}
			observations, err := m.ReconcilePending(context.Background())
			if err != nil || len(observations) != 1 || observations[0].MatchesDesired != apply {
				t.Fatalf("observation %+v: %v", observations, err)
			}
			status, _ = m.Status(context.Background())
			if !status.WriteBlocked {
				t.Fatal("GET incorrectly cleared pending")
			}
			f.fail = nil
			_, err = m.UpdatePool(context.Background(), nextID, poolID, f.pool.Fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(20))}, "new change")
			if !errors.Is(err, ErrBlocked) || f.writes != 1 {
				t.Fatal("unresolved operation allowed another write")
			}
			snapshot, err := m.Snapshot(context.Background())
			if err != nil || !snapshot.Status.WriteBlocked || len(snapshot.Pools) != 1 {
				t.Fatal("blocked discovery unavailable")
			}
		})
	}
}
func TestRejectedPredispatchOperationReleasesSlotButCannotReplay(t *testing.T) {
	m, j, f, _ := setup(t)
	f.fail = garmapi.ErrManagementUnsafe
	f.apply = false
	_, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{MinIdleRunners: ptr(uint64(0))}, "decrease idle")
	if !errors.Is(err, garmapi.ErrManagementUnsafe) {
		t.Fatal(err)
	}
	status, _ := m.Status(context.Background())
	if status.WriteBlocked {
		t.Fatal("known non-dispatch stayed blocked")
	}
	history, _ := j.History(context.Background(), 0, 10)
	if len(history) != 1 || history[0].State != "rejected" {
		t.Fatal("rejection not durable")
	}
	_, err = m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{MinIdleRunners: ptr(uint64(0))}, "decrease idle")
	if !errors.Is(err, ErrRequestConflict) || f.writes != 1 {
		t.Fatal("rejected key re-executed")
	}
	f.fail = nil
	f.apply = true
	if _, err = m.UpdatePool(context.Background(), nextID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(1))}, "new change"); err != nil {
		t.Fatal(err)
	}
}
func TestRepositoryJournalAndRequestValidation(t *testing.T) {
	m, j, f, _ := setup(t)
	after, err := m.UpdateRepositoryBalancer(context.Background(), requestID, repoID, fingerprint, "pack", "pack new requests")
	if err != nil || after.Balancer != "pack" {
		t.Fatal(err)
	}
	if _, err = m.UpdateRepositoryBalancer(context.Background(), requestID, repoID, fingerprint, "pack", "pack new requests"); err != nil || f.writes != 1 {
		t.Fatal("repository replay repeated")
	}
	for _, input := range []struct{ id, target, expected, reason string }{{"bad", poolID, fingerprint, "reason"}, {nextID, "bad", fingerprint, "reason"}, {nextID, poolID, "bad", "reason"}, {nextID, poolID, fingerprint, ""}, {nextID, poolID, fingerprint, " leading"}, {nextID, poolID, fingerprint, "bad\nreason"}, {nextID, poolID, fingerprint, strings.Repeat("x", 513)}} {
		if _, err = m.UpdatePool(context.Background(), input.id, input.target, input.expected, garmapi.PoolUpdate{Priority: ptr(uint64(2))}, input.reason); !errors.Is(err, garmapi.ErrManagementUnsafe) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
	history, _ := j.History(context.Background(), 0, 100)
	if len(history) != 1 || history[0].Kind != "repository" {
		t.Fatal("invalid input persisted")
	}
}
func TestNoDispatchWithoutAtomicIntentAndAudit(t *testing.T) {
	m, j, f, _ := setup(t)
	if _, err := j.db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(2))}, "new change"); !errors.Is(err, ErrJournal) {
		t.Fatalf("failed journal accepted: %v", err)
	}
	if f.writes != 0 {
		t.Fatal("remote write before audit commit")
	}
	pending, _ := j.Pending(context.Background())
	history, _ := j.History(context.Background(), 0, 10)
	if len(pending) != 0 || len(history) != 0 {
		t.Fatal("partial intent escaped rollback")
	}
}
func TestPostWriteJournalFailureLeavesDurablePending(t *testing.T) {
	m, j, f, path := setup(t)
	f.hook = func() { _ = j.Close() }
	if _, err := m.UpdatePool(context.Background(), requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(2))}, "new change"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("lost local completion: %v", err)
	}
	reopened, err := OpenJournal(path, j.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := reopened.Pending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].State != "pending" {
		t.Fatalf("crash history missing %+v %v", pending, err)
	}
	if f.writes != 1 {
		t.Fatal("incorrect remote dispatch")
	}
}
func TestCallerCancellationCannotDiscardCompletedOutcome(t *testing.T) {
	m, j, f, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.hook = cancel
	_, err := m.UpdatePool(ctx, requestID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(2))}, "new change")
	if err != nil {
		t.Fatalf("background completion failed: %v", err)
	}
	history, _ := j.History(context.Background(), 0, 10)
	if len(history) != 1 || history[0].State != "confirmed" {
		t.Fatal("canceled caller discarded outcome")
	}
}
func TestCanceledWaitDoesNotEnterRemote(t *testing.T) {
	m, _, f, _ := setup(t)
	m.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Snapshot(ctx)
	<-m.gate
	if !errors.Is(err, ErrJournal) || f.reads != 0 {
		t.Fatal("canceled waiter reached remote")
	}
}
func TestPrivateJournalAndForeignSchemaRefusal(t *testing.T) {
	for _, kind := range []string{"public", "symlink", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(privateJournalDir(t), "db")
			if kind == "foreign" {
				db, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("CREATE TABLE foreign_data(value TEXT); PRAGMA journal_mode=DELETE")
				if err != nil {
					t.Fatal(err)
				}
				_ = db.Close()
				_ = os.Chmod(path, 0600)
			} else {
				if err := os.WriteFile(path, []byte{}, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "public" {
					_ = os.Chmod(path, 0644)
				} else {
					target := path
					path += "-link"
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := OpenJournal(path, strings.Repeat("a", 64)); !errors.Is(err, ErrJournal) {
				t.Fatal("unsafe journal opened")
			}
			if kind == "foreign" {
				db, _ := sql.Open("sqlite3", path)
				defer db.Close()
				var mode string
				_ = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
				if mode != "delete" {
					t.Fatal("foreign db mode changed")
				}
			}
		})
	}
}
func TestJournalIndependentProcessesOneIntentAndCrashPersistence(t *testing.T) {
	path := filepath.Join(privateJournalDir(t), "journal.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(remoteFixture(), j); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	var wg sync.WaitGroup
	outputs := make(chan string, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cmd := exec.Command(binary, "-test.run=^TestJournalProcessHelper$")
			cmd.Env = append(os.Environ(), "GARM_JOURNAL_HELPER="+path, "GARM_JOURNAL_LINEAGE="+j.Lineage(), fmt.Sprintf("GARM_HELPER_NUMBER=%d", n))
			data, err := cmd.CombinedOutput()
			if err != nil {
				outputs <- "ERROR:" + err.Error() + string(data)
			} else {
				outputs <- string(data)
			}
		}(n)
	}
	wg.Wait()
	close(outputs)
	won, blocked := 0, 0
	for output := range outputs {
		switch strings.TrimSpace(output) {
		case "committed":
			won++
		case "blocked":
			blocked++
		default:
			t.Fatal(output)
		}
	}
	if won != 1 || blocked != workers-1 {
		t.Fatalf("atomic intent admission: won=%d blocked=%d", won, blocked)
	}
	reopened, err := OpenJournal(path, j.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending, err := reopened.Pending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].State != "pending" {
		t.Fatalf("process exit erased intent %+v %v", pending, err)
	}
	f := remoteFixture()
	m, _ := New(f, reopened)
	_, err = m.UpdatePool(context.Background(), nextID, poolID, fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(2))}, "new change")
	if !errors.Is(err, ErrBlocked) || f.writes != 0 {
		t.Fatal("process restart replayed or bypassed pending")
	}
}
func TestJournalProcessHelper(t *testing.T) {
	path := os.Getenv("GARM_JOURNAL_HELPER")
	if path == "" {
		return
	}
	j, err := OpenJournal(path, os.Getenv("GARM_JOURNAL_LINEAGE"))
	if err != nil {
		fmt.Print("open failed")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := "10000000-0000-4000-8000-00000000000" + os.Getenv("GARM_HELPER_NUMBER")
	_, err = j.begin(ctx, Intent{RequestID: id, Kind: "pool", Target: poolID, Reason: "process fixture", Before: json.RawMessage(`{}`), Desired: json.RawMessage(`{}`)}, fingerprint, fingerprint)
	if err == nil {
		fmt.Print("committed")
	} else if errors.Is(err, ErrBlocked) {
		fmt.Print("blocked")
	} else {
		fmt.Print("unexpected error")
		os.Exit(2)
	}
	// Exit before completion. An unresolved intent is never automatically replayed.
	os.Exit(0)
}

type alternateIdentity struct{ *fakeRemote }

func (alternateIdentity) ManagementIdentity() string { return strings.Repeat("d", 64) }
func TestJournalRejectsAnotherDestination(t *testing.T) {
	_, j, _, _ := setup(t)
	if _, err := New(alternateIdentity{remoteFixture()}, j); !errors.Is(err, ErrJournal) {
		t.Fatal("journal rebound to unrelated endpoint")
	}
}
func TestActualAuthenticatedClientCommitThenErrorIsDurablyBlocked(t *testing.T) {
	var mu sync.Mutex
	writes := 0
	pool := map[string]any{"id": poolID, "repo_id": repoID, "provider_name": "capacity", "generation": 7, "max_runners": 4, "min_idle_runners": 1, "enabled": true, "tags": []any{map[string]any{"name": "linux"}}, "extra_specs": map[string]any{"private": "not-for-journal"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-private-write-token" || r.URL.Path != "/api/v1/pools/"+poolID {
			t.Error("incorrect authenticated scope")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPut {
			writes++
			var update garmapi.PoolUpdate
			if json.NewDecoder(r.Body).Decode(&update) != nil || update.Priority == nil {
				t.Error("wrong write schema")
				w.WriteHeader(400)
				return
			}
			pool["priority"] = *update.Priority
			w.WriteHeader(500)
			_, _ = w.Write([]byte("not-for-journal"))
			return
		}
		if r.Method != http.MethodGet {
			t.Error("unexpected method")
			w.WriteHeader(405)
			return
		}
		_ = json.NewEncoder(w).Encode(pool)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "fixture-private-write-token", Trust: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Providers: []string{"capacity"}, PoolIDs: []string{poolID}, AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	path := filepath.Join(privateJournalDir(t), "journal.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(client, j)
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.Pool(context.Background(), poolID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.UpdatePool(context.Background(), requestID, poolID, before.Fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(9))}, "fixture setting update")
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("ambiguous actual write %v", err)
	}
	_ = j.Close()
	j, err = OpenJournal(path, j.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	manager, err = New(client, j)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := manager.ReconcilePending(context.Background())
	if err != nil || len(observed) != 1 || !observed[0].MatchesDesired {
		t.Fatalf("authoritative observation %+v %v", observed, err)
	}
	current, err := client.Pool(context.Background(), poolID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.UpdatePool(context.Background(), nextID, poolID, current.Fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(10))}, "another update"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("uncertainty was cleared %v", err)
	}
	if writes != 1 {
		t.Fatalf("remote write replayed %d times", writes)
	}
	audit, err := manager.Audit(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	history, err := j.History(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{encoded(audit), encoded(history)} {
		if strings.Contains(string(data), "not-for-journal") || strings.Contains(string(data), "fixture-private-write-token") || strings.Contains(string(data), "extra_specs") {
			t.Fatal("private remote data persisted in journal")
		}
	}
}

func privateJournalDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}
