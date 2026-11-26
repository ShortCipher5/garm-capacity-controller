package admission

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

// This test couples durable accounting to the separately observed job
// recovery criterion. Neither the registered retirement evidence producer nor
// the HTTPS job source is a live producer. Stock GARM does not dispatch here;
// the fixture models the observations a qualified installed trial must supply.
func TestCrossRepoRetirementAndExactJobRecoveryModel(t *testing.T) {
	size := Resources{CPU: 1000, RAM: 2048, Disk: 8192, Runners: 1}
	for _, tc := range []struct {
		name string
		cap  Resources
	}{
		{"runner cap", Resources{CPU: 4000, RAM: 8192, Disk: 32768, Runners: 1}},
		{"cpu cap with spare runner slots", Resources{CPU: 1000, RAM: 8192, Disk: 32768, Runners: 4}},
		{"ram cap with spare runner slots", Resources{CPU: 4000, RAM: 2048, Disk: 32768, Runners: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			// The fixed timeline is old enough for ObserveExactJob's no-future
			// check but independent of wall-clock timing and scheduler speed.
			at := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
			config := fixture(at)
			config.Global = tc.cap
			for id, host := range config.Hosts {
				host.Budget.Disk = 32768
				config.Hosts[id] = host
			}
			for id, pool := range config.Pools {
				pool.Budget.Disk = 32768
				config.Pools[id] = pool
			}
			store := sqliteFixtureStore(t, config, false)
			store.now = func() time.Time { return at }
			reconcileSQLite(t, store, nil)
			other, err := OpenSQLite(store.path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			other.now = store.now
			a := modelWaitingJob{demandrepair.Repository{GarmID: "app", Host: "github.example", Owner: "sample", Name: "app"}, 318, 2, 9007199254741001}
			b := modelWaitingJob{demandrepair.Repository{GarmID: "api", Host: "github.example", Owner: "sample", Name: "api"}, 319, 3, 9007199254741002}
			jobs := []modelWaitingJob{a, b}
			var requests [2]Request
			var results [2]Decision
			var errs [2]error
			var wg sync.WaitGroup
			begin := make(chan struct{})
			for i, handle := range []*SQLiteStore{store, other} {
				j := jobs[i]
				requests[i] = Request{
					Key:  evidenceDigest([]byte(fmt.Sprintf("%s/%s/%d/%d/%d", j.repo.Owner, j.repo.Name, j.run, j.attempt, j.id))),
					Pool: j.repo.GarmID, Host: "a", Resources: size,
				}
				wg.Go(func() {
					<-begin
					results[i], errs[i] = handle.Admit(ctx, requests[i])
				})
			}
			close(begin)
			wg.Wait()

			winner := 0
			if errs[0] != nil {
				winner = 1
			}
			loser := 1 - winner
			held, waiting := requests[winner], requests[loser]
			exactJob := jobs[loser]
			if errs[winner] != nil || !results[winner].Simulation || results[winner].Reservation.Request != held ||
				!errors.Is(errs[loser], ErrDenied) || results[loser] != (Decision{}) {
				t.Fatalf("wanted one simulated commitment and one denial, got %+v %v", results, errs)
			}
			assertWaiting := func(s *SQLiteStore) {
				t.Helper()
				if got := slotReuseCharged(t, s); got != size {
					t.Fatalf("original full vector was not charged: %+v", got)
				}
				if _, err := s.Admit(ctx, waiting); !errors.Is(err, ErrDenied) {
					t.Fatalf("other repo got capacity before verified retirement: %v", err)
				}
			}
			assertWaiting(other)

			identity := slotReuseFixtureEvidence(held, 0, at).Identity
			row := InventoryResource{
				ID: identity.ResourceID, Host: held.Host, Pool: held.Pool,
				ReservationKey: held.Key, Resources: held.Resources, State: "idle",
			}
			reconcileSQLite(t, store, []InventoryResource{row})
			if _, err := store.MarkDeleting(ctx, held); err != nil {
				t.Fatal(err)
			}
			row.State = "deleting"
			fence := reconcileSQLite(t, store, []InventoryResource{row})
			at = at.Add(time.Second) // fence observation follows the durable barrier.
			evidence := slotReuseFixtureEvidence(held, fence.Epoch, at)
			if got, err := other.ReleaseRetiredSlot(ctx, held, evidence); !errors.Is(err, ErrSlotReuseEvidence) || got.Status != SlotReuseUnsupportedSource {
				t.Fatalf("stock production registry accepted fixture facts: %+v %v", got, err)
			}
			assertWaiting(other)

			registerSlotReuseFixtureSource(t)
			unsettled := evidence
			unsettled.Teardown.StorageReleased = false
			if got, err := other.ReleaseRetiredSlot(ctx, held, unsettled); !errors.Is(err, ErrSlotReuseEvidence) || got.Status != SlotReuseAwaitingStorageEvidence {
				t.Fatalf("incomplete retirement yielded credit: %+v %v", got, err)
			}
			assertWaiting(other)
			decision, err := store.ReleaseRetiredSlot(ctx, held, evidence)
			if err != nil || decision.Replay || decision.ReleasedAt != at || decision.EvidenceDigest == "" {
				t.Fatalf("complete synthetic retirement failed: %+v %v", decision, err)
			}
			if got := slotReuseCharged(t, other); got != (Resources{}) {
				t.Fatalf("original vector not released: %+v", got)
			}
			readmitted, err := other.Admit(ctx, waiting)
			if err != nil || readmitted.Replay || !readmitted.Simulation || readmitted.Reservation.Request != waiting {
				t.Fatalf("exact blocked request did not regain modeled headroom: %+v %v", readmitted, err)
			}
			again, err := store.ReleaseRetiredSlot(ctx, held, evidence)
			if err != nil || !again.Replay || again.EvidenceDigest != decision.EvidenceDigest || slotReuseCharged(t, store) != size {
				t.Fatalf("retirement replay affected replacement charge: %+v %v", again, err)
			}
			// The ledger assertions above do not count as queue recovery. Check
			// a separate positive scoped job observation against a declared
			// interval and exact repo/run/attempt/job and assignment.
			assertExactRecoveryModel(t, exactJob, decision.ReleasedAt)
		})
	}
}

type modelWaitingJob struct {
	repo             demandrepair.Repository
	run, attempt, id int64
}

func assertExactRecoveryModel(t *testing.T, job modelWaitingJob, released time.Time) {
	t.Helper()
	const window = 30 * time.Second
	const expectedRunner int64 = 417
	const runnerName = "replacement-fixture"
	for _, tc := range []struct {
		name                  string
		owner, state          string
		run, attempt, id, rid int64
		start                 time.Duration
		want                  bool
	}{
		{name: "same job started within window", want: true},
		{name: "runner ready but job remains queued", state: "queued"},
		{name: "different repository", owner: "other"},
		{name: "different run", run: job.run + 1},
		{name: "different attempt", attempt: job.attempt + 1},
		{name: "different job", id: job.id + 1},
		{name: "different assignment", rid: expectedRunner + 1},
		{name: "started before release", start: -time.Second},
		{name: "started after recovery window", start: window + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, owner, id, run, attempt, runner, start := "in_progress", job.repo.Owner, job.id, job.run, job.attempt, expectedRunner, 5*time.Second
			if tc.state != "" {
				status = tc.state
			}
			if tc.owner != "" {
				owner = tc.owner
			}
			if tc.id != 0 {
				id = tc.id
			}
			if tc.run != 0 {
				run = tc.run
			}
			if tc.attempt != 0 {
				attempt = tc.attempt
			}
			if tc.rid != 0 {
				runner = tc.rid
			}
			if tc.start != 0 {
				start = tc.start
			}
			route := fmt.Sprintf("/repos/%s/%s/actions", job.repo.Owner, job.repo.Name)
			expectPath := fmt.Sprintf("%s/jobs/%d", route, job.id)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != expectPath || req.Header.Get("Authorization") != "Bearer fixture-only" {
					t.Error("unexpected request to local job fixture")
					http.NotFound(w, req)
					return
				}
				endpoint := "https://" + req.Host
				responseRoute := strings.Replace(route, "/"+job.repo.Owner+"/", "/"+owner+"/", 1)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{
					"id": id, "run_id": run, "run_attempt": attempt, "runner_id": runner,
					"runner_name": runnerName, "url": fmt.Sprintf("%s%s/jobs/%d", endpoint, responseRoute, id),
					"run_url": fmt.Sprintf("%s%s/runs/%d", endpoint, responseRoute, run),
					"status":  status, "conclusion": nil, "started_at": released.Add(start), "completed_at": nil,
				}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			client, err := demandrepair.NewGitHub(server.URL, job.repo.Host, "fixture-ref",
				func(context.Context, string) (string, error) { return "fixture-only", nil },
				&tls.Config{RootCAs: roots}, []demandrepair.Repository{job.repo})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			observation, err := client.ObserveExactJob(t.Context(), job.repo, job.id)
			r := observation.Record()
			got := err == nil && observation.Valid() && r.Repository == job.repo && r.RunID == job.run &&
				r.RunAttempt == job.attempt && r.JobID == job.id && r.RunnerID == expectedRunner && r.RunnerName == runnerName &&
				r.Status == "in_progress" && !r.StartedAt.Before(released) && !r.StartedAt.After(released.Add(window))
			if got != tc.want {
				t.Fatalf("recovery=%v, want %v; observation=%+v err=%v", got, tc.want, r, err)
			}
		})
	}
}
