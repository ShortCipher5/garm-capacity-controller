package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
)

func TestCIHistoryTrendsExplicitOwnerProjection(t *testing.T) {
	const owner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, mode := range []string{"persistence-only", "owner-close", "exact-404", "record-budget", "retry-recovery"} {
		t.Run(mode, func(t *testing.T) {
			f := newCIHistoryFixture(t)
			ctx := t.Context()
			if mode == "record-budget" {
				seedCIHistoryRecordBudget(t, f)
			}
			budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
			policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}},
				Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
			store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "admission.db"), policy)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			h, err := adminview.New(store, owner, nil, false)
			if err != nil || h.SetCIHealthSnapshot(f.runtime.snapshot) != nil {
				t.Fatal("fixture owner route unavailable", err)
			}
			ownerLoopback := httptest.NewTLSServer(h)
			defer ownerLoopback.Close()
			read := func(token string) (int, string) {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, ownerLoopback.URL+"/admin/api/v1/ci-health", nil)
				if err != nil {
					t.Fatal(err)
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				res, err := ownerLoopback.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				if err != nil || res.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("owner response unavailable or cacheable", err)
				}
				return res.StatusCode, string(raw)
			}
			if mode == "persistence-only" {
				if err := f.runtime.attachOwnedHistory(f.owned, f.expect); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.runtime.attachOwnedHistoryWithTrends(f.owned, f.expect[:1]); err == nil {
					t.Fatal("incomplete workflow/revision approval enabled trends")
				}
				if err := f.runtime.attachOwnedHistoryWithTrends(f.owned, f.expect); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "retry-recovery" {
				f.mode.Store(7)
			}
			if !f.runtime.runCycle(ctx) {
				t.Fatal("positive exact timing fixture failed")
			}
			if mode == "retry-recovery" {
				status, body := read(owner)
				if status != http.StatusOK || !strings.Contains(body, `"recovered_initial_failures":0`) ||
					strings.Contains(body, `"recovered_initial_failures":1`) {
					t.Fatal("an initial failure alone established recovery")
				}
				f.mode.Store(8)
				if !f.runtime.runCycle(ctx) {
					t.Fatal("exact same-run retry timing fixture failed")
				}
			}
			status, body := read(owner)
			if status != http.StatusOK || !strings.Contains(body, `"state":"observed"`) {
				t.Fatalf("fresh timing is missing: %d, %s", status, body)
			}
			if mode == "persistence-only" {
				if strings.Contains(body, `"history":`) {
					t.Fatal("ordinary storage consent exposed an owner history view")
				}
				return
			}
			var view struct {
				At      time.Time `json:"observed_at"`
				History struct {
					Expires   time.Time `json:"expires_at"`
					Coverage  string    `json:"coverage"`
					Reports   string    `json:"test_results"`
					Workflows []struct {
						ID        string `json:"workflow_id"`
						Selection string `json:"selection_limited"`
						Previous  struct {
							Recovered *int `json:"recovered_initial_failures"`
						} `json:"previous"`
						Current struct {
							To        time.Time `json:"to"`
							Recovered *int      `json:"recovered_initial_failures"`
							Initial   struct {
								Runs     int `json:"distinct_runs"`
								Attempts struct {
									Observed, Active, Failure int
								} `json:"attempts"`
								Jobs, Steps struct {
									Observed int
								}
							} `json:"initial"`
							Retries struct {
								Attempts struct{ Success int } `json:"attempts"`
							} `json:"retries"`
						} `json:"current"`
					} `json:"workflows"`
				} `json:"history"`
			}
			if err := json.Unmarshal([]byte(body), &view); err != nil ||
				view.History.Coverage != "retained_subset" || view.History.Reports != "unavailable" ||
				len(view.History.Workflows) != 1 || !view.History.Expires.After(view.At) ||
				view.History.Expires.After(view.At.Add(2*time.Minute)) {
				t.Fatalf("history omitted, overstated or outside exact lease: %+v, %v", view, err)
			}
			w := view.History.Workflows[0]
			wantRuns, wantJobs, wantSteps, wantSelection := 1, 0, 0, ""
			wantActive, wantInitialFailures, wantRetrySuccesses, wantRecovered := 1, 0, 0, 0
			if mode == "record-budget" {
				// Fresh positive timing has one active attempt. Exactly 38 of
				// the 39 older complete attempts fit beside it.
				wantRuns, wantJobs, wantSteps, wantSelection = 39, 38*28, 38*279, "records"
			}
			if mode == "retry-recovery" {
				wantActive, wantInitialFailures, wantRetrySuccesses, wantRecovered = 0, 1, 1, 1
			}
			if w.ID != "622" || !w.Current.To.Equal(view.At) || w.Current.Initial.Runs != wantRuns ||
				w.Current.Initial.Attempts.Observed != wantRuns || w.Current.Initial.Attempts.Active != wantActive || w.Current.Initial.Attempts.Failure != wantInitialFailures ||
				w.Current.Initial.Jobs.Observed != wantJobs || w.Current.Initial.Steps.Observed != wantSteps || w.Selection != wantSelection ||
				w.Previous.Recovered == nil || *w.Previous.Recovered != 0 || w.Current.Recovered == nil || *w.Current.Recovered != wantRecovered ||
				w.Current.Retries.Attempts.Success != wantRetrySuccesses {
				t.Fatalf("wrong retained scope or invented outcome: %+v", w)
			}
			if status, body := read(""); status != http.StatusUnauthorized || strings.Contains(body, `"history":`) {
				t.Fatal("owner role was bypassed")
			}
			switch mode {
			case "owner-close":
				if err := f.owned.Close(); err != nil {
					t.Fatal(err)
				}
				if status, body := read(owner); status != http.StatusOK || !strings.Contains(body, `"state":"observed"`) ||
					strings.Contains(body, `"history":`) {
					t.Fatalf("closed store still exposed history or removed independent timing: %s", body)
				}
			case "exact-404":
				f.mode.Store(6)
				if f.runtime.runCycle(ctx) {
					t.Fatal("404 appeared as a fresh observation")
				}
				if status, body := read(owner); status != http.StatusOK || !strings.Contains(body, `"state":"unavailable"`) ||
					strings.Contains(body, `"history":`) {
					t.Fatalf("failed cycle reused previous history: %s", body)
				}
				f.ensureHistory(t, 1)
			case "record-budget":
				if f.runtime.activeHistory() == nil || !f.runtime.runCycle(ctx) || f.runtime.activeHistory() == nil {
					t.Fatal("bounded read closed the owner or stopped later persistence")
				}
				if status, body := read(owner); status != http.StatusOK || !strings.Contains(body, `"selection_limited":"records"`) {
					t.Fatal("later positive publication lost bounded history")
				}
				if err := f.owned.Close(); err != nil {
					t.Fatal(err)
				}
				if status, body := read(owner); status != http.StatusOK || strings.Contains(body, `"history":`) {
					t.Fatal("bounded history outlived closed authority")
				}
				f.ensureHistory(t, 4) // two seed transitions and two successful gateway cycle commits
			}
		})
	}
}

