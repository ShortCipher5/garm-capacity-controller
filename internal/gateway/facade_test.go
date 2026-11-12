package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	common "github.com/cloudbase/garm-provider-common/params"
)

const operation = "12345678-1234-1234-1234-123456789abc"

type backendFixture struct {
	mu         sync.Mutex
	instances  map[string]incusapi.Instance
	operations []incusapi.Operation
	calls      atomic.Int32
	fail       bool
	drift      bool
	readFail   bool
	userdata   string
}

func (b *backendFixture) CreateAndWait(_ context.Context, s incusapi.InstanceSpec) (incusapi.Created, error) {
	b.calls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.userdata = s.UserData
	instance := incusapi.Instance{Name: s.Name, Status: 103, CPUMillis: s.CPUMillis, RAMMiB: s.RAMMiB, ControllerID: s.ControllerID, PoolID: s.PoolID, ReservationKey: s.ReservationKey}
	if b.drift {
		instance.RAMMiB++
	}
	b.instances[s.Name] = instance
	receipt := incusapi.Created{OperationID: operation, ResourceID: s.Name}
	if b.fail {
		return receipt, errors.New("private-bootstrap-fault")
	}
	return receipt, nil
}
func (b *backendFixture) Instance(_ context.Context, name string) (incusapi.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.instances[name]
	if !ok || b.readFail {
		return incusapi.Instance{}, errors.New("private-remote-fault")
	}
	return v, nil
}
func (b *backendFixture) Instances(context.Context) ([]incusapi.Instance, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.readFail {
		return nil, ErrUnavailable
	}
	result := []incusapi.Instance{}
	for _, v := range b.instances {
		result = append(result, v)
	}
	return result, nil
}
func (b *backendFixture) Operations(context.Context) ([]incusapi.Operation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.readFail {
		return nil, ErrUnavailable
	}
	return append([]incusapi.Operation(nil), b.operations...), nil
}

type garmRows []GarmInstance

func (r garmRows) Instances(context.Context) ([]GarmInstance, error) {
	return append([]GarmInstance(nil), r...), nil
}

