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
	"testing/synctest"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
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
			config := fixture(time.Now())
			config.Global = tc.cap
			delete(config.Hosts, "b")
			host := config.Hosts["a"]
			host.Budget.Disk = 32768
			config.Hosts["a"] = host
			for id, pool := range config.Pools {
				pool.Budget.Disk = 32768
				pool.Hosts = []string{"a"}
				config.Pools[id] = pool
			}
			store := sqliteFixtureStore(t, config, false)
			fake := testsupport.NewFakeIncus(t, "cross-repo-model")
			t.Cleanup(fake.Client.Close)
			reconcileModelRunner(t, store, config, fake.Client, nil, "")
			other, err := OpenSQLite(store.path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
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
					Key:  executionKey(ExecutionImage{Controller: modelController, Name: j.runnerName()}, j.repo.GarmID),
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

			spec := incusapi.InstanceSpec{
				Name: jobs[winner].runnerName(), Type: "container", Profiles: []string{"runner-base"},
				ImageFingerprint: strings.Repeat("a", 64), UserData: "#cloud-config\nfixture",
				CPUMillis: size.CPU, RAMMiB: size.RAM, DiskMiB: size.Disk,
				ControllerID: modelController, PoolID: held.Pool, ReservationKey: held.Key,
			}
			if _, err := fake.Client.Create(ctx, spec); err != nil {
				t.Fatal(err)
			}
			reconcileModelRunner(t, store, config, fake.Client, &held, "idle")
			if _, err := store.MarkDeleting(ctx, held); err != nil {
				t.Fatal(err)
			}
			identity := reconcileModelRunner(t, store, config, fake.Client, &held, "deleting")
			fake.SetGuestVisible(false)
			at := time.Now().UTC()
			store.now = func() time.Time { return at }
			other.now = store.now
			evidence := slotReuseFixtureEvidence(held, identity.Epoch, at)
			evidence.Identity = identity
			digest := slotReuseIdentityDigest(identity)
			evidence.Settlement.ObservedAt = at
			evidence.Settlement.IdentityDigest, evidence.Teardown.IdentityDigest, evidence.Fence.IdentityDigest = digest, digest, digest
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

const modelController = "controller-1"

// Preserve the original enrollment and collect a new complete observation on
// every pass, including the empty pre-admission inventory.
func reconcileModelRunner(t *testing.T, store *SQLiteStore, config Config, client *incusapi.Client, held *Request, state string) SlotReuseIdentity {
	t.Helper()
	ctx := t.Context()
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := HostBinding{
		Host: "a", ServerKeyFingerprint: physical.ServerKeyFingerprint(),
		OriginFingerprint: physical.OriginFingerprint(), ScopeFingerprint: physical.ScopeFingerprint(),
		ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(), ObservedAt: store.now(),
	}
	if err := store.BindHostIdentities(ctx, fence, []HostBinding{binding}); err != nil {
		t.Fatal(err)
	}
	if err := client.PinPhysicalIdentity(physical); err != nil {
		t.Fatal(err)
	}
	observed := store.now()
	list, err := client.Instances(incusapi.WithInventoryFence(ctx, fence.Nonce))
	if err != nil {
		t.Fatal(err)
	}
	if operations, err := client.Operations(ctx); err != nil || len(operations) != 0 {
		t.Fatalf("fixture operations: %v %v", operations, err)
	}
	snap := InventorySnapshot{Hosts: observations(config, observed)}
	var identity SlotReuseIdentity
	if held == nil {
		if len(list) != 0 {
			t.Fatal("unexpected pre-admission allocation")
		}
	} else {
		if len(list) != 1 {
			t.Fatal("incomplete fixture inventory", len(list))
		}
		listed := list[0].ListedIdentity
		observation := listed.Observation()
		subject := observation.Subject
		if observation.CompletedAt.IsZero() || subject.ReservationKey != held.Key || subject.PoolID != held.Pool {
			t.Fatal("missing exact fixture incarnation")
		}
		identity = SlotReuseIdentity{
			Source: slotReuseFixtureSource, SourceID: "model-source", Route: evidenceDigest([]byte("model-provider-route")),
			RequestKey: subject.ReservationKey, Pool: subject.PoolID, Host: held.Host,
			CPU: subject.CPUMillis, RAM: subject.RAMMiB, Disk: held.Resources.Disk,
			Controller: subject.ControllerID, Daemon: subject.BackendKeyFingerprint, Project: subject.ScopeFingerprint,
			Instance: subject.UUID, Generation: subject.Generation, Epoch: fence.Epoch, ObservedAt: observation.CompletedAt,
		}
		identity.ResourceID = slotReuseResourceID(identity)
		row := InventoryResource{ID: identity.ResourceID, Host: held.Host, Pool: held.Pool, ReservationKey: held.Key, Resources: held.Resources, State: state}
		snap.Resources = []InventoryResource{row.WithListedIdentity(listed)}
	}
	if err := store.CompleteReconciliation(ctx, fence, snap); err != nil {
		t.Fatal(err)
	}
	return identity
}

type modelWaitingJob struct {
	repo             demandrepair.Repository
	run, attempt, id int64
}

func (j modelWaitingJob) runnerName() string {
	return fmt.Sprintf("%s-%s-%d-%d-%d", j.repo.Owner, j.repo.Name, j.run, j.attempt, j.id)
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
			// Observe after every modeled start, including the late-start negative.
			// Only the test reader's clock advances; the actual durable release
			// timestamp anchors every response and the acceptance window.
			synctest.Test(t, func(t *testing.T) {
				time.Sleep(time.Until(released.Add(window + time.Minute)))
				client, err := demandrepair.NewGitHub(server.URL, job.repo.Host, "fixture-ref",
					func(context.Context, string) (string, error) { return "fixture-only", nil },
					&tls.Config{RootCAs: roots}, []demandrepair.Repository{job.repo})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				observation, err := client.ObserveExactJob(t.Context(), job.repo, job.id)
				r := observation.Record()
				if tc.start != 0 && (err != nil || !observation.Valid() || !r.StartedAt.Equal(released.Add(start))) {
					t.Fatalf("timing boundary needs a valid exact-job observation: %+v %v", r, err)
				}
				got := err == nil && observation.Valid() && r.Repository == job.repo && r.RunID == job.run &&
					r.RunAttempt == job.attempt && r.JobID == job.id && r.RunnerID == expectedRunner && r.RunnerName == runnerName &&
					r.Status == "in_progress" && !r.StartedAt.Before(released) && !r.StartedAt.After(released.Add(window))
				if got != tc.want {
					t.Fatalf("recovery=%v, want %v; observation=%+v err=%v", got, tc.want, r, err)
				}
			})
		})
	}
}
