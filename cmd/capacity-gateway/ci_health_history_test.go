package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

type ciHistoryFixture struct {
	startup  configuration
	demand   demandobserver.Config
	runtime  *ciHealthRuntime
	owned    *cihealthsource.OwnedCycleHistory
	expect   []cihealthsource.HistoryExpectation
	root     *os.Root
	reads    atomic.Int32
	mode     atomic.Int32
	notFound atomic.Int32
	entered  chan struct{}
}

func newCIHistoryFixture(t *testing.T) *ciHistoryFixture {
	t.Helper()
	f := &ciHistoryFixture{entered: make(chan struct{}, 1)}
	const repoID, workflowID, baseRun int64 = 621, 622, 623
	repo := demandrepair.Repository{GarmID: "fixture", Host: "github.example", Owner: "fixture", Name: "ci"}
	created := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	identity := map[string]any{"id": repoID, "full_name": "fixture/ci"}
	run := func(id int64) map[string]any {
		workflow := workflowID
		if f.mode.Load() == 5 && id == baseRun+1 {
			workflow++
		}
		row := map[string]any{"id": id, "workflow_id": workflow, "run_attempt": 1, "repository": identity, "status": "queued", "created_at": created}
		switch f.mode.Load() {
		case 7:
			row["status"], row["conclusion"] = "completed", "failure"
		case 8:
			row["run_attempt"], row["status"], row["conclusion"] = 2, "completed", "success"
		}
		return row
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reads.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-ci-history" {
			t.Error("unexpected CI request")
			w.WriteHeader(401)
			return
		}
		const path = "/repos/fixture/ci"
		var out any
		switch {
		case r.URL.Path == path:
			out = identity
		case r.URL.Path == path+"/actions/runs":
			want := int64(1)
			switch f.mode.Load() {
			case 1:
				want = 26 // True partial capture: exceeds the attempt cap.
			case 2:
				want = 0
			case 3:
				w.WriteHeader(503)
				return
			case 4:
				f.entered <- struct{}{}
				<-r.Context().Done()
				return
			case 5:
				want = 2 // Two independently observed workflows.

			}
			rows := []any{}
			status := "queued"
			if mode := f.mode.Load(); mode == 7 || mode == 8 {
				status = "" // Completed runs are discovered by the bounded recent scan.
			}
			total := 0
			if r.URL.Query().Get("status") == status {
				first := int64(0)
				total = int(want)
				if f.mode.Load() == 9 {
					// Both pages are valid, but totals differ. Do not keep
					// the second page or persist this bounded partial cycle.
					switch r.URL.Query().Get("page") {
					case "1":
						want, total = demandrepair.CIPerPage, demandrepair.CIPerPage+2
					case "2":
						first, want, total = demandrepair.CIPerPage, demandrepair.CIPerPage+1, demandrepair.CIPerPage+1
					default:
						t.Error("unexpected discovery page")
						w.WriteHeader(404)
						return
					}
				}
				for i := first; i < want; i++ {
					rows = append(rows, run(baseRun+i))
				}
			}
			out = map[string]any{"total_count": total, "workflow_runs": rows}
		case strings.HasPrefix(r.URL.Path, path+"/actions/runs/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, path+"/actions/runs/"), "/")
			id, err := strconv.ParseInt(parts[0], 10, 64)
			attempt := "1"
			if f.mode.Load() == 8 {
				attempt = "2"
			}
			if err != nil || id < baseRun || id >= baseRun+26 || len(parts) < 3 || parts[1] != "attempts" || parts[2] != attempt {
				t.Error("unreviewed fixture scope")
				w.WriteHeader(404)
				return
			}
			if len(parts) == 3 {
				if f.mode.Load() == 6 && id == baseRun {
					f.notFound.Add(1)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				out = run(id)
			} else if len(parts) == 4 && parts[3] == "jobs" {
				out = map[string]any{"total_count": 0, "jobs": []any{}}
			}
		}
		if out == nil {
			t.Error("unsupported fixture endpoint")
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	files, dir := t.TempDir(), t.TempDir()
	for _, p := range []string{files, dir} {
		if err := os.Chmod(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	key, ca := filepath.Join(files, "token"), filepath.Join(files, "ca")
	if err := os.WriteFile(key, []byte("synthetic-ci-history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	pins := []demandobserver.CIHealthRepositoryPin{{GarmID: repo.GarmID, GitHubID: repoID}}
	cfg := configuration{EnableCIHealth: true, Admin: &adminConfiguration{}, DemandObserverConfigFile: "fixture", CIHealth: &ciHealthConfiguration{Repositories: pins}}
	dc := demandobserver.Config{Enabled: true, GitHubEndpoint: srv.URL, GitHubAuthority: repo.Host, GitHubTokenFile: key, GitHubCAFile: ca,
		Repositories: []demandrepair.Repository{repo}, CheckpointPath: "fixture", InitialFrom: created, IntervalSeconds: 60, MaxBackoffSeconds: 60}
	f.startup, f.demand = cfg, dc
	var err error
	f.runtime, err = buildCIHealth(cfg, &dc)
	if err != nil || f.runtime.activeHistory() != nil {
		t.Fatalf("default construction unexpectedly attached history: %v", err)
	}
	t.Cleanup(f.runtime.close)
	f.root, err = os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.root.Close() })
	first := cihealth.HistoryScope{Source: f.runtime.source.SourceID(), RepositoryID: repoID, WorkflowID: workflowID}
	absent := first
	absent.WorkflowID++
	f.owned, f.expect, err = cihealthsource.OpenOwnedCycleHistory(t.Context(), f.runtime.source, cihealthsource.OwnedCycleHistoryInput{Enabled: true, Root: f.root, Initialize: true, Workflows: []cihealth.HistoryScope{absent, first}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.owned.Close() })
	return f
}

func (f *ciHistoryFixture) ensureHistory(t *testing.T, revision int64) {
	t.Helper()
	h, err := cihealth.OpenHistory(t.Context(), cihealth.HistoryInput{Enabled: true, Root: f.root})
	if err != nil {
		t.Fatalf("did not release held root: %v", err)
	}
	defer h.Close()
	for i, want := range []int64{revision, 0} {
		cursor, err := h.Cursor(t.Context(), f.expect[i].Scope)
		if err != nil || cursor != (cihealth.HistoryCursor{Revision: want}) {
			t.Fatalf("workflow %d: cursor %+v, %v", i, cursor, err)
		}
	}
}

func TestCIHistoryBackgroundPersistsOnlyCleanObservedGroupsAndClosesViews(t *testing.T) {
	f := newCIHistoryFixture(t)
	ctx := t.Context()
	to := time.Now().UTC().Truncate(time.Second)
	cycle, err := cihealthsource.ReadCycle(ctx, f.runtime.source, f.runtime.repos, to.Add(-time.Hour), to)
	if err != nil || len(cycle.Facts()) != 1 {
		t.Fatalf("actual positive setup capture failed: %v", err)
	}
	proof, ok := cycle.HistoryProof(cycle.Facts()[0].Key)
	if !ok {
		t.Fatal("fixture had no valid earlier proof to revoke")
	}
	if _, err := f.owned.ReadObservedWorkflowHistory(ctx, proof, f.expect[0].Cursor, 1); err != nil {
		t.Fatalf("fixture proof should initially allow guarded reading: %v", err)
	}
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect[:1]); err != cihealthsource.ErrCycleHistory {
		t.Fatalf("incomplete cursor review accepted: %v", err)
	}
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	if _, err := f.owned.ReadObservedWorkflowHistory(ctx, proof, f.expect[0].Cursor, 1); err != cihealthsource.ErrCycleHistory {
		t.Fatalf("transfer retained a standalone history view: %v", err)
	}
	if !f.runtime.runCycle(ctx) {
		t.Fatal("clean actual capture failed")
	}
	for _, mode := range []int32{1, 2} {
		f.mode.Store(mode)
		if !f.runtime.runCycle(ctx) || f.runtime.activeHistory() == nil {
			t.Fatal("partial/absent evidence should retain timing without retry or write")
		}
	}
	f.mode.Store(3)
	if f.runtime.runCycle(ctx) || f.runtime.activeHistory() != nil {
		t.Fatal("source failure did not close optional history")
	}
	f.mode.Store(0)
	if !f.runtime.runCycle(ctx) || f.runtime.activeHistory() != nil {
		t.Fatal("timing recovery reopened optional history")
	}
	f.ensureHistory(t, 1)
}

func TestCIHistoryBackgroundDiscoveryDriftKeepsAttachmentWithoutPersistingPartial(t *testing.T) {
	f := newCIHistoryFixture(t)
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	f.mode.Store(9)
	if !f.runtime.runCycle(t.Context()) || f.runtime.activeHistory() == nil {
		t.Fatal("valid page drift failed timing or disabled history")
	}
	f.mode.Store(0)
	if !f.runtime.runCycle(t.Context()) || f.runtime.activeHistory() == nil {
		t.Fatal("clean cycle did not recover history persistence")
	}
	f.runtime.disableHistory()
	f.ensureHistory(t, 1) // Only the clean observation was persisted.
}

func TestCIHistoryBackgroundExactAttemptNotFoundLeavesRetainedRevision(t *testing.T) {
	f := newCIHistoryFixture(t)
	ctx := t.Context()
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	if !f.runtime.runCycle(ctx) || f.runtime.activeHistory() == nil {
		t.Fatal("initial observation did not persist")
	}
	f.mode.Store(6) // The run remains listed, but its exact attempt is unavailable.
	if f.runtime.runCycle(ctx) || f.runtime.activeHistory() != nil || f.notFound.Load() != 1 {
		t.Fatal("an unavailable exact attempt was accepted or kept the history attachment open")
	}
	f.ensureHistory(t, 1)
	f.mode.Store(0)
	if !f.runtime.runCycle(ctx) || f.runtime.activeHistory() != nil {
		t.Fatal("a later successful timing read resumed history without an owner review")
	}
	f.ensureHistory(t, 1)
}

func TestCIHistoryBackgroundExplicitDisablePublicationFailureAndUncertainty(t *testing.T) {
	for _, why := range []string{"disable", "publication", "uncertainty"} {
		t.Run(why, func(t *testing.T) {
			f := newCIHistoryFixture(t)
			if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
				t.Fatal(err)
			}
			const intent = "ci-health-history-v1.intent"
			switch why {
			case "disable":
				f.runtime.disableHistory()
			case "publication":
				var err error
				f.runtime.snapshot, err = adminview.NewCIHealthSnapshot([]adminview.CIHealthRepository{{Source: f.runtime.source.SourceID(), RepositoryID: 1}})
				if err != nil {
					t.Fatal(err)
				}
			case "uncertainty":
				// Synthetic local marker only: the existing custody owner must
				// fence it without deletion/reopening even when timing succeeds.
				file, err := f.root.OpenFile(intent, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil || file.Close() != nil {
					t.Fatal("fixture could not stage a local uncertainty marker", err)
				}
			}
			if got := f.runtime.runCycle(t.Context()); got != (why != "publication") || f.runtime.activeHistory() != nil {
				t.Fatalf("incorrect timing or history state after %s", why)
			}
			if why == "uncertainty" {
				if h, err := cihealth.OpenHistory(t.Context(), cihealth.HistoryInput{Enabled: true, Root: f.root}); h != nil || !errors.Is(err, cihealth.ErrHistoryUncertain) {
					t.Fatal("existing uncertainty bypassed after background close", err)
				}
			} else {
				f.ensureHistory(t, 0)
			}
		})
	}
}

func TestCIHistoryBackgroundStopCancelsActiveReadAndReleasesStore(t *testing.T) {
	f := newCIHistoryFixture(t)
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	f.mode.Store(4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { f.runtime.run(ctx); close(done) }()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("read did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background stop did not cancel the active read")
	}
	if f.runtime.activeHistory() != nil {
		t.Fatal("stopped worker retained history")
	}
	f.ensureHistory(t, 0)
}

func TestCIHistoryBackgroundDoesNotAdvanceOneObservedCycleTwice(t *testing.T) {
	f := newCIHistoryFixture(t)
	ctx := t.Context()
	if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	to := time.Now().UTC().Truncate(time.Second)
	cycle, err := cihealthsource.ReadCycle(ctx, f.runtime.source, f.runtime.repos, to.Add(-time.Hour), to)
	if err != nil {
		t.Fatal(err)
	}
	stage := f.runtime.activeHistory()
	if got, err := stage.PersistCycle(ctx, cycle); err != nil || len(got.Commits) != 1 {
		t.Fatalf("first clean observation failed: %+v, %v", got, err)
	}
	if got, err := stage.PersistCycle(ctx, cycle); err != cihealthsource.ErrCycleHistory || len(got.Commits) != 0 {
		t.Fatalf("copied observation advanced a revision again: %+v, %v", got, err)
	}
	f.ensureHistory(t, 1)
}

type ciHistoryCloseAfterCommit struct {
	context.Context
	root   *os.Root
	first  os.FileInfo
	source *demandobserver.CIHealthSource
	closed bool
}

// Tick at an actual persistence fence, once the original snapshot inode has
// been replaced by a durable workflow Apply. The caller context remains live;
// only the concrete CI reader is stopped.
func (c *ciHistoryCloseAfterCommit) Err() error {
	if !c.closed {
		now, err := c.root.Stat("ci-health-history-v1.json")
		if err == nil && !os.SameFile(now, c.first) {
			c.closed = true
			c.source.Close()
		}
	}
	return c.Context.Err()
}

func TestCIHistoryBackgroundSourceCloseFencesDurablePrefix(t *testing.T) {
	for _, twoWorkflows := range []bool{false, true} {
		name := "before-final-ack"
		if twoWorkflows {
			name = "between-workflows"
		}
		t.Run(name, func(t *testing.T) {
			f := newCIHistoryFixture(t)
			ctx := t.Context()
			if twoWorkflows {
				f.mode.Store(5)
			}
			to := time.Now().UTC().Truncate(time.Second)
			cycle, err := cihealthsource.ReadCycle(ctx, f.runtime.source, f.runtime.repos, to.Add(-time.Hour), to)
			factCount := 1
			if twoWorkflows {
				factCount = 2
			}
			if err != nil || cycle.State() != cihealthsource.CycleObserved || len(cycle.Facts()) != factCount {
				t.Fatalf("expected independent captured workflows: %v %v", cycle.State(), err)
			}
			stage, err := f.owned.BindBackground(f.runtime.source, f.expect)
			if err != nil {
				t.Fatal(err)
			}
			first, err := f.root.Stat("ci-health-history-v1.json")
			if err != nil {
				t.Fatal(err)
			}
			readLease, release, err := f.runtime.source.BindLocalLifetime(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			storeLease, releaseStore := context.WithCancel(readLease)
			defer releaseStore()
			call := &ciHistoryCloseAfterCommit{Context: ctx, root: f.root, first: first, source: f.runtime.source}
			got, err := stage.PersistCycle(call, cycle)
			if !call.closed || ctx.Err() != nil || readLease.Err() != context.Canceled || storeLease.Err() != context.Canceled ||
				f.runtime.source.LifetimeActive() || err != cihealthsource.ErrCycleHistory || len(got.Commits) != 1 ||
				got.Commits[0].Scope != f.expect[0].Scope || got.Commits[0].Cursor != (cihealth.HistoryCursor{Revision: 1}) {
				t.Fatalf("source close must withhold success while preserving the actual prefix: %+v %v", got, err)
			}
			if twoWorkflows && (got.Failed == nil || *got.Failed != f.expect[1].Scope) || !twoWorkflows && got.Failed != nil {
				t.Fatalf("wrong refused workflow: %+v", got)
			}
			if again, err := stage.PersistCycle(ctx, cycle); err != cihealthsource.ErrCycleHistory || len(again.Commits) != 0 {
				t.Fatalf("closed persistence could retry: %+v %v", again, err)
			}
			f.ensureHistory(t, 1)
		})
	}
}