func facadeFixture(t *testing.T) (*Facade, *admission.SQLiteStore, *backendFixture, string) {
	t.Helper()
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}, "pool-b": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute}
	path := filepath.Join(t.TempDir(), "ledger.db")
	store, err := admission.OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	backend := &backendFixture{instances: map[string]incusapi.Instance{}}
	policy := PoolPolicy{Host: "host-a", CPU: 1000, RAM: 1024, Flavor: "bounded-small", Image: "pinned-linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"runner-base"}, Type: "container", Arch: common.Amd64}
	f, err := New(context.Background(), store, "controller-a", map[string]PoolPolicy{"pool-a": policy, "pool-b": policy}, map[string]Backend{"host-a": backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	return f, store, backend, path
}
func bootstrap(name, pool string) common.BootstrapInstance {
	osType := "linux"
	arch := "x64"
	download := "https://example.invalid/disposable-runner.tar.gz"
	filename := "actions-runner-linux-x64.tar.gz"
	return common.BootstrapInstance{Name: name, PoolID: pool, OSType: common.Linux, OSArch: common.Amd64, Flavor: "bounded-small", Image: "pinned-linux", RepoURL: "https://github.com/example/disposable", CallbackURL: "https://example.invalid/callback", MetadataURL: "https://example.invalid/metadata", InstanceToken: "bootstrap-secret-must-not-persist", ExtraSpecs: []byte("{}"), JitConfigEnabled: true, Tools: []common.RunnerApplicationDownload{{OS: &osType, Architecture: &arch, DownloadURL: &download, Filename: &filename}}}
}
func preflight(t *testing.T, f *Facade, name, pool string) {
	t.Helper()
	request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: name, PoolID: pool}
	exit, data, err := f.Admit(context.Background(), request)
	if err != nil || exit != 0 {
		t.Fatalf("preflight %d %s %v", exit, data, err)
	}
	if _, err := garmprotocol.Parse(exit, data, request); err != nil {
		t.Fatal(err)
	}
}

func TestFacadeExactPolicyAdmissionCreateReplayAndSecrets(t *testing.T) {
	f, store, b, path := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	if b.calls.Load() != 0 {
		t.Fatal("preflight dispatched")
	}
	result, err := f.Create(context.Background(), bootstrap("runner-1", "pool-a"))
	if err != nil || result.Name != "runner-1" || result.Status != common.InstanceRunning {
		t.Fatalf("create %+v %v", result, err)
	}
	if !strings.Contains(b.userdata, "#cloud-config") {
		t.Fatal("official cloud-init rendering missing")
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = f.Create(context.Background(), bootstrap("runner-1", "pool-a")) }()
	}
	wg.Wait()
	if b.calls.Load() != 1 {
		t.Fatalf("duplicate dispatch %d", b.calls.Load())
	}
	record, err := store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
	if err != nil || record.State != "confirmed" {
		t.Fatalf("receipt %+v %v", record, err)
	}
	for _, suffix := range []string{"", "-wal"} {
		body, err := os.ReadFile(path + suffix)
		if err == nil && strings.Contains(string(body), "bootstrap-secret-must-not-persist") {
			t.Fatal("bootstrap persisted in SQLite")
		}
	}
}
func TestFacadeNoReservationOrPolicyDriftCannotDispatch(t *testing.T) {
	f, _, b, _ := facadeFixture(t)
	if _, err := f.Create(context.Background(), bootstrap("missing", "pool-a")); err == nil {
		t.Fatal("create without preflight")
	}
	preflight(t, f, "runner-1", "pool-a")
	for _, change := range []func(*common.BootstrapInstance){func(v *common.BootstrapInstance) { v.Flavor = "bigger" }, func(v *common.BootstrapInstance) { v.Image = "remote" }, func(v *common.BootstrapInstance) { v.OSType = common.Windows }, func(v *common.BootstrapInstance) { v.ExtraSpecs = []byte(`{"limits.cpu":"99"}`) }, func(v *common.BootstrapInstance) { v.UserDataOptions.EnableBootDebug = true }, func(v *common.BootstrapInstance) { v.Name = "../other" }} {
		value := bootstrap("runner-1", "pool-a")
		change(&value)
		if _, err := f.Create(context.Background(), value); !errors.Is(err, ErrPolicy) {
			t.Fatalf("drift allowed %v", err)
		}
	}
	if b.calls.Load() != 0 {
		t.Fatal("untrusted policy reached backend")
	}
}
func TestFacadeUnknownOrDriftRetainsReceiptAndNeverRedispatches(t *testing.T) {
	for _, mode := range []string{"failed", "drift"} {
		t.Run(mode, func(t *testing.T) {
			f, store, b, _ := facadeFixture(t)
			b.fail = mode == "failed"
			b.drift = mode == "drift"
			preflight(t, f, "runner-1", "pool-a")
			for i := 0; i < 3; i++ {
				_, err := f.Create(context.Background(), bootstrap("runner-1", "pool-a"))
				if err == nil || strings.Contains(err.Error(), "private-") {
					t.Fatal("unsafe create failure")
				}
			}
			if b.calls.Load() != 1 {
				t.Fatal("ambiguous create repeated")
			}
			record, err := store.LookupDispatch(context.Background(), key("controller-a", "pool-a", "runner-1"))
			if err != nil || record.State != "unknown" || record.OperationID != operation || record.ResourceID != "runner-1" {
				t.Fatalf("lost correlation %+v %v", record, err)
			}
			state, err := store.Snapshot(context.Background())
			if err != nil || state.Allocated.Global.Runners != 1 {
				t.Fatal("unknown allocation released")
			}
		})
	}
}
func TestFacadePreflightDefersAtGlobalCapacityAcrossPools(t *testing.T) {
	f, store, b, _ := facadeFixture(t)
	for i, name := range []string{"one", "two", "three", "four"} {
		pool := "pool-a"
		if i%2 != 0 {
			pool = "pool-b"
		}
		preflight(t, f, name, pool)
	}
	req := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "fifth", PoolID: "pool-b"}
	exit, data, err := f.Admit(context.Background(), req)
	if err != nil || exit != 75 {
		t.Fatalf("expected defer %d %s %v", exit, data, err)
	}
	if _, err := garmprotocol.Parse(exit, data, req); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Snapshot(context.Background())
	if state.Allocated.Global.Runners != 4 || b.calls.Load() != 0 {
		t.Fatal("bad admission charge or unexpected create")
	}
}
func TestReconcileMergedExistingUnknownAndOrphanNeverRelease(t *testing.T) {
	f, store, b, _ := facadeFixture(t)
	preflight(t, f, "runner-1", "pool-a")
	if _, err := f.Create(context.Background(), bootstrap("runner-1", "pool-a")); err != nil {
		t.Fatal(err)
	}
	b.instances["orphan"] = incusapi.Instance{Name: "orphan", Status: 103, CPUMillis: 2000, RAMMiB: 2048, ControllerID: "controller-a", PoolID: "pool-b"}
	rows := garmRows{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: "running", Busy: true}}
	if err := f.Reconcile(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Snapshot(context.Background())
	if !state.Ready || state.Allocated.Global.Runners != 2 || state.Allocated.Global.CPU != 3000 {
		t.Fatalf("merge accounting %+v", state.Allocated)
	}
	b.instances = map[string]incusapi.Instance{}
	if err := f.Reconcile(context.Background(), garmRows{}); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Snapshot(context.Background())
	if after.Allocated.Global != state.Allocated.Global {
		t.Fatal("missing resources released")
	}
	for _, resource := range after.Inventory {
		if resource.State != "unknown" {
			t.Fatal("missing resource not marked unknown")
		}
	}
}
func TestReconcileFailuresLeaveDurableBarrierClosed(t *testing.T) {
	for _, mode := range []string{"read-error", "operation-active", "operation-no-resource", "unowned", "bad-reservation", "bad-garm", "unknown-pool"} {
		t.Run(mode, func(t *testing.T) {
			f, store, b, _ := facadeFixture(t)
			rows := garmRows{}
			switch mode {
			case "read-error":
				b.readFail = true
			case "operation-active":
				b.operations = []incusapi.Operation{{ID: operation, Status: 103}}
			case "operation-no-resource":
				b.operations = []incusapi.Operation{{ID: operation, Status: 200, Instances: []string{"invisible"}}}
			case "bad-garm":
				rows = garmRows{{Name: "runner-1", ProviderID: "different", PoolID: "pool-a"}}
			default:
				v := incusapi.Instance{Name: "orphan", Status: 103, CPUMillis: 1000, RAMMiB: 1024, ControllerID: "controller-a", PoolID: "pool-a"}
				if mode == "unowned" {
					v.ControllerID = "other"
				}
				if mode == "bad-reservation" {
					v.ReservationKey = "wrong"
				}
				if mode == "unknown-pool" {
					v.PoolID = "other"
				}
				b.instances[v.Name] = v
			}
			if err := f.Reconcile(context.Background(), rows); err == nil {
				t.Fatal("incomplete source reopened admission")
			}
			state, _ := store.Snapshot(context.Background())
			if state.Ready {
				t.Fatal("barrier not durable")
			}
		})
	}
}

func TestNilSourceAlsoClosesReconciliationBarrier(t *testing.T) {
	f, store, _, _ := facadeFixture(t)
	if err := f.Reconcile(context.Background(), nil); err == nil {
		t.Fatal("nil source accepted")
	}
	state, _ := store.Snapshot(context.Background())
	if state.Ready {
		t.Fatal("nil source left readiness open")
	}
}
