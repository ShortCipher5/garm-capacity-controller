package cihealthsource

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func heldCycleHistory(t *testing.T) *cihealth.History {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	h, err := cihealth.OpenHistory(t.Context(), cihealth.HistoryInput{Enabled: true, Root: root, Initialize: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	return h
}

func historySourceCycle(t *testing.T) (*demandrepair.CIHealthSource, *demandrepair.GitHubClient, Cycle, []cihealth.HistoryScope) {
	t.Helper()
	from, to := relativeWindow()
	repos := cycleRepos()
	created := to.Add(-time.Minute)
	s, client := fixtureReposWithClient(t, repos, func(w http.ResponseWriter, r *http.Request) {
		repo := repos[0]
		if strings.HasPrefix(r.URL.Path, "/repos/fixture/another") {
			repo = repos[1]
		}
		base := "/repos/fixture/" + repo.Name
		firstRun := runID + 10*(repo.GitHubID-repoID)
		maxIndex := int64(1)
		if repo.GitHubID == repoID {
			maxIndex = 3
		}
		row := func(run int64) map[string]any {
			v := cycleRun(repo, run, 1, "queued", created)
			v["workflow_id"] = workflowID + run - firstRun
			return v
		}
		switch {
		case r.URL.Path == base+"/actions/runs":
			var rows []map[string]any
			if r.URL.Query().Get("status") == "queued" {
				// The fixtures arrive in reverse workflow order, independent
				// of the adapter's deterministic storage order.
				for index := maxIndex - 1; index >= 0; index-- {
					rows = append(rows, row(firstRun+index))
				}
			}
			runPage(w, rows, len(rows))
		case strings.HasPrefix(r.URL.Path, base+"/actions/runs/"):
			p := strings.Split(strings.TrimPrefix(r.URL.Path, base+"/actions/runs/"), "/")
			run, err := strconv.ParseInt(p[0], 10, 64)
			if err != nil || run < firstRun || run >= firstRun+maxIndex || len(p) < 3 || p[1] != "attempts" || p[2] != "1" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if len(p) == 3 {
				_ = json.NewEncoder(w).Encode(row(run))
			} else if len(p) == 4 && p[3] == "jobs" {
				jobPage(w, run, 1, "queued")
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case r.URL.Path == base:
			_ = json.NewEncoder(w).Encode(repoMetadata(repo))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	cycle, err := ReadCycle(t.Context(), s, []string{"another", "runners"}, from, to)
	if err != nil || cycle.State() != CycleObserved || len(cycle.Facts()) != 4 {
		t.Fatalf("fixture did not reach real scoped observations: %v, %v", cycle.State(), err)
	}
	return s, client, cycle, []cihealth.HistoryScope{
		{Source: s.SourceID(), RepositoryID: repoID, WorkflowID: workflowID},
		{Source: s.SourceID(), RepositoryID: repoID, WorkflowID: workflowID + 1},
		{Source: s.SourceID(), RepositoryID: repoID + 1, WorkflowID: workflowID},
		{Source: s.SourceID(), RepositoryID: repoID, WorkflowID: workflowID + 2},
	}
}

func TestCycleHistoryPersistsOnlyApprovedPositiveScopedFacts(t *testing.T) {
	source, _, cycle, scopes := historySourceCycle(t)
	store := heldCycleHistory(t)
	approved := []cihealth.HistoryScope{scopes[2], scopes[0], scopes[1]}
	h, err := NewCycleHistory(store, source, approved)
	if err != nil {
		t.Fatal(err)
	}
	approved[0] = scopes[3]    // The allowed scope is owned, not a live slice.
	cycle.state = CyclePartial // Exercise partial positives from the real source fixture.
	expected := []HistoryExpectation{{Scope: scopes[2]}, {Scope: scopes[0]}, {Scope: scopes[1]}}
	got, err := h.Persist(t.Context(), cycle, expected)
	if err != nil || got.CycleState != CyclePartial || len(got.Commits) != 3 || got.Failed != nil {
		t.Fatalf("unexpected persist: %+v, %v", got, err)
	}
	for i, commit := range got.Commits {
		if commit.Scope != scopes[i] || commit.Cursor != (cihealth.HistoryCursor{Revision: 1}) {
			t.Fatalf("unscoped/out-of-order cursor: %+v", commit)
		}
		rows, err := h.Recent(t.Context(), scopes[i], 1)
		if err != nil || len(rows) != 1 || len(rows[0].Jobs) != 1 || rows[0].Clock.Phase != cihealth.Queued ||
			!rows[0].Clock.StartedAt.IsZero() || !rows[0].Jobs[0].QueuedAt.IsZero() || !rows[0].Jobs[0].FirstQueuedObservedAt.IsZero() {
			t.Fatalf("source timing uncertainty changed: %+v, %v", rows, err)
		}
		rows[0].Jobs[0].Key.JobID++
		if fresh, err := h.Recent(t.Context(), scopes[i], 1); err != nil || fresh[0].Jobs[0].Key.JobID != jobID {
			t.Fatal("returned attempt wasn't separately owned", err)
		}
	}
	if c, err := store.Cursor(t.Context(), scopes[3]); err != nil || c != (cihealth.HistoryCursor{}) {
		t.Fatal("unapproved captured workflow advanced", c, err)
	}
	if _, err := h.Cursor(t.Context(), scopes[3]); err != ErrCycleHistory {
		t.Fatal("unapproved cursor read accepted", err)
	}
	if rows, err := h.Recent(t.Context(), scopes[3], 1); rows != nil || err != ErrCycleHistory {
		t.Fatal("unapproved history read accepted", err)
	}
	if replay, err := h.Persist(t.Context(), cycle, expected); err != nil || len(replay.Commits) != 3 || replay.Commits[2].Cursor.Revision != 1 {
		t.Fatalf("exact explicitly replayed receipt changed: %+v, %v", replay, err)
	}
}

func TestCycleHistoryPreflightsAndReportsOnlyCommittedPrefix(t *testing.T) {
	source, _, cycle, scopes := historySourceCycle(t)
	store := heldCycleHistory(t)
	h, err := NewCycleHistory(store, source, scopes[:3])
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	expected := []HistoryExpectation{{Scope: scopes[2]}, {Scope: scopes[0]}, {Scope: scopes[1]}}
	for _, in := range []Cycle{
		{state: CycleUnavailable, observedAt: cycle.observedAt, facts: cycle.facts, source: cycle.source},
		{state: CycleObserved, observedAt: cycle.observedAt.Add(-historyCycleFreshness), facts: cycle.facts, source: cycle.source},
	} {
		if got, err := h.Persist(ctx, in, expected); err != ErrCycleHistory || got.CycleState != CycleUnavailable || len(got.Commits) != 0 || got.Failed != nil {
			t.Fatalf("stale/unavailable advanced history: %+v, %v", got, err)
		}
	}
	if got, err := h.Persist(ctx, cycle, expected[:2]); err != ErrCycleHistory || len(got.Commits) != 0 {
		t.Fatalf("missing relevant expected cursor permitted subset writes: %+v, %v", got, err)
	}
	if got, err := h.Persist(ctx, Cycle{state: CycleObserved, observedAt: cycle.observedAt, source: cycle.source}, expected); err != nil || len(got.Commits) != 0 {
		t.Fatalf("no positive facts was not a no-op: %+v, %v", got, err)
	}
	first := cihealth.HistoryCursor{Revision: 1, Checkpoint: 42}
	second := cihealth.HistoryCursor{Revision: 1, Checkpoint: 43}
	for i, cursor := range []cihealth.HistoryCursor{first, second} {
		if old, err := h.Cursor(ctx, scopes[i]); err != nil || old != (cihealth.HistoryCursor{}) {
			t.Fatalf("ineligible request changed store: %+v, %v", old, err)
		}
		if err := store.Apply(ctx, scopes[i], cihealth.HistoryCursor{}, cursor, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	expected[1].Cursor = first
	got, err := h.Persist(ctx, cycle, expected)
	if !errors.Is(err, cihealth.ErrHistoryConflict) || len(got.Commits) != 1 || got.Commits[0].Scope != scopes[0] ||
		got.Commits[0].Cursor != (cihealth.HistoryCursor{Revision: 2, Checkpoint: 42}) || got.Failed == nil || *got.Failed != scopes[1] {
		t.Fatalf("conflict hid successful prefix or advanced checkpoint: %+v, %v", got, err)
	}
	if after, err := h.Cursor(ctx, scopes[1]); err != nil || after != second {
		t.Fatalf("conflicting scope changed: %+v, %v", after, err)
	}
	if after, err := h.Cursor(ctx, scopes[2]); err != nil || after != (cihealth.HistoryCursor{}) {
		t.Fatalf("continued after conflict: %+v, %v", after, err)
	}
}

type uncomparableHistorySource struct {
	Source
	_ []int
}

func TestCycleHistoryRequiresExactOwnedSourceNotOriginHash(t *testing.T) {
	source, client, cycle, scopes := historySourceCycle(t)
	store := heldCycleHistory(t)
	other, err := demandrepair.NewCIHealthSource(client, source.Repositories())
	if err != nil || other == source || other.SourceID() != source.SourceID() {
		t.Fatal("fixture did not create distinct readers for one authenticated origin", err)
	}
	h, err := NewCycleHistory(store, source, scopes[:1])
	if err != nil {
		t.Fatal(err)
	}
	from, to := relativeWindow()
	unsupported := uncomparableHistorySource{Source: source}
	if _, err := NewCycleHistory(store, unsupported, scopes[:1]); err != ErrCycleHistory {
		t.Fatal("unsupported source constructed persistent history adapter", err)
	}
	for _, in := range []Source{other, unsupported} {
		c, err := ReadCycle(t.Context(), in, []string{"runners"}, from, to)
		if err != nil || c.State() != CycleObserved || len(c.Facts()) == 0 {
			t.Fatalf("fallback timing read did not produce the same-origin positive facts: %v, %v", c.State(), err)
		}
		if got, err := h.Persist(t.Context(), c, []HistoryExpectation{{Scope: scopes[0]}}); err != ErrCycleHistory || len(got.Commits) != 0 {
			t.Fatalf("different reader persisted same-origin facts: %+v, %v", got, err)
		}
	}
	if old, err := h.Cursor(t.Context(), scopes[0]); err != nil || old != (cihealth.HistoryCursor{}) {
		t.Fatalf("different reader changed cursor: %+v, %v", old, err)
	}
	if got, err := h.Persist(t.Context(), cycle, []HistoryExpectation{{Scope: scopes[0]}}); err != nil || len(got.Commits) != 1 {
		t.Fatalf("original exact reader refused: %+v, %v", got, err)
	}
}

func TestCycleHistoryRequiresCurrentClockBeforeEachScopedWrite(t *testing.T) {
	start := time.Now()
	observed, prior, cutoff := start.Add(-time.Second), start.Add(time.Second), start.Add(time.Minute)
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before persistence started", start.Add(-time.Nanosecond), false},
		{"before the observed capture", observed.Add(-time.Nanosecond), false},
		{"wall rolled back after earlier workflow", prior.Add(-time.Nanosecond).UTC(), false},
		{"same wall observation remains valid", prior.UTC(), true},
		{"strict deadline", cutoff, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyCycleTimeCurrent(start, prior, observed, cutoff, tc.at); got != tc.want {
				t.Fatalf("got %t, want %t", got, tc.want)
			}
		})
	}
}