func seedCIHistoryRecordBudget(t *testing.T, f *ciHistoryFixture) {
	t.Helper()
	ctx := t.Context()
	if err := f.owned.Close(); err != nil {
		t.Fatal(err)
	}
	h, err := cihealth.OpenHistory(ctx, cihealth.HistoryInput{Enabled: true, Root: f.root})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	now := time.Now()
	stamp := now.Add(-time.Hour).UTC()
	scope := f.expect[0].Scope
	c := cihealth.Clock{Phase: cihealth.Completed, StartedAt: stamp.Add(-time.Minute), CompletedAt: stamp, ObservedAt: stamp}
	rows := make([]cihealth.AttemptFacts, 39)
	for i := range rows {
		a := cihealth.AttemptKey{Source: scope.Source, RepositoryID: scope.RepositoryID, WorkflowID: scope.WorkflowID, RunID: int64(i + 1), Attempt: 1}
		fact := cihealth.AttemptFacts{Key: a, AsOf: stamp, Clock: c}
		for j := int64(1); j <= 28; j++ {
			key := cihealth.JobKey{Attempt: a, JobID: j}
			job := cihealth.JobFacts{Key: key, Clock: c}
			for step := int64(1); step <= 10; step++ {
				if j == 28 && step == 10 {
					break
				}
				job.Steps = append(job.Steps, cihealth.StepFacts{Key: cihealth.StepKey{Job: key, Number: step}, Clock: c})
			}
			fact.Jobs = append(fact.Jobs, job)
		}
		rows[i] = fact
	}
	one, two := cihealth.HistoryCursor{Revision: 1}, cihealth.HistoryCursor{Revision: 2}
	if err := h.Apply(ctx, scope, cihealth.HistoryCursor{}, one, rows[:38], now); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply(ctx, scope, one, two, rows[38:], now); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	f.owned, f.expect, err = cihealthsource.OpenOwnedCycleHistory(ctx, f.runtime.source, cihealthsource.OwnedCycleHistoryInput{
		Enabled: true, Root: f.root, Workflows: []cihealth.HistoryScope{scope, f.expect[1].Scope},
	})
	if err != nil || len(f.expect) != 2 || f.expect[0].Cursor != two {
		t.Fatal("fixture failed to independently review the held revisions", err)
	}
	t.Cleanup(func() { _ = f.owned.Close() })
}

