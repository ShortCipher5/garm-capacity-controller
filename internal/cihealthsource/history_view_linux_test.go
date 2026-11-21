package cihealthsource

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	var calls, sourceState, missingAttemptReads atomic.Int32
	const (
		priorAvailable int32 = iota
		priorListedButMissing
		priorOmitted
	)
	const nextRun = runID + 1
	const runPrefix = "/repos/fixture/runners/actions/runs/"
	priorAttemptURL := runPrefix + strconv.FormatInt(runID, 10) + "/attempts/1"
	nextAttemptURL := runPrefix + strconv.FormatInt(nextRun, 10) + "/attempts/1"
	created := to.Add(-time.Minute)
	started := created.Add(10 * time.Second)
	completed := started.Add(5 * time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-history-fixture" {
			t.Error("unexpected method or credential in fixture")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mode := sourceState.Load()
		prior := cycleRun(repo, runID, 1, "completed", created)
		prior["run_started_at"] = started
		next := cycleRun(repo, nextRun, 1, "queued", created)
		switch r.URL.Path {
		case "/repos/fixture/runners":
			_ = json.NewEncoder(w).Encode(repoMetadata(repo))
		case "/repos/fixture/runners/actions/runs":
			status := r.URL.Query().Get("status")
			if mode == priorOmitted && status == "queued" {
				runPage(w, []map[string]any{next}, 1)
			} else if mode != priorOmitted && status == "" {
				runPage(w, []map[string]any{prior}, 1)
			} else {
				runPage(w, nil, 0)
			}
		case priorAttemptURL:
			if mode != priorAvailable {
				missingAttemptReads.Add(1)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(prior)
		case priorAttemptURL + "/jobs":
			if mode != priorAvailable {
				t.Error("failed exact attempt read must not fall back to its jobs")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			job := map[string]any{"id": jobID, "run_id": runID, "run_attempt": 1, "status": "completed",
				"conclusion": "success", "started_at": started, "completed_at": completed}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{job}, "total_count": 1})
		case nextAttemptURL:
			_ = json.NewEncoder(w).Encode(next)
		case nextAttemptURL + "/jobs":
			job := map[string]any{"id": jobID + 1, "run_id": nextRun, "run_attempt": 1, "status": "queued"}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{job}, "total_count": 1})
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
	const stored = "ci-health-history-v1.json"
	saved, err := root.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		after, err := root.ReadFile(stored)
		if err != nil || !bytes.Equal(after, saved) {
			t.Fatalf("source loss or revocation changed stored history: %v", err)
		}
	}
	// A listing can race with an unavailable exact attempt. A 404 is not
	// proof of deletion, abandonment, failure, or removal of authorization.
	sourceState.Store(priorListedButMissing)
	unavailable, err := ReadCycle(ctx, source, []string{repo.GarmID}, from, to)
	if err != ErrCycleObservation || unavailable.State() != CycleUnavailable || len(unavailable.Facts()) != 0 || missingAttemptReads.Load() != 1 {
		t.Fatalf("a listed attempt's 404 produced new evidence: %v, %v", unavailable.State(), err)
	}
	if _, ok := unavailable.HistoryProof(key); ok {
		t.Fatal("a failed exact attempt read minted a fresh history proof")
	}
	assertUnchanged()
	// That old attempt now drops out of the listing. A separate positive
	// exact attempt of the same workflow can renew view authorization, not
	// the old timing, and cannot add an unobserved or unstored row.
	sourceState.Store(priorOmitted)
	current := positive(source)
	currentKey := current.Facts()[0].Key
	if currentKey.RunID != nextRun || currentKey.WorkflowID != key.WorkflowID || currentKey.RepositoryID != key.RepositoryID ||
		missingAttemptReads.Load() != 1 {
		t.Fatal("the next observation did not establish a distinct attempt in the original scope")
	}
	if _, ok := current.HistoryProof(key); ok {
		t.Fatal("omitted historical attempt minted a fresh proof")
	}
	// The real loopback observation proves the attempt, not a supported
	// GitHub.com browser origin. The optional link must stay absent.
	if link, ok := current.ObservedWorkflowRunLink(source, currentKey); ok || link != (ObservedWorkflowRunLink{}) {
		t.Fatal("observed unsupported origin produced a browser address")
	}
	proof, ok := current.HistoryProof(currentKey)
	if !ok {
		t.Fatal("live exact attempt proof unavailable")
	}
	tooOld := current
	tooOld.facts = current.Facts()
	tooOld.facts[0].Clock.ObservedAt = current.sealedAt.UTC().Add(-historyCycleFreshness)
	if proof, ok := tooOld.HistoryProof(currentKey); ok || proof != (WorkflowHistoryProof{}) {
		t.Fatal("newer job pages extended the old exact attempt authorization")
	}
	wrong := currentKey
	wrong.WorkflowID++
	if proof, ok := current.HistoryProof(wrong); ok || proof != (WorkflowHistoryProof{}) {
		t.Fatal("unobserved workflow minted proof")
	}
	shadow := newSource()
	shadowProof, ok := positive(shadow).HistoryProof(currentKey)
	if !ok || shadow.SourceID() != source.SourceID() {
		t.Fatal("same-origin distinct instance fixture failed")
	}
	before := calls.Load()
	expected := written.Commits[0].Cursor
	view, err := history.ReadObservedWorkflowHistory(ctx, proof, expected, 2)
	if err != nil || len(view.Attempts) != 1 || view.Scope != scope || view.Cursor != expected || view.Attempts[0].Key != key ||
		!view.Attempts[0].AsOf.Equal(old.Facts()[0].AsOf) ||
		!view.AuthorizationObservedAt.Equal(current.Facts()[0].Clock.ObservedAt) || !view.AuthorizationObservedAt.After(view.Attempts[0].AsOf) ||
		!view.AuthorizationExpiresAt.Equal(view.AuthorizationObservedAt.Add(historyCycleFreshness)) {
		t.Fatalf("historical timestamp rebased or live scope lost: %+v, %v", view, err)
	}
	timing, err := cihealth.Measure(view.Attempts[0])
	if err != nil || timing.Conclusion != cihealth.ConclusionSuccess || len(timing.Jobs) != 1 ||
		timing.Jobs[0].Key.JobID != jobID || timing.Jobs[0].Conclusion != cihealth.ConclusionSuccess ||
		timing.Jobs[0].Execution != (cihealth.Span{Basis: cihealth.ObservedEndpoints, Duration: 5 * time.Second}) {
		t.Fatalf("prior positive timing was lost or reinterpreted after absence: %+v, %v", timing, err)
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
	if _, ok := current.HistoryProof(currentKey); ok {
		t.Fatal("closed reader minted proof")
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	if calls.Load() != before {
		t.Fatal("viewing local history made an upstream API request")
	}
}
