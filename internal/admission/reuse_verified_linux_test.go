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
	"testing"
	"time"

	common "github.com/cloudbase/garm-provider-common/params"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

// reuseHarness serves one local observation endpoint pair for several stock
// runners: the exact stock metadata credential read and the scoped GitHub job
// reads. Runners are distinguished by their metadata bearer token and jobs by
// their exact path; nothing here leaves the test process.
type reuseHarness struct {
	server        *httptest.Server
	trust         *tls.Config
	registrations map[string]demandrepair.Repository
	agentIDs      map[string]int64
	jobs          map[string]map[string]any
}

func newReuseHarness(t *testing.T) *reuseHarness {
	t.Helper()
	h := &reuseHarness{registrations: map[string]demandrepair.Repository{}, agentIDs: map[string]int64{}, jobs: map[string]map[string]any{}}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/metadata/credentials/runner" {
			name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer metadata-token-")
			repo, ok := h.registrations[name]
			if !ok {
				http.Error(w, "unknown runner", http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"agentId":%d,"agentName":%q,"gitHubUrl":%q,"ephemeral":true}`, h.agentIDs[name], name, "https://"+repo.Host+"/"+repo.Owner+"/"+repo.Name)
			return
		}
		for _, job := range h.jobs {
			if job["path"] == r.URL.Path {
				if r.Header.Get("Authorization") != "Bearer github-token" {
					http.Error(w, "wrong token", http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(job["body"])
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(h.server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(h.server.Certificate())
	h.trust = &tls.Config{RootCAs: roots}
	return h
}

func (h *reuseHarness) addJob(path string, body map[string]any) {
	h.jobs[path] = map[string]any{"path": path, "body": body}
}

func (h *reuseHarness) jobBody(path string, jobID, runID, runnerID int64, runnerName, owner, repoName string, completed bool) {
	now := time.Now().UTC()
	status := "completed"
	var conclusion any = "success"
	var completedAt any = now.Add(-time.Second)
	if !completed {
		status = "in_progress"
		conclusion = nil
		completedAt = nil
	}
	h.addJob(path, map[string]any{"id": jobID, "run_id": runID, "run_attempt": 1, "runner_id": runnerID, "runner_name": runnerName, "url": h.server.URL + path, "run_url": h.server.URL + "/repos/" + owner + "/" + repoName + "/actions/runs/" + fmt.Sprint(runID), "status": status, "conclusion": conclusion, "started_at": now.Add(-time.Minute), "completed_at": completedAt})
}

// reuseFixture is one charged stock runner with durable create evidence, a
// retirement intent and authenticated observation endpoints.
type reuseFixture struct {
	harness    *reuseHarness
	store      *SQLiteStore
	fake       *testsupport.FakeIncus
	request    Request
	controller string
	pool       string
	name       string
	creation   CreateEvidence
	stock      reuse.StockBinding
	repo       demandrepair.Repository
}

func reuseRunnerKey(controller, pool, name string) string {
	return evidenceDigest([]byte(controller + "\x00" + pool + "\x00" + name))
}

func reuseRepo(name, repoName string) demandrepair.Repository {
	return demandrepair.Repository{GarmID: "garm-" + name, Host: "github.example", Owner: "owner", Name: repoName}
}

func newReuseFixture(t *testing.T, harness *reuseHarness, controller, pool, name string, repo demandrepair.Repository) *reuseFixture {
	t.Helper()
	return newReuseFixtureAgent(t, harness, controller, pool, name, 17, repo)
}

func reuseFixtureConfig() Config {
	config := fixture(time.Now())
	delete(config.Hosts, "b")
	for id, p := range config.Pools {
		p.Hosts = []string{"a"}
		config.Pools[id] = p
	}
	return config
}

// reuseStoreFixture opens one ledger bound to one fake daemon with an empty
// completed inventory, ready for provisioning several runners.
func reuseStoreFixture(t *testing.T, config Config) (*SQLiteStore, *testsupport.FakeIncus) {
	t.Helper()
	ctx := context.Background()
	store := sqliteFixtureStore(t, config, false)
	fake := testsupport.NewFakeIncus(t, "reuse-daemon")
	physical, err := fake.Client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindHostIdentities(ctx, fence, []HostBinding{{Host: "a", ServerKeyFingerprint: physical.ServerKeyFingerprint(), OriginFingerprint: physical.OriginFingerprint(), ScopeFingerprint: physical.ScopeFingerprint(), ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(), ObservedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(config, time.Now())}); err != nil {
		t.Fatal(err)
	}
	return store, fake
}

func newReuseFixtureAgent(t *testing.T, harness *reuseHarness, controller, pool, name string, agentID int64, repo demandrepair.Repository) *reuseFixture {
	t.Helper()
	store, fake := reuseStoreFixture(t, reuseFixtureConfig())
	f := newReuseRunner(t, harness, store, fake, controller, pool, name, agentID, repo)
	reconcileSQLite(t, store, []InventoryResource{f.inventoryRow()})
	return f
}

// newReuseRunner registers one stock runner with the observation harness and
// provisions its full charge: admission, dispatch, provider create, durable
// create evidence and the retirement intent, on a shared ledger and daemon.
func newReuseRunner(t *testing.T, harness *reuseHarness, store *SQLiteStore, fake *testsupport.FakeIncus, controller, pool, name string, agentID int64, repo demandrepair.Repository) *reuseFixture {
	t.Helper()
	harness.registrations[name] = repo
	harness.agentIDs[name] = agentID
	f := &reuseFixture{harness: harness, store: store, fake: fake, controller: controller, pool: pool, name: name, repo: repo,
		request: Request{Key: reuseRunnerKey(controller, pool, name), Pool: pool, Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Disk: 4096, Runners: 1}}}
	f.provision(t)
	return f
}

func (f *reuseFixture) inventoryRow() InventoryResource {
	return InventoryResource{ID: f.name, Host: "a", Pool: f.pool, ReservationKey: f.request.Key, Resources: f.request.Resources, State: "idle"}
}

// provision charges one runner end to end. It never reconciles inventory;
// the caller supplies the complete bounded inventory afterwards.
func (f *reuseFixture) provision(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.Admit(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	binding, err := f.store.LookupHostBinding(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	permit, err := f.store.ClaimDispatchBound(ctx, f.request, binding)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.fake.Client.Create(ctx, incusapi.InstanceSpec{Name: f.name, Type: "container", Profiles: []string{"runner-base"}, ImageFingerprint: strings.Repeat("a", 64), CPUMillis: 1000, RAMMiB: 2048, UserData: "#cloud-config\nfixture-userdata", ControllerID: f.controller, PoolID: f.pool, ReservationKey: f.request.Key})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordDispatchOutcome(ctx, *permit, DispatchReceipt{State: "confirmed", OperationID: created.OperationID, ResourceID: created.ResourceID}); err != nil {
		t.Fatal(err)
	}
	subject := f.mustObserveIdentity(t)
	createProof, err := f.fake.Client.CaptureTerminalOperation(ctx, subject, f.fake.CreateOperationID(), incusapi.RecoveryCreate)
	if err != nil {
		t.Fatal(err)
	}
	f.creation, err = f.store.StoreCreateObservation(ctx, f.request, createProof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestRetirement(ctx, f.request); err != nil {
		t.Fatal(err)
	}
}

func (f *reuseFixture) identityExpectation() incusapi.IdentityExpectation {
	return incusapi.IdentityExpectation{Name: f.name, ControllerID: f.controller, PoolID: f.pool, ReservationKey: f.request.Key, CPUMillis: 1000, RAMMiB: 2048}
}

func (f *reuseFixture) mustObserveIdentity(t *testing.T) incusapi.VerifiedInstanceIdentity {
	t.Helper()
	identity, err := f.fake.Client.ObserveInstanceIdentity(context.Background(), f.identityExpectation())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

// observeStockRegistration performs the authenticated stock JIT registration
// read for this fixture's runner.
func (f *reuseFixture) observeStockRegistration(t *testing.T) stockjit.Observation {
	t.Helper()
	subject := f.creation.Observation.Identity.Subject
	metadata, err := stockjit.New(stockjit.Policy{MetadataURL: f.harness.server.URL + "/api/v1/metadata", ControllerID: subject.ControllerID, PoolID: subject.PoolID, GitHubURL: "https://" + f.repo.Host + "/" + f.repo.Owner + "/" + f.repo.Name}, f.harness.trust)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(metadata.Close)
	observation, err := metadata.Observe(context.Background(), subject.ControllerID, common.BootstrapInstance{Name: subject.Name, PoolID: subject.PoolID, MetadataURL: f.harness.server.URL + "/api/v1/metadata", RepoURL: "https://" + f.repo.Host + "/" + f.repo.Owner + "/" + f.repo.Name, InstanceToken: "metadata-token-" + subject.Name, JitConfigEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

// buildCharge binds the durable creation evidence and the authenticated
// registration into the anchored charge.
func (f *reuseFixture) buildCharge(t *testing.T, registration stockjit.Observation) reuse.ChargeIdentity {
	t.Helper()
	stock, err := registration.Record().ReuseBinding()
	if err != nil {
		t.Fatal(err)
	}
	f.stock = stock
	subject := f.creation.Observation.Identity.Subject
	volumes := []reuse.VolumeCharge{{Name: subject.Name, BindingDigest: reuse.VolumeBindingDigest(subject.ScopeFingerprint, subject.Name)}}
	charge, err := ChargeFromCreation(f.request, f.creation, stock, volumes)
	if err != nil {
		t.Fatal(err)
	}
	return charge
}

// observeLifecycle reads one exact job through the scoped demand client.
func (f *reuseFixture) observeLifecycle(t *testing.T, jobID int64) demandrepair.JobLifecycleObservation {
	t.Helper()
	client, err := demandrepair.NewGitHub(f.harness.server.URL, f.repo.Host, "private", func(context.Context, string) (string, error) { return "github-token", nil }, f.harness.trust, []demandrepair.Repository{f.repo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	observation, err := client.ObserveJobLifecycle(context.Background(), f.repo, jobID, f.observeStockRegistration(t))
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

// workloadProof converts a completed lifecycle observation into the reuse
// workload proof. Queued or in-progress assignments fail conversion.
func (f *reuseFixture) workloadProof(t *testing.T, observation demandrepair.JobLifecycleObservation) *reuse.WorkloadProof {
	t.Helper()
	proof, err := observation.ReuseWorkloadProof(f.stock)
	if err != nil {
		t.Fatal(err)
	}
	return &proof
}

// teardownProof deletes the runner at the provider, captures the original
// terminal DELETE operation and observes the two-pass post-delete absence.
// The fake daemon stands in for the stock provider; controller code never
// deletes runners in production flows.
func (f *reuseFixture) teardownProof(t *testing.T) *reuse.InstanceTeardownProof {
	t.Helper()
	ctx := context.Background()
	identity := f.mustObserveIdentity(t)
	deletion, err := f.fake.Client.Delete(ctx, f.name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.fake.Client.WaitDeletion(ctx, deletion); err != nil {
		t.Fatal(err)
	}
	deleteProof, err := f.fake.Client.CaptureTerminalOperation(ctx, identity, f.fake.DeleteOperationID(), incusapi.RecoveryDelete)
	if err != nil {
		t.Fatal(err)
	}
	absence, err := f.fake.Client.ObserveProjectAbsence(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := reuse.TeardownProofFromObservations(identity.Observation(), deleteProof, absence)
	if err != nil {
		t.Fatal(err)
	}
	return &proof
}

func (f *reuseFixture) cleanupProof() *reuse.CleanupProof {
	return &reuse.CleanupProof{Volumes: []reuse.VolumeAbsence{{Name: f.creation.Observation.Identity.Subject.Name, ScopeDigest: f.creation.Observation.Identity.Subject.ScopeFingerprint, ObservedAt: time.Now().UTC()}}}
}

func (f *reuseFixture) evidence(charge reuse.ChargeIdentity, workload *reuse.WorkloadProof, teardown *reuse.InstanceTeardownProof, cleanup *reuse.CleanupProof) reuse.TerminalEvidence {
	return reuse.TerminalEvidence{Charge: charge, Workload: workload, Teardown: teardown, Cleanup: cleanup, CapturedAt: time.Now().UTC()}
}

func (f *reuseFixture) chargedRunners(t *testing.T) int64 {
	t.Helper()
	return sqliteSnapshot(t, f.store).Allocated.Global.Runners
}

func TestReleaseVerifiedFullTeardownReleasesOnceAcrossDuplicateAndRestart(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	evidence := f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())
	decision, err := f.store.ReleaseVerified(ctx, f.request, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Replay || !decision.Releasable.All() || decision.Remaining != (Resources{}) {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	after := sqliteSnapshot(t, f.store)
	if after.Allocated.Global.Runners != 0 || after.Allocated.Global.CPU != 0 || len(after.Inventory) != 0 {
		t.Fatalf("release changed accounting: %+v", after.Allocated.Global)
	}
	if _, err := f.store.LookupRelease(ctx, f.request.Key); err != nil {
		t.Fatal(err)
	}
	// Duplicate delivery replays the immutable decision without re-releasing.
	for i := 0; i < 2; i++ {
		again, err := f.store.ReleaseVerified(ctx, f.request, evidence)
		if err != nil || !again.Replay {
			t.Fatalf("duplicate delivery not replayed: %+v %v", again, err)
		}
	}
	// Restart replays the tombstone instead of releasing again.
	reopened, err := OpenSQLite(f.store.path, after.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.ReleaseVerified(ctx, f.request, evidence)
	if err != nil || !replay.Replay {
		t.Fatalf("restart released again: %+v %v", replay, err)
	}
	if got := sqliteSnapshot(t, reopened); got.Allocated.Global.Runners != 0 {
		t.Fatal("restart lost the release")
	}
	// A different decision for the released identity stays refused.
	if _, err := reopened.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, nil, nil)); !errors.Is(err, ErrDenied) {
		t.Fatalf("different evidence after release accepted: %v", err)
	}
}

func TestReleaseVerifiedRetainedCases(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	h.jobBody("/repos/owner/repo/actions/jobs/92", 92, 13, 17, "runner-1", "owner", "repo", false)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	assertCharged := func() {
		t.Helper()
		after := sqliteSnapshot(t, f.store)
		if after.Allocated.Global.Runners != 1 || len(after.Inventory) != 1 || after.Reservations[f.request.Key].State != "deleting" {
			t.Fatalf("held case changed accounting: %+v", after.Allocated.Global)
		}
	}
	// Same-name replacement: a new incarnation under the same display name is
	// a distinct charge; its evidence never releases the original charge.
	f.fake.SetIncarnationDrift(true)
	drifted := f.mustObserveIdentity(t)
	replacementCharge := charge
	replacementCharge.Incarnation.Generation = drifted.Observation().Subject.Generation
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(replacementCharge, nil, nil, nil)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("same-name replacement released: %v", err)
	}
	assertCharged()
	// Conflicting workload evidence (a second job after completion) blocks
	// release deterministically even when every other component validates.
	second := *f.workloadProof(t, f.observeLifecycle(t, 91))
	second.JobID = 92
	second.RunID = 13
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, &second, f.teardownProof(t), f.cleanupProof())); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("conflicting evidence released: %v", err)
	}
	assertCharged()
	// Delayed teardown: the captured original DELETE operation is missing, so
	// the unproven compute dimensions stay charged. Only the positively
	// observed absent volume frees disk; no release tombstone is written.
	delayedEvidence := f.evidence(charge, f.workloadProof(t, f.observeLifecycle(t, 91)), nil, f.cleanupProof())
	delayed, err := f.store.ReleaseVerified(ctx, f.request, delayedEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if delayed.Releasable.CPU || delayed.Releasable.RAM || delayed.Releasable.Runners || !delayed.Releasable.Disk {
		t.Fatalf("delayed teardown released unproven dimensions: %+v", delayed)
	}
	if f.chargedRunners(t) != 1 {
		t.Fatalf("delayed teardown released the runner: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
	if _, err := f.store.LookupRelease(ctx, f.request.Key); err == nil {
		t.Fatal("delayed teardown wrote a release tombstone")
	}
	// Still-assigned runner: the in-progress job observation cannot mint a
	// completed workload proof at all.
	if _, err := f.observeLifecycle(t, 92).ReuseWorkloadProof(f.stock); err == nil {
		t.Fatal("in-progress lifecycle minted a completed proof")
	}
	// A component-less bundle never validates and never changes accounting.
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, nil, nil, nil)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("component-less bundle released: %v", err)
	}
	if f.chargedRunners(t) != 1 {
		t.Fatalf("component-less bundle changed accounting: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
}

func TestReleaseVerifiedResidualVolumeKeepsDiskCharged(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	// The applicable volume is still present: no cleanup proof is delivered.
	evidence := f.evidence(charge, workload, f.teardownProof(t), nil)
	decision, err := f.store.ReleaseVerified(ctx, f.request, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Releasable.Disk || !decision.Releasable.CPU || !decision.Releasable.RAM || !decision.Releasable.Runners {
		t.Fatalf("residual volume freed disk: %+v", decision)
	}
	if decision.Remaining != (Resources{CPU: 0, RAM: 0, Disk: 4096, Runners: 0}) {
		t.Fatalf("remaining charge wrong: %+v", decision.Remaining)
	}
	after := sqliteSnapshot(t, f.store)
	if after.Allocated.Global.Disk != 4096 || after.Allocated.Global.Runners != 0 || after.Allocated.Global.CPU != 0 {
		t.Fatalf("partial reuse accounting wrong: %+v", after.Allocated.Global)
	}
	// The verified dimensions fund growth; the residual disk stays charged.
	growth := Request{Key: reuseRunnerKey("controller-1", "app", "runner-9"), Pool: "app", Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Runners: 1}}
	if _, err := f.store.Admit(ctx, growth); err != nil {
		t.Fatal(err)
	}
	// Duplicate delivery of the partial decision replays it exactly once.
	again, err := f.store.ReleaseVerified(ctx, f.request, evidence)
	if err != nil || !again.Replay {
		t.Fatalf("partial duplicate not replayed: %+v %v", again, err)
	}
}

func TestReleaseVerifiedCompetingRepositories(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	h.jobBody("/repos/owner/competitor/actions/jobs/93", 93, 14, 18, "runner-2", "owner", "competitor", true)
	repoA := reuseRepo("a", "repo")
	repoB := reuseRepo("b", "competitor")
	fA := newReuseFixture(t, h, "controller-1", "app", "runner-1", repoA)
	fB := newReuseFixtureAgent(t, h, "controller-1", "app", "runner-2", 18, repoB)
	chargeA := fA.buildCharge(t, fA.observeStockRegistration(t))
	chargeB := fB.buildCharge(t, fB.observeStockRegistration(t))
	if _, err := fA.store.MarkDeleting(ctx, fA.request); err != nil {
		t.Fatal(err)
	}
	if _, err := fB.store.MarkDeleting(ctx, fB.request); err != nil {
		t.Fatal(err)
	}
	// Cross-wired evidence never releases: repo A's workload bound to repo B's
	// charge fails matching, and repo B stays charged throughout. B's teardown
	// proof is captured once and delivered twice: duplicate components must not
	// change the outcome.
	workloadA := fA.workloadProof(t, fA.observeLifecycle(t, 91))
	teardownB := fB.teardownProof(t)
	cleanupB := fB.cleanupProof()
	if _, err := fB.store.ReleaseVerified(ctx, fB.request, fB.evidence(chargeB, workloadA, teardownB, cleanupB)); !errors.Is(err, ErrReuseEvidence) {
		t.Fatalf("cross-wired evidence released: %v", err)
	}
	if fB.chargedRunners(t) != 1 {
		t.Fatalf("cross-wired release changed B: %+v", sqliteSnapshot(t, fB.store).Allocated.Global)
	}
	// Correctly scoped evidence for A releases exactly A.
	if _, err := fA.store.ReleaseVerified(ctx, fA.request, fA.evidence(chargeA, workloadA, fA.teardownProof(t), fA.cleanupProof())); err != nil {
		t.Fatal(err)
	}
	if fA.chargedRunners(t) != 0 {
		t.Fatalf("repository A charge not released: %+v", sqliteSnapshot(t, fA.store).Allocated.Global)
	}
	if _, err := fB.store.LookupRelease(ctx, fB.request.Key); err == nil {
		t.Fatal("foreign charge released")
	}
	// Repo B's own qualified evidence releases B exactly once.
	workloadB := fB.workloadProof(t, fB.observeLifecycle(t, 93))
	if _, err := fB.store.ReleaseVerified(ctx, fB.request, fB.evidence(chargeB, workloadB, teardownB, cleanupB)); err != nil {
		t.Fatal(err)
	}
	if fB.chargedRunners(t) != 0 {
		t.Fatalf("both repositories not released: %+v", sqliteSnapshot(t, fB.store).Allocated.Global)
	}
}

func TestReleaseVerifiedReductionAloneDoesNotFundGrowth(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	// A lower pool limit alone never funds growth: the charge stays allocated.
	budget := Resources{CPU: 1000, RAM: 2048, Runners: 1}
	if err := f.store.ApplyPolicyChange(ctx, PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, f.store).Config), Action: "pool_budget", Target: "app", Budget: &budget, Reason: "lower stock pool maximum"}); err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, f.store, []InventoryResource{{ID: "runner-1", Host: "a", Pool: "app", ReservationKey: f.request.Key, Resources: f.request.Resources, State: "idle"}})
	growth := Request{Key: reuseRunnerKey("controller-1", "app", "runner-9"), Pool: "app", Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Runners: 1}}
	if _, err := f.store.Admit(ctx, growth); !errors.Is(err, ErrDenied) {
		t.Fatalf("reduction alone funded growth: %v", err)
	}
	if f.chargedRunners(t) != 1 {
		t.Fatalf("reduction changed charges: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
	// After the policy change the barrier is fresh again; qualified delayed
	// evidence still releases exactly once.
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())); err != nil {
		t.Fatal(err)
	}
	if f.chargedRunners(t) != 0 {
		t.Fatalf("qualified evidence did not release after policy change: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
}

func TestReleaseVerifiedRequiresReadyReconciliation(t *testing.T) {
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	if _, err := f.store.MarkDeleting(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	// An incomplete inventory barrier: admission is blocked, and blocked
	// readiness can never convert an unknown into free capacity.
	if _, err := f.store.BeginReconciliation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReleaseVerified(context.Background(), f.request, f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("incomplete inventory released: %v", err)
	}
	if f.chargedRunners(t) != 1 {
		t.Fatalf("blocked release changed accounting: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
}

func TestReleaseVerifiedRefusesWithoutDeletingMark(t *testing.T) {
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	if _, err := f.store.ReleaseVerified(context.Background(), f.request, f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())); !errors.Is(err, ErrDenied) {
		t.Fatalf("release without deleting mark: %v", err)
	}
	if f.chargedRunners(t) != 1 {
		t.Fatalf("unmarked release changed accounting: %+v", sqliteSnapshot(t, f.store).Allocated.Global)
	}
}
