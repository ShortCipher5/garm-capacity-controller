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

// This authority exists ONLY in a disposable fixture. Stock Garm v0.2.1 has no
// corresponding ingress+request-drain API. It must not become a production fence
// adapter by replacing proof with an operator boolean or a successful GET.
type fixtureWriteFence struct {
	mu     sync.Mutex
	frozen bool
	active sync.WaitGroup
}

func (f *fixtureWriteFence) enter() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frozen {
		return false
	}
	f.active.Add(1)
	return true
}
func (f *fixtureWriteFence) leave() { f.active.Done() }
func (f *fixtureWriteFence) freezeAndWait(ctx context.Context) error {
	f.mu.Lock()
	f.frozen = true
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { f.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDelayedPUTNeedsCompletionFenceNotTimeoutOrMatchingRead(t *testing.T) {
	var mu sync.Mutex
	pool := map[string]any{"id": poolID, "provider_name": "capacity", "generation": 7, "max_runners": 4, "min_idle_runners": 1, "enabled": true, "tags": []any{map[string]any{"name": "linux"}}}
	started, commit, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fence := &fixtureWriteFence{}
	var release sync.Once
	defer release.Do(func() { close(commit) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pools/"+poolID || r.Header.Get("Authorization") != "Bearer disposable-delayed-write-token" {
			t.Error("unexpected request scope/auth")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPut {
			if !fence.enter() {
				w.WriteHeader(403)
				return
			}
			defer fence.leave()
			close(started)
			// Deliberately model an accepted handler that can commit after its caller
			// disconnects, as the pinned Garm Runner/SQLite cancellation test permits.
			<-commit
			mu.Lock()
			pool["priority"] = uint64(9)
			mu.Unlock()
			close(finished)
		} else if r.Method != http.MethodGet {
			t.Error("unexpected method")
			w.WriteHeader(405)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(pool)
	}))
	defer func() { release.Do(func() { close(commit) }); server.Close() }()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "disposable-delayed-write-token", Trust: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Providers: []string{"capacity"}, PoolIDs: []string{poolID}, AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	journalPath := filepath.Join(privateJournalDir(t), "journal.db")
	journal, err := InitializeJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	manager, err := New(client, journal)
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.Pool(context.Background(), poolID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, e := manager.UpdatePool(ctx, requestID, poolID, before.Fingerprint, garmapi.PoolUpdate{Priority: ptr(uint64(9))}, "delayed fixture write")
		result <- e
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request was not accepted")
	}
	cancel()
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled client failed to return")
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("cancellation wrongly treated as definite non-write: %v", err)
	}

	// A wrong/missing mount during an accepted delayed write must not silently
	// become a new journal. Expected lineage comes from independent trusted config.
	expectedLineage := journal.Lineage()
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = exerciseWrongMounts(t, journalPath, expectedLineage)
	defer journal.Close()
	manager, err = New(client, journal)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := manager.ReconcilePending(context.Background())
	if err != nil || len(observation) != 1 || observation[0].MatchesDesired {
		t.Fatalf("pre-commit observation %+v %v", observation, err)
	}
	status, err := manager.Status(context.Background())
	if err != nil || !status.WriteBlocked {
		t.Fatal("observing old settings incorrectly unblocked writes")
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- fence.freezeAndWait(context.Background()) }()
	// Wait deterministically for the ingress gate to close, not a timed guess.
	deadline := time.Now().Add(time.Second)
	for {
		fence.mu.Lock()
		frozen := fence.frozen
		fence.mu.Unlock()
		if frozen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture fence did not close")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-drainDone:
		t.Fatal("fence reported completion while a handler could still commit")
	default:
	}
	// A new caller cannot bypass this fixture's frozen ingress, even with the token.
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/pools/"+poolID, nil)
	request.Header.Set("Authorization", "Bearer disposable-delayed-write-token")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("frozen ingress admitted a second writer")
	}
	release.Do(func() { close(commit) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("fixture writer did not finish")
	}
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fixture drain did not prove completion")
	}
	observation, err = manager.ReconcilePending(context.Background())
	if err != nil || len(observation) != 1 || !observation[0].MatchesDesired {
		t.Fatalf("post-fence observation %+v %v", observation, err)
	}
	status, err = manager.Status(context.Background())
	if err != nil || !status.WriteBlocked {
		t.Fatal("test-only fence or matching GET silently unlocked production journal")
	}
	audit, err := manager.Audit(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 4 || audit[0].Kind != "intent_committed" || audit[1].Kind != "unknown" || audit[2].Kind != "observed_other" || audit[3].Kind != "observed_desired" {
		t.Fatalf("missing explicit uncertainty/observations audit %+v", audit)
	}
}
