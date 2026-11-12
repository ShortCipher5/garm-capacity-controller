package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func nativeStageSpec() garmapi.DisabledPoolSpec {
	return garmapi.DisabledPoolSpec{ProviderName: "capacity", MaxRunners: 2, Image: "linux", Flavor: "small", Arch: "amd64", Tags: []string{"linux"}}
}

type stageFixture struct {
	delayPost             <-chan struct{}
	entered               chan<- struct{}
	finished              chan<- struct{}
	created               bool
	mu                    sync.Mutex
	posts, gets, reads    int
	postStatus, getStatus int
	badPost               bool
	beforePost, beforeGet func()
}

func stageSetup(t *testing.T, enabled ...bool) (*Manager, *Journal, *garmapi.ManagementClient, *stageFixture, string) {
	t.Helper()
	path := filepath.Join(privateJournalDir(t), "stage.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	f := &stageFixture{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer private-stage-fixture-token" {
			t.Error("wrong separate bearer")
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/v1/repositories/"+repoID {
			f.reads++
			json.NewEncoder(w).Encode(map[string]any{"id": repoID, "owner": "example", "name": "app", "pool_balancing_type": "roundrobin"})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v1/repositories/"+repoID+"/pools" {
			f.posts++
			if f.beforePost != nil {
				f.beforePost()
			}
			if f.delayPost != nil {
				if f.entered != nil {
					close(f.entered)
				}
				f.mu.Unlock()
				<-f.delayPost
				f.mu.Lock()
			}
			f.created = true
			if f.finished != nil {
				close(f.finished)
			}
			if f.postStatus != 0 {
				w.WriteHeader(f.postStatus)
				return
			}
		} else if r.Method == "GET" && r.URL.Path == "/api/v1/repositories/"+repoID+"/pools/"+poolID {
			f.gets++
			if f.beforeGet != nil {
				f.beforeGet()
			}
			if f.getStatus != 0 {
				w.WriteHeader(f.getStatus)
				return
			}
		} else {
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		pool := map[string]any{"id": poolID, "repo_id": repoID, "provider_name": "capacity", "max_runners": 2, "generation": 1, "image": "linux", "flavor": "small", "os_type": "linux", "os_arch": "amd64", "enable_shell": false, "extra_specs": map[string]any{}, "tags": []any{map[string]any{"name": "linux"}}}
		if f.badPost && r.Method == "POST" {
			pool["enabled"] = true
		}
		json.NewEncoder(w).Encode(pool)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "private-stage-fixture-token", Trust: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Providers: []string{"capacity"}, RepositoryIDs: []string{repoID}, AllowWrites: true, AllowDisabledPoolCreation: len(enabled) == 0 || enabled[0]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	m, err := New(client, j)
	if err != nil {
		t.Fatal(err)
	}
	return m, j, client, f, path
}
func stageExpected(t *testing.T, c *garmapi.ManagementClient) string {
	t.Helper()
	repo, err := c.Repository(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Fingerprint
}
func TestDisabledPoolDurableIntentUUIDBeforeReadbackAndNoActivation(t *testing.T) {
	m, j, c, f, _ := stageSetup(t)
	expected := stageExpected(t, c)
	f.beforePost = func() {
		pending, err := j.Pending(context.Background())
		if err != nil || len(pending) != 1 || pending[0].Kind != "repository" || pending[0].Target != repoID || len(pending[0].Result) != 0 {
			t.Error("POST preceded immutable scoped intent")
		}
	}
	f.beforeGet = func() {
		pending, err := j.Pending(context.Background())
		if err != nil || len(pending) != 1 {
			t.Error("missing pending intent")
			return
		}
		var result StagedPool
		if json.Unmarshal(pending[0].Result, &result) != nil || result.PoolID != poolID || result.DisabledReadback || result.RouteInstalled || result.ActivationAllowed {
			t.Error("readback preceded durable UUID or permit forged")
		}
	}
	result, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "stage disabled pool")
	if err != nil || !result.DisabledReadback || !result.RouteRequired || result.RouteInstalled || result.ActivationAllowed || result.PoolID != poolID {
		t.Fatal("misleading stage result", result, err)
	}
	status, err := m.Status(context.Background())
	if err != nil || status.WriteBlocked {
		t.Fatal("confirmed disabled creation left global barrier")
	}
	audit, err := j.Audit(context.Background(), 0, 100)
	if err != nil || len(audit) != 3 || audit[1].Kind != "disabled_pool_uuid_received" || audit[2].Kind != "confirmed" {
		t.Fatal("missing durable capture/audit", audit, err)
	}
	calls := f.posts + f.gets + f.reads
	replay, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "stage disabled pool")
	if err != nil || replay != result || f.posts+f.gets+f.reads != calls {
		t.Fatal("confirmed replay made network call or changed evidence")
	}
	changed := nativeStageSpec()
	changed.MaxRunners++
	if _, err = m.CreateDisabledPool(context.Background(), requestID, repoID, expected, changed, "stage disabled pool"); !errors.Is(err, ErrRequestConflict) || f.posts+f.gets+f.reads != calls {
		t.Fatal("altered replay accepted")
	}
}
func TestDisabledPoolUnknownPreservesKnownUUIDAndBlocksAcrossRestart(t *testing.T) {
	for _, failure := range []string{"lost-ack", "readback-failed", "malformed-ack"} {
		t.Run(failure, func(t *testing.T) {
			m, j, c, f, path := stageSetup(t)
			expected := stageExpected(t, c)
			switch failure {
			case "lost-ack":
				f.postStatus = 500
			case "readback-failed":
				f.getStatus = 500
			case "malformed-ack":
				f.badPost = true
			}
			if _, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "unknown disabled creation"); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal("ambiguous POST escaped stop barrier", err)
			}
			lineage := j.Lineage()
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenJournal(path, lineage)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			again, err := New(c, reopened)
			if err != nil {
				t.Fatal(err)
			}
			status, err := again.Status(context.Background())
			if err != nil || !status.WriteBlocked || len(status.Pending) != 1 {
				t.Fatal("restart lost uncertainty")
			}
			var result StagedPool
			if json.Unmarshal(status.Pending[0].Result, &result) != nil {
				t.Fatal("lost outcome record")
			}
			if failure != "lost-ack" && result.PoolID != poolID {
				t.Fatal("known UUID erased")
			}
			if result.DisabledReadback || result.RouteInstalled || result.ActivationAllowed {
				t.Fatal("uncertainty became activation permit")
			}
			before := f.posts
			if _, err = again.CreateDisabledPool(context.Background(), nextID, repoID, expected, nativeStageSpec(), "never repeat POST"); !errors.Is(err, ErrBlocked) || f.posts != before {
				t.Fatal("unknown creation retried")
			}
			if _, err = again.UpdateRepositoryBalancer(context.Background(), nextID, repoID, expected, "pack", "other settings blocked"); !errors.Is(err, ErrBlocked) {
				t.Fatal("creation uncertainty did not share settings barrier", err)
			}
			observations, err := again.ReconcilePending(context.Background())
			if err != nil || len(observations) != 1 || observations[0].MatchesDesired {
				t.Fatal("repository GET invented CREATE completion")
			}
			status, _ = again.Status(context.Background())
			if !status.WriteBlocked {
				t.Fatal("GET unlocked unknown POST")
			}
		})
	}
}
func TestDisabledPoolReceiptAuditFailurePreventsReadbackAndStaysPending(t *testing.T) {
	m, j, c, f, _ := stageSetup(t)
	expected := stageExpected(t, c)
	if _, err := j.db.Exec("CREATE TRIGGER fail_receipt BEFORE INSERT ON audit WHEN NEW.kind='disabled_pool_uuid_received' BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "audit receipt failure"); !errors.Is(err, ErrOutcomeUnknown) || f.posts != 1 || f.gets != 0 {
		t.Fatal("failed durable UUID allowed readback or retry")
	}
	status, err := m.Status(context.Background())
	if err != nil || !status.WriteBlocked || len(status.Pending) != 1 || status.Pending[0].State != "pending" {
		t.Fatal("audit failure escaped barrier")
	}
}

