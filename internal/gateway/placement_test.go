package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

func multiHostFacade(t *testing.T) (*Facade, *admission.SQLiteStore, map[string]*backendFixture, map[string]PoolPolicy) {
	t.Helper()
	ctx := context.Background()
	global := admission.Resources{CPU: 2000, RAM: 2048, Runners: 2}
	hostBudget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	config := admission.Config{Global: global, MaxObservationAge: time.Minute, Hosts: map[string]admission.Host{}, Pools: map[string]admission.Pool{"pool-a": {Budget: global, Hosts: []string{"host-a", "host-b"}}}}
	backends := map[string]*backendFixture{}
	sources := map[string]Backend{}
	for _, id := range []string{"host-a", "host-b"} {
		config.Hosts[id] = admission.Host{Budget: hostBudget, Mode: "enabled", ObservedAt: time.Now()}
		identity := testsupport.Identity(t, "multi-"+id)
		b := &backendFixture{identity: identity.Client, instances: map[string]incusapi.Instance{}}
		backends[id] = b
		sources[id] = b
	}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policies := map[string]PoolPolicy{"pool-a": {Hosts: []string{"host-b", "host-a"}, Strategy: "first_fit", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}}
	f, err := New(ctx, store, "controller-a", policies, sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(ctx, garmRows{}); err != nil {
		t.Fatal(err)
	}
	return f, store, backends, policies
}
func TestMultiHostFacadeAtomicPlacementAndReconcileReadsPersistedHosts(t *testing.T) {
	f, store, backends, _ := multiHostFacade(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []string{}
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("multi-%d", i)
			exit, _, err := f.Admit(ctx, garmprotocol.Request{Protocol: garmprotocol.Version, Name: name, PoolID: "pool-a"})
			if err != nil {
				t.Errorf("admit %v", err)
			} else if exit == 0 {
				mu.Lock()
				accepted = append(accepted, name)
				mu.Unlock()
			} else if exit != 75 {
				t.Errorf("exit %d", exit)
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 2 {
		t.Fatalf("accepted %v", accepted)
	}
	rows := garmRows{}
	for _, name := range accepted {
		if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Get(ctx, name, "pool-a"); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, GarmInstance{Name: name, ProviderID: name, PoolID: "pool-a", State: "running", Idle: true})
	}
	if backends["host-a"].calls.Load() != 1 || backends["host-b"].calls.Load() != 1 {
		t.Fatal("physical skew")
	}
	list, err := f.List(ctx, "pool-a")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %d %v", len(list), err)
	}
	if err := f.Reconcile(ctx, rows); err != nil {
		t.Fatal(err)
	}
	state, err := store.ReadSnapshot(ctx)
	if err != nil || state.Allocated.Global.CPU != 2000 || len(state.Inventory) != 2 {
		t.Fatalf("reconciled %+v %v", state.Allocated, err)
	}
}
func TestMultiHostConfigChangeCannotReinterpretReservedExecution(t *testing.T) {
	f, store, backends, policies := multiHostFacade(t)
	ctx := context.Background()
	preflight(t, f, "old-plan", "pool-a")
	p := policies["pool-a"]
	p.Hosts = []string{"host-b"}
	p.CPU = 2000
	p.RAM = 2048
	p.Flavor = "new-flavor"
	p.Image = "new-image"
	p.Fingerprint = strings.Repeat("b", 64)
	p.Profiles = []string{"new-profile"}
	f2, err := New(ctx, store, "controller-a", map[string]PoolPolicy{"pool-a": p}, map[string]Backend{"host-a": backends["host-a"], "host-b": backends["host-b"]})
	if err != nil {
		t.Fatal(err)
	}
	if err := f2.Reconcile(ctx, garmRows{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.Create(ctx, bootstrap("old-plan", "pool-a")); err == nil {
		t.Fatal("revoked old plan launched")
	}
	if backends["host-a"].calls.Load() != 0 || backends["host-b"].calls.Load() != 0 {
		t.Fatal("revoked policy dispatched")
	}
	original, err := store.LookupExecutionPlan(ctx, key("controller-a", "pool-a", "old-plan"))
	if err != nil || original.Request.Host != "host-a" || original.Execution.Fingerprint != strings.Repeat("a", 64) {
		t.Fatal("rewrote original plan")
	}
	// The original still-authorized process may execute only its own unchanged
	// policy; a deployment revocation must ALSO fence that old process.
	if _, err := f.Create(ctx, bootstrap("old-plan", "pool-a")); err != nil {
		t.Fatal(err)
	}
	got := backends["host-a"].lastSpec
	if got.ImageFingerprint != strings.Repeat("a", 64) || len(got.Profiles) != 1 || got.Profiles[0] != "runner-base" || got.CPUMillis != 1000 || backends["host-b"].calls.Load() != 0 {
		t.Fatalf("reinterpreted %+v", got)
	}

	if err := f2.Reconcile(ctx, garmRows{{Name: "old-plan", ProviderID: "old-plan", PoolID: "pool-a", State: "running", Idle: true}}); err != nil {
		t.Fatalf("forgot old host %v", err)
	}
	list, err := f2.List(ctx, "pool-a")
	if err != nil || len(list) != 1 {
		t.Fatal("eligibility change hid runner")
	}
}
func TestMultiHostAmbiguousCreateNeverFailsOverToNewConfiguration(t *testing.T) {
	f, store, backends, policies := multiHostFacade(t)
	ctx := context.Background()
	preflight(t, f, "ambiguous", "pool-a")
	backends["host-a"].createFail = true
	if _, err := f.Create(ctx, bootstrap("ambiguous", "pool-a")); err == nil {
		t.Fatal("lost response succeeded")
	}
	p := policies["pool-a"]
	p.Hosts = []string{"host-b"}
	f2, err := New(ctx, store, "controller-a", map[string]PoolPolicy{"pool-a": p}, map[string]Backend{"host-a": backends["host-a"], "host-b": backends["host-b"]})
	if err != nil {
		t.Fatal(err)
	}
	preflight(t, f2, "ambiguous", "pool-a")
	if _, err := f2.Create(ctx, bootstrap("ambiguous", "pool-a")); err == nil {
		t.Fatal("unknown replay promoted")
	}
	if backends["host-a"].calls.Load() != 1 || backends["host-b"].calls.Load() != 0 {
		t.Fatal("redispatch/failover")
	}
	plan, err := store.LookupExecutionPlan(ctx, key("controller-a", "pool-a", "ambiguous"))
	if err != nil || plan.Request.Host != "host-a" {
		t.Fatal("replaced plan")
	}
}
func TestMultiHostStartupRejectsAmbiguousOrOutOfScopeCandidates(t *testing.T) {
	_, store, backends, policies := multiHostFacade(t)
	sources := map[string]Backend{"host-a": backends["host-a"], "host-b": backends["host-b"]}
	for _, kind := range []string{"duplicate", "unknown", "both", "strategy"} {
		t.Run(kind, func(t *testing.T) {
			p := policies["pool-a"]
			switch kind {
			case "duplicate":
				p.Hosts = []string{"host-a", "host-a"}
			case "unknown":
				p.Hosts = []string{"unenrolled"}
			case "both":
				p.Host = "host-a"
			case "strategy":
				p.Strategy = "random-unbounded"
			}
			if _, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": p}, sources); err == nil {
				t.Fatal("accepted untrusted candidate policy")
			}
		})
	}
}

func TestMultiHostStartupRevocationRefusesEveryChangedExecutionDimension(t *testing.T) {
	for _, kind := range []string{"fingerprint", "profiles", "type", "arch", "cpu", "ram", "flavor", "image", "host"} {
		t.Run(kind, func(t *testing.T) {
			f, store, backends, policies := multiHostFacade(t)
			ctx := context.Background()
			preflight(t, f, "revoke", "pool-a")
			p := policies["pool-a"]
			switch kind {
			case "fingerprint":
				p.Fingerprint = strings.Repeat("b", 64)
			case "profiles":
				p.Profiles = []string{"revoked-profile"}
			case "type":
				p.Type = "virtual-machine"
			case "arch":
				p.Arch = common.Arm64
			case "cpu":
				p.CPU = 2000
			case "ram":
				p.RAM = 2048
			case "flavor":
				p.Flavor = "changed"
			case "image":
				p.Image = "changed"
			case "host":
				p.Hosts = []string{"host-b"}
			}
			fresh, err := New(ctx, store, "controller-a", map[string]PoolPolicy{"pool-a": p}, map[string]Backend{"host-a": backends["host-a"], "host-b": backends["host-b"]})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fresh.Create(ctx, bootstrap("revoke", "pool-a")); err == nil {
				t.Fatal("revoked policy created")
			}
			if backends["host-a"].calls.Load() != 0 || backends["host-b"].calls.Load() != 0 {
				t.Fatal("revoked policy POST")
			}
			if _, err := store.LookupDispatch(ctx, key("controller-a", "pool-a", "revoke")); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("revoked policy created intent %v", err)
			}
			plan, err := store.LookupExecutionPlan(ctx, key("controller-a", "pool-a", "revoke"))
			if err != nil || plan.Request.Host != "host-a" || plan.Request.Resources.CPU != 1000 || plan.Execution.Fingerprint != strings.Repeat("a", 64) {
				t.Fatal("revocation rewrote history")
			}
		})
	}
}

func TestMultiHostActualLedgerEligibilityEditKeepsExistingHostVisible(t *testing.T) {
	f, source, backends, policies := multiHostFacade(t)
	ctx := context.Background()
	preflight(t, f, "visible-a", "pool-a")
	if _, err := f.Create(ctx, bootstrap("visible-a", "pool-a")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "offline-activation.db")
	if err := source.BackupSQLite(ctx, path); err != nil {
		t.Fatal(err)
	}
	state, err := source.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	config := state.Config
	pool := config.Pools["pool-a"]
	pool.Hosts = []string{"host-b"}
	config.Pools["pool-a"] = pool
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// This is a trusted OFFLINE policy activation fixture, not an enabled API.
	// The copied artifact remains permanently runtime-disabled throughout.
	if _, err := db.Exec("UPDATE metadata SET config=?,ready=0,nonce=''", raw); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := admission.OpenSQLite(path, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p := policies["pool-a"]
	p.Hosts = []string{"host-b"}
	updated, err := New(ctx, store, "controller-a", map[string]PoolPolicy{"pool-a": p}, map[string]Backend{"host-a": backends["host-a"], "host-b": backends["host-b"]})
	if err != nil {
		t.Fatal(err)
	}
	rows := garmRows{{Name: "visible-a", ProviderID: "visible-a", PoolID: "pool-a", State: "running", Idle: true}}
	if err := updated.Reconcile(ctx, rows); err != nil {
		t.Fatalf("historical host hidden %v", err)
	}
	list, err := updated.List(ctx, "pool-a")
	if err != nil || len(list) != 1 || list[0].Name != "visible-a" {
		t.Fatalf("historical list %v %v", list, err)
	}
	now, err := store.ReadSnapshot(ctx)
	if err != nil || !now.Ready || now.Allocated.Hosts["host-a"].CPU != 1000 {
		t.Fatal("accounting scope confused with eligibility")
	}
	preflight(t, updated, "new-b", "pool-a")
	if _, err := updated.Create(ctx, bootstrap("new-b", "pool-a")); err != nil {
		t.Fatal(err)
	}
	if backends["host-a"].calls.Load() != 1 || backends["host-b"].calls.Load() != 1 {
		t.Fatal("new eligibility ignored")
	}
}
