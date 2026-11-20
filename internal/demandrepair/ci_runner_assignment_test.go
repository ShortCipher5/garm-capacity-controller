package demandrepair

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ciAssignmentFixtureRunner = int64(9007199254740999)

func ciAssignmentJobFixture() map[string]any {
	return map[string]any{
		"id": int64(ciFirstJobID), "run_id": int64(ciRunID), "run_attempt": int64(2),
		"runner_id": ciAssignmentFixtureRunner, "runner_name": "fixture-private-runner",
		"started_at": "2026-01-01T00:00:10Z", "completed_at": nil, "status": "in_progress", "conclusion": nil,
		"html_url": "https://example.invalid/fixture-private-runner", "name": "fixture-private-job",
	}
}

func ciAssignmentFixture(t *testing.T, job map[string]any) (*CIHealthSource, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	source := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("read escaped the existing authenticated client")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case ciFixtureAttempt:
			_, _ = io.WriteString(w, ciRunJSON(ciRepoID))
		case ciFixtureAttempt + "/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []any{job}})
		default:
			t.Error("unrecognized scoped endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return source, calls
}

func TestCIRunnerAssignmentBoundToOriginalSourceAndReadNotMutablePage(t *testing.T) {
	job := ciAssignmentJobFixture()
	source, calls := ciAssignmentFixture(t, job)
	a, err := source.ReadCIAttempt(t.Context(), repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	page, err := source.ListCIAttemptJobs(t.Context(), a, 1)
	if err != nil || len(page.Jobs) != 1 || calls.Load() != 2 {
		t.Fatal("existing workflow exact attempt/jobs reads not preserved", err)
	}
	readTime := page.ObservedAt
	page.ObservedAt = time.Now().Add(time.Hour)
	page.Jobs[0].RunnerID = 0
	page.Jobs[0].RunnerName = "replaced"
	page.Jobs[0].ID++
	*page.Jobs[0].StartedAt = time.Time{}
	page.Jobs = nil

	receipt, ok := page.RunnerAssignment(a, ciFirstJobID)
	facts, valid := receipt.Evidence(a)
	if !ok || !valid || facts.SourceID != source.SourceID() || facts.RepositoryID != ciRepoID || facts.WorkflowID != 10 ||
		facts.RunID != ciRunID || facts.Attempt != 2 || facts.JobID != ciFirstJobID || facts.RunnerID != ciAssignmentFixtureRunner ||
		!facts.ObservedAt.Equal(readTime) || facts.ExpiresAt.Sub(readTime) != ciRunnerAssignmentLifetime ||
		facts.StartedAt.IsZero() || !facts.EndedAt.IsZero() {
		t.Fatalf("exact receipt did not survive projection mutation: %+v %t", facts, valid)
	}
	for _, raw := range []string{fmt.Sprintf("%+v", receipt), fmt.Sprintf("%#v", receipt), fmt.Sprintf("%+v", facts)} {
		if strings.Contains(raw, "fixture-private") || strings.Contains(raw, "https://") || strings.Contains(raw, "synthetic-test-token") {
			t.Fatal("private/raw material appears in formatted receipt")
		}
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("opaque receipt became serializable", err)
	}
	nested := struct {
		Page    CIJobPage
		Receipt CIRunnerAssignmentReceipt
	}{page, receipt}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if raw := fmt.Sprintf(verb, nested); strings.Contains(raw, fmt.Sprint(ciAssignmentFixtureRunner)) ||
			strings.Contains(raw, facts.SourceID) || strings.Contains(raw, fmt.Sprint(ciFirstJobID)) {
			t.Fatalf("nested formatting (%s) exposed gated assignment facts", verb)
		}
	}
	if _, ok := receipt.evidence(a, facts.ExpiresAt); ok {
		t.Fatal("expired receipt is still usable")
	}
	if _, ok := receipt.evidence(a, facts.ObservedAt.Add(-time.Nanosecond)); ok {
		t.Fatal("receipt is usable before its observation")
	}
	fresh, err := source.ReadCIAttempt(t.Context(), repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewCIHealthSource(source.client, source.Repositories())
	if err != nil || shadow.SourceID() != source.SourceID() {
		t.Fatal("fixture must overlap source labels", err)
	}
	foreign, err := shadow.ReadCIAttempt(t.Context(), repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []CIAttempt{{}, fresh, foreign} {
		if _, ok := receipt.Evidence(other); ok {
			t.Fatal("matching labels/IDs supplied a different concrete source or attempt")
		}
	}
	if _, ok := (CIJobPage{Jobs: []CIJob{{ID: ciFirstJobID, RunID: ciRunID, RunAttempt: 2, RunnerID: ciAssignmentFixtureRunner}}}).RunnerAssignment(a, ciFirstJobID); ok {
		t.Fatal("a caller-created projection minted a receipt")
	}
}

func TestCIRunnerAssignmentClampsAttemptWallAndMonotonicCutoffsIndependently(t *testing.T) {
	s, _ := ciAssignmentFixture(t, ciAssignmentJobFixture())
	attempt, err := s.ReadCIAttempt(t.Context(), repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ciAssignmentJobFixture())
	if err != nil {
		t.Fatal(err)
	}
	var job ciJobWire
	if json.Unmarshal(raw, &job) != nil {
		t.Fatal("invalid wire fixture")
	}
	observed := time.Now()
	for _, tc := range []struct {
		name           string
		wallAge, age   time.Duration
		wallLifetime   time.Duration
		enforcedCutoff time.Duration
		want           bool
	}{
		{"forward wall skew retains old wall cap", 110 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second, true},
		{"forward wall skew already past old wall cap", 121 * time.Second, 10 * time.Second, 0, 0, false},
		{"slower wall retains old monotonic cap", 10 * time.Second, 110 * time.Second, 30 * time.Second, 10 * time.Second, true},
		{"old monotonic cap already expired", 10 * time.Second, 121 * time.Second, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := attempt
			a.at = observed.UTC().Add(-tc.wallAge)
			a.minted = observed.Add(-tc.age)
			r, ok := mintCIRunnerAssignment(a, job, observed)
			if ok != tc.want {
				t.Fatalf("mint availability %t, want %t", ok, tc.want)
			}
			if !ok {
				return
			}
			f, ok := r.evidence(a, observed.Add(tc.enforcedCutoff-time.Nanosecond))
			if !ok || f.ExpiresAt.Sub(observed.UTC()) != tc.wallLifetime {
				t.Fatal("private expiry did not preserve its independent cap")
			}
			if _, ok := r.evidence(a, observed.Add(tc.enforcedCutoff)); ok {
				t.Fatal("accepted evidence at the earlier actual cutoff")
			}
		})
	}
}

func TestCIRunnerAssignmentRequiresPositiveCoherentNumericEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want bool
	}{
		{"numeric without display name", func(j map[string]any) { delete(j, "runner_name") }, true},
		{"known completion", func(j map[string]any) {
			j["status"], j["conclusion"], j["completed_at"] = "completed", "failure", "2026-01-01T00:00:20Z"
		}, true},
		{"omitted run attempt", func(j map[string]any) { delete(j, "run_attempt") }, false},
		{"missing runner number", func(j map[string]any) { delete(j, "runner_id") }, false},
		{"zero runner number", func(j map[string]any) { j["runner_id"] = 0 }, false},
		{"missing start", func(j map[string]any) { delete(j, "started_at") }, false},
		{"start predates attempt running", func(j map[string]any) { j["started_at"] = "2025-12-31T23:59:59Z" }, false},
		{"active already ended", func(j map[string]any) { j["completed_at"] = "2026-01-01T00:00:20Z" }, false},
		{"pending numeric field", func(j map[string]any) { j["status"] = "pending" }, false},
		{"completes without end", func(j map[string]any) { j["status"], j["conclusion"] = "completed", "success" }, false},
		{"completed without result", func(j map[string]any) { j["status"], j["completed_at"] = "completed", "2026-01-01T00:00:20Z" }, false},
		{"skipped never ran", func(j map[string]any) {
			j["status"], j["conclusion"], j["completed_at"] = "completed", "skipped", "2026-01-01T00:00:20Z"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := ciAssignmentJobFixture()
			tc.edit(job)
			s, calls := ciAssignmentFixture(t, job)
			a, err := s.ReadCIAttempt(t.Context(), repo.GarmID, ciRunID, 2)
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.ListCIAttemptJobs(t.Context(), a, 1)
			if err != nil || len(p.Jobs) != 1 || calls.Load() != 2 || p.Jobs[0].RunAttempt != 2 {
				t.Fatalf("legacy timing projection changed: %v", err)
			}
			receipt, ok := p.RunnerAssignment(a, ciFirstJobID)
			if ok != tc.want {
				t.Fatal("numeric receipt availability", ok)
			}
			if ok {
				f, valid := receipt.Evidence(a)
				if !valid || f.RunnerID != ciAssignmentFixtureRunner {
					t.Fatal("lost exact numeric registration")
				}
			}
		})
	}
}