func TestDisabledPoolFoundationOffDoesNotReadOrJournal(t *testing.T) {
	m, j, _, f, _ := stageSetup(t, false)
	if _, err := m.CreateDisabledPool(context.Background(), requestID, repoID, fingerprint, nativeStageSpec(), "disabled foundation"); !errors.Is(err, garmapi.ErrManagementDisabled) {
		t.Fatal(err)
	}
	history, err := j.History(context.Background(), 0, 10)
	if err != nil || len(history) != 0 || f.posts != 0 || f.gets != 0 || f.reads != 0 {
		t.Fatal("disabled feature performed work")
	}
}

func TestDisabledPoolCanceledCallerDoesNotFenceDelayedPOST(t *testing.T) {
	m, j, c, f, _ := stageSetup(t)
	expected := stageExpected(t, c)
	release, entered, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.delayPost = release
	f.entered = entered
	f.finished = finished
	var once sync.Once
	defer once.Do(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.CreateDisabledPool(ctx, requestID, repoID, expected, nativeStageSpec(), "delayed accepted POST")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("POST did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled caller stuck")
	}
	another, err := New(c, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := another.CreateDisabledPool(context.Background(), nextID, repoID, expected, nativeStageSpec(), "must not duplicate delayed POST"); !errors.Is(err, ErrBlocked) {
		t.Fatal("unknown POST was bypassed", err)
	}
	once.Do(func() { close(release) })
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("old admitted POST did not finish")
	}
	f.mu.Lock()
	committed, posts := f.created, f.posts
	f.mu.Unlock()
	if !committed || posts != 1 {
		t.Fatal("delayed-commit fixture or no-retry invariant failed")
	}
	status, err := another.Status(context.Background())
	if err != nil || !status.WriteBlocked {
		t.Fatal("late response unlocked writer")
	}
}
func TestDisabledPoolCapturedUUIDSurvivesInspectionBackupWithoutActivation(t *testing.T) {
	m, j, c, f, _ := stageSetup(t)
	expected := stageExpected(t, c)
	f.getStatus = 500
	if _, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "capture before failed readback"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	archive := filepath.Join(privateJournalDir(t), "stage.backup")
	info, err := j.Backup(context.Background(), archive)
	if err != nil || info.Pending != 1 {
		t.Fatal("backup lost unknown CREATE", err)
	}
	restored := filepath.Join(privateJournalDir(t), "stage.inspection")
	info, err = RestoreInspection(context.Background(), archive, restored, j.Lineage())
	if err != nil || info.Pending != 1 {
		t.Fatal("inspection lost pending", err)
	}
	if active, err := OpenJournal(restored, j.Lineage()); err == nil {
		active.Close()
		t.Fatal("inspection became active mutation journal")
	}
}

