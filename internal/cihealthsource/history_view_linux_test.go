package cihealthsource

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestObservedWorkflowHistoryRequiresFreshExactLiveReaderAndRevision(t *testing.T) {
	from, to := relativeWindow()
	repo := demandrepair.CIRepository{
		Repository: demandrepair.Repository{GarmID: "fixture", Host: "github.example", Owner: "fixture", Name: "runners"},
		GitHubID:   repoID,
	}
	var calls atomic.Int32
	const attemptURL = "/repos/fixture/runners/actions/runs/9007199254740995/attempts/1"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-history-fixture" {
			t.Error("unexpected method or credential in fixture")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		row := cycleRun(repo, runID, 1, "queued", to.Add(-time.Minute))
		switch r.URL.Path {
		case "/repos/fixture/runners":
			_ = json.NewEncoder(w).Encode(repoMetadata(repo))
		case "/repos/fixture/runners/actions/runs":
			if r.URL.Query().Get("status") == "queued" {
				runPage(w, []map[string]any{row}, 1)
			} else {
				runPage(w, nil, 0)
			}
		case attemptURL:
			_ = json.NewEncoder(w).Encode(row)
		case attemptURL + "/jobs":
			jobPage(w, runID, 1, "queued")
		default:
			t.Error("nonconfigured read")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	creds := t.TempDir()
	if err := os.Chmod(creds, 0700); err != nil {
		t.Fatal(err)
	}
	token, ca := filepath.Join(creds, "token"), filepath.Join(creds, "ca")
	if err := os.WriteFile(token, []byte("synthetic-history-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := demandobserver.Config{Enabled: true, GitHubEndpoint: server.URL, GitHubAuthority: repo.Host, GitHubTokenFile: token, GitHubCAFile: ca,
		Repositories: []demandrepair.Repository{repo.Repository}, CheckpointPath: filepath.Join(creds, "unused"), InitialFrom: from,
		IntervalSeconds: 60, MaxBackoffSeconds: 60}
	input := demandobserver.CIHealthInput{Enabled: true, Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: repo.GarmID, GitHubID: repo.GitHubID}}}
	newSource := func() *demandobserver.CIHealthSource {
		s, err := demandobserver.NewCIHealthSource(cfg, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	ctx := t.Context()
	source := newSource()
	positive := func(s *demandobserver.CIHealthSource) Cycle {
		c, err := ReadCycle(ctx, s, []string{repo.GarmID}, from, to)
		if err != nil || c.State() != CycleObserved || len(c.Facts()) != 1 {
			t.Fatalf("owned real timing capture failed: %v, %v", c.State(), err)
		}
		return c
	}
	old := positive(source)
	key := old.Facts()[0].Key
	scope := cihealth.HistoryScope{Source: key.Source, RepositoryID: key.RepositoryID, WorkflowID: key.WorkflowID}
	_, root := serviceHistoryRoot(t)
	history, initial, err := OpenOwnedCycleHistory(ctx, source, OwnedCycleHistoryInput{Enabled: true, Root: root, Initialize: true, Workflows: []cihealth.HistoryScope{scope}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	written, err := history.PersistObservedCycle(ctx, old, initial)
	if err != nil || len(written.Commits) != 1 {
		t.Fatalf("fixture did not write a historical row: %+v, %v", written, err)
	}
	current := positive(source)
	// The real loopback observation proves the attempt, not a supported
	// GitHub.com browser origin. The optional link must stay absent.
	if link, ok := current.ObservedWorkflowRunLink(source, key); ok || link != (ObservedWorkflowRunLink{}) {
		t.Fatal("observed unsupported origin produced a browser address")
	}
	proof, ok := current.HistoryProof(key)
	if !ok {
		t.Fatal("live exact attempt proof unavailable")
	}
	tooOld := current
	tooOld.facts = current.Facts()
	tooOld.facts[0].Clock.ObservedAt = current.sealedAt.UTC().Add(-historyCycleFreshness)
	if proof, ok := tooOld.HistoryProof(key); ok || proof != (WorkflowHistoryProof{}) {
		t.Fatal("newer job pages extended the old exact attempt authorization")
	}
	wrong := key
	wrong.WorkflowID++
	if proof, ok := current.HistoryProof(wrong); ok || proof != (WorkflowHistoryProof{}) {
		t.Fatal("unobserved workflow minted proof")
	}
	shadow := newSource()
	shadowProof, ok := positive(shadow).HistoryProof(key)
	if !ok || shadow.SourceID() != source.SourceID() {
		t.Fatal("same-origin distinct instance fixture failed")
	}
	before := calls.Load()
	expected := written.Commits[0].Cursor
	view, err := history.ReadObservedWorkflowHistory(ctx, proof, expected, 1)
	if err != nil || len(view.Attempts) != 1 || view.Scope != scope || view.Cursor != expected || !view.Attempts[0].AsOf.Equal(old.Facts()[0].AsOf) ||
		!view.AuthorizationObservedAt.Equal(current.Facts()[0].Clock.ObservedAt) || !view.AuthorizationObservedAt.After(view.Attempts[0].AsOf) ||
		!view.AuthorizationExpiresAt.Equal(view.AuthorizationObservedAt.Add(historyCycleFreshness)) {
		t.Fatalf("historical timestamp rebased or live scope lost: %+v, %v", view, err)
	}
	assertNoView := func(ctx context.Context, p WorkflowHistoryProof, c cihealth.HistoryCursor) {
		t.Helper()
		if got, err := history.ReadObservedWorkflowHistory(ctx, p, c, 1); err != ErrCycleHistory || got.Attempts != nil || !got.AuthorizationObservedAt.IsZero() {
			t.Fatalf("stale or unauthorized request exposed history: %+v, %v", got, err)
		}
	}
	assertNoView(ctx, WorkflowHistoryProof{}, expected)
	assertNoView(ctx, shadowProof, expected)
	assertNoView(ctx, proof, initial[0].Cursor)
	ended := proof
	ended.deadline = time.Now().Add(-time.Nanosecond)
	assertNoView(ctx, ended, expected)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	assertNoView(canceled, proof, expected)
	source.Close()
	assertNoView(ctx, proof, expected)
	if _, ok := current.HistoryProof(key); ok {
		t.Fatal("closed reader minted proof")
	}
	if calls.Load() != before {
		t.Fatal("viewing local history made an upstream API request")
	}
}