func TestCIHistoryTrendsLeaseUsesEarliestProofAndCurrentRevision(t *testing.T) {
	f := newCIHistoryFixture(t)
	ctx := t.Context()
	f.mode.Store(5) // two independently observed, approved workflows
	if err := f.runtime.attachOwnedHistoryWithTrends(f.owned, f.expect); err != nil {
		t.Fatal(err)
	}
	h := f.runtime.activeHistory()
	to := time.Now().UTC().Truncate(time.Second)
	observe := func() cihealthsource.Cycle {
		cycle, err := cihealthsource.ReadCycle(ctx, f.runtime.source, f.runtime.repos, to.Add(-time.Hour), to)
		if err != nil || cycle.State() != cihealthsource.CycleObserved || len(cycle.Facts()) != 2 {
			t.Fatalf("timing fixture did not retain two live workflow proofs: %v", err)
		}
		return cycle
	}
	first := observe()
	if got, err := h.PersistCycle(ctx, first); err != nil || len(got.Commits) != 2 {
		t.Fatalf("could not persist first exact revisions: %+v, %v", got, err)
	}
	held, err := h.SummarizeObservedTrends(ctx, first)
	at, expiry, rows, ok := held.Summary()
	earliest := first.Facts()[0].Clock.ObservedAt
	for _, fact := range first.Facts() {
		if fact.Clock.ObservedAt.Before(earliest) {
			earliest = fact.Clock.ObservedAt
		}
	}
	if err != nil || !ok || len(rows) != 2 || !at.Equal(first.ObservedAt()) ||
		!expiry.Equal(earliest.Add(2*time.Minute)) || !expiry.Before(at.Add(2*time.Minute)) {
		t.Fatalf("did not apply the earliest current exact proof: %s to %s, rows %d, %v", at, expiry, len(rows), err)
	}
	next := observe()
	if got, err := h.PersistCycle(ctx, next); err != nil || len(got.Commits) != 2 || got.Commits[0].Cursor.Revision != 2 {
		t.Fatalf("could not persist the next revision: %+v, %v", got, err)
	}
	if held.Current() {
		t.Fatal("an older approved revision remained current after replacement")
	}
	if old, err := h.SummarizeObservedTrends(ctx, first); err == nil || old.Current() {
		t.Fatal("an older source cycle renewed authorization")
	}
}