type heldStageRepository struct {
	changedFingerprint string
	*garmapi.ManagementClient
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *heldStageRepository) Repository(ctx context.Context, id string) (garmapi.ManagedRepository, error) {
	r.once.Do(func() {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	result, err := r.ManagementClient.Repository(ctx, id)
	if r.changedFingerprint != "" {
		result.Fingerprint = r.changedFingerprint
	}
	return result, err
}
func TestDisabledPoolAtomicBeginReplayAcrossManagersNeverPostsTwice(t *testing.T) {
	first, j, c, f, path := stageSetup(t)
	expected := stageExpected(t, c)
	secondJournal, err := OpenJournal(path, j.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	defer secondJournal.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	held := &heldStageRepository{ManagementClient: c, entered: entered, release: release}
	second, err := New(held, secondJournal)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result StagedPool
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := second.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "same creation intent")
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second manager did not pass early replay")
	}
	a, err := first.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "same creation intent")
	if err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case b := <-done:
		if b.err != nil || a != b.result {
			t.Fatalf("atomic begin did not replay original confirmation: %+v %v", b.result, b.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held manager stuck")
	}
	f.mu.Lock()
	posts, gets := f.posts, f.gets
	f.mu.Unlock()
	if posts != 1 || gets != 1 {
		t.Fatalf("atomic replay dispatched more traffic: POSTs=%d readbacks=%d", posts, gets)
	}
	status, err := second.Status(context.Background())
	if err != nil || status.WriteBlocked {
		t.Fatal("confirmed replay changed journal state")
	}
}

func TestDisabledPoolConfirmationDuringPreflightOverridesStaleRead(t *testing.T) {
	first, j, c, f, path := stageSetup(t)
	expected := stageExpected(t, c)
	secondJournal, err := OpenJournal(path, j.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	defer secondJournal.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	held := &heldStageRepository{ManagementClient: c, entered: entered, release: release, changedFingerprint: fingerprint}
	second, err := New(held, secondJournal)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result StagedPool
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := second.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "same creation intent")
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second manager did not pass early replay")
	}
	a, err := first.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "same creation intent")
	if err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case b := <-done:
		if b.err != nil || a != b.result {
			t.Fatalf("atomic begin did not replay original confirmation: %+v %v", b.result, b.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held manager stuck")
	}
	f.mu.Lock()
	posts, gets := f.posts, f.gets
	f.mu.Unlock()
	if posts != 1 || gets != 1 {
		t.Fatalf("atomic replay dispatched more traffic: POSTs=%d readbacks=%d", posts, gets)
	}
	status, err := second.Status(context.Background())
	if err != nil || status.WriteBlocked {
		t.Fatal("confirmed replay changed journal state")
	}
}

func TestDisabledPoolConfirmedMissingReplayCannotBeMistakenForNewIntent(t *testing.T) {
	m, j, c, f, _ := stageSetup(t)
	expected := stageExpected(t, c)
	if _, err := m.CreateDisabledPool(context.Background(), requestID, repoID, expected, nativeStageSpec(), "confirmed record integrity"); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := j.db.QueryRow("SELECT request_hash FROM intents WHERE request_id=?", requestID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.Exec("UPDATE intents SET result_json=NULL WHERE request_id=?", requestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.replay(context.Background(), requestID, hash); !errors.Is(err, ErrJournal) {
		t.Fatal("missing result accepted by early replay")
	}
	if _, err := j.begin(context.Background(), Intent{RequestID: requestID}, hash, expected); !errors.Is(err, ErrJournal) {
		t.Fatal("confirmed missing result mistaken for new pending write")
	}
	if f.posts != 1 {
		t.Fatal("damaged replay caused another POST")
	}
}
