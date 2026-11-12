package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
)

// mutablePreparationSource is a trusted in-process fixture, never a production
// assertion/endpoint. Parser and full source/poll wire tests are separate gates.
type mutablePreparationSource struct {
	rows []GarmInstance
	pool PreparationPoolObservation
}

func (s *mutablePreparationSource) Instances(context.Context) ([]GarmInstance, error) {
	return append([]GarmInstance(nil), s.rows...), nil
}
func (s *mutablePreparationSource) PreparationPool(context.Context, string) (PreparationPoolObservation, error) {
	return s.pool, nil
}
func configureSourceFixture(t *testing.T, f *Facade) (*mutablePreparationSource, string) {
	t.Helper()
	generation := uint64(0)
	jit := true
	actor := strings.Repeat("b", 64)
	policy := f.pools["pool-a"]
	source := &mutablePreparationSource{pool: PreparationPoolObservation{DefaultBootstrap: true, ShellDisabled: true, Enabled: true, Image: policy.Image, Flavor: policy.Flavor, OSType: "linux", OSArch: string(policy.Arch), FieldsKnown: true, PoolID: "pool-a", ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github-fixture"}}
	err := f.configurePreparations(PreparationSettings{Endpoint: "https://garm.example.invalid/api/v1/instances", Providers: []string{"capacity"}, Source: source, Policies: map[string]PreparationPoolPolicy{"pool-a": {ProviderName: source.pool.ProviderName, EntityType: source.pool.EntityType, EntityID: source.pool.EntityID, ForgeEndpointName: source.pool.ForgeEndpointName, Generation: &generation, JITEnabled: &jit, SourceFingerprints: []string{actor}}}})
	if err != nil {
		t.Fatal(err)
	}
	return source, actor
}
func publishOriginalFixture(t *testing.T, f *Facade, source *mutablePreparationSource, actor, name string) garmprotocol.PreparationRequest {
	t.Helper()
	request := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: name, PoolID: "pool-a", InstanceID: "11111111-1111-4111-8111-111111111111", AgentID: 42, ProviderName: source.pool.ProviderName, EntityType: source.pool.EntityType, EntityID: source.pool.EntityID, ForgeEndpointName: source.pool.ForgeEndpointName, JITEnabled: true}
	if _, err := f.RecordPreparation(context.Background(), request, actor); err != nil {
		t.Fatal(err)
	}
	source.rows = []GarmInstance{{RowID: request.InstanceID, Name: name, PoolID: request.PoolID, ProviderName: request.ProviderName, AgentID: request.AgentID, Generation: request.Generation, IdentityFieldsKnown: true, State: "pending_create", SourcePreparationPending: true}}
	return request
}
func TestOriginalSourceReceiptQualifiesPositiveAgentBeforeAndDuringProviderPreparation(t *testing.T) {
	for _, state := range []string{"pending_create", "creating"} {
		t.Run(state, func(t *testing.T) {
			f, s, backends, _ := multiHostFacade(t)
			source, actor := configureSourceFixture(t, f)
			ctx := context.Background()
			name := "positive-original"
			preflight(t, f, name, "pool-a")
			request := publishOriginalFixture(t, f, source, actor, name)
			source.rows[0].State = state
			if _, err := f.ValidatePreparation(ctx, request, actor); err != nil {
				t.Fatal(err)
			}
			if err := f.Reconcile(ctx, source); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.ReadSnapshot(ctx)
			if err != nil || !snapshot.Ready || len(snapshot.Inventory) != 0 || snapshot.Allocated.Global.Runners != 1 {
				t.Fatalf("placeholder accounting %+v %v", snapshot, err)
			}
			if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
				t.Fatal(err)
			}
			calls := int32(0)
			for _, backend := range backends {
				calls += backend.calls.Load()
			}
			if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
			dispatch, err := s.LookupDispatch(ctx, key(f.controller, "pool-a", name))
			if err != nil || dispatch.OriginalPreparationSequence <= 0 {
				t.Fatal("source history not bound to intent", err)
			}
		})
	}
}
func TestOriginalSourceDirectCreateAndReconcileNeverInferPositiveAgentProof(t *testing.T) {
	for _, kind := range []string{"missing", "wrong-row", "wrong-agent", "wrong-generation", "wrong-provider", "wrong-runner", "busy", "provider-id", "scale-set", "prior-inventory", "template-drift", "shell-enabled", "route-drift", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f, s, backends, _ := multiHostFacade(t)
			source, actor := configureSourceFixture(t, f)
			ctx := context.Background()
			name := "reject-original"
			preflight(t, f, name, "pool-a")
			if kind == "missing" {
				source.rows = []GarmInstance{{Name: name, PoolID: "pool-a", ProviderName: "capacity", AgentID: 42, RowID: "11111111-1111-4111-8111-111111111111", IdentityFieldsKnown: true, State: "pending_create", SourcePreparationPending: true}}
			} else {
				publishOriginalFixture(t, f, source, actor, name)
			}
			row := &source.rows[0]
			switch kind {
			case "wrong-row":
				row.RowID = "44444444-4444-4444-8444-444444444444"
			case "wrong-agent":
				row.AgentID++
			case "wrong-generation":
				row.Generation++
			case "wrong-provider":
				row.ProviderName = "other"
			case "wrong-runner":
				row.SourcePreparationPending = false
			case "busy":
				row.Busy = true
			case "provider-id":
				row.ProviderID = name
			case "scale-set":
				row.ScaleSetID = 1
			case "template-drift":
				source.pool.TemplateID = 1
			case "shell-enabled":
				source.pool.ShellDisabled = false
			case "route-drift":
				source.pool.Generation++
			case "prior-inventory":
				if err := f.Reconcile(ctx, garmRows{{Name: name, PoolID: "pool-a", State: "creating"}}); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				plan, err := s.LookupExecutionPlan(ctx, key(f.controller, "pool-a", name))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.CancelUndispatched(ctx, plan.Request); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err == nil {
				t.Fatal("unqualified direct create")
			}
			// Cancellation reappearance rightly closes readiness; other rows are retained
			// as charged inventory, not silently transformed into trusted preparation.
			err := f.Reconcile(ctx, source)
			snapshot, readErr := s.ReadSnapshot(ctx)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if kind != "cancelled" && (err != nil || len(snapshot.Inventory) != 1 || snapshot.Allocated.Global.Runners != 1) {
				t.Fatalf("lost charge/row %+v %v", snapshot, err)
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("backend invoked")
				}
			}
		})
	}
}

func TestOriginalSourceKnownPoolDriftDefersBeforeReservationAndOriginalJIT(t *testing.T) {
	for _, kind := range []string{"generation", "provider", "entity", "forge", "template", "extras", "shell", "image", "flavor", "os", "arch", "missing-route", "unknown-metadata", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f, s, backends, _ := multiHostFacade(t)
			source, _ := configureSourceFixture(t, f)
			switch kind {
			case "generation":
				source.pool.Generation++
			case "provider":
				source.pool.ProviderName = "other"
			case "entity":
				source.pool.EntityID = "44444444-4444-4444-8444-444444444444"
			case "forge":
				source.pool.ForgeEndpointName = "other"
			case "template":
				source.pool.TemplateID = 1
			case "extras":
				source.pool.DefaultBootstrap = false
			case "shell":
				source.pool.ShellDisabled = false
			case "image":
				source.pool.Image = "other"
			case "flavor":
				source.pool.Flavor = "other"
			case "os":
				source.pool.OSType = "windows"
			case "arch":
				source.pool.OSArch = "arm64"
			case "missing-route":
				delete(f.preparationRoutes, "pool-a")
			case "disabled":
				source.pool.Enabled = false
			case "unknown-metadata":
				source.pool.FieldsKnown = false
			}
			// An installed source calls JIT only after a known successful preflight ACK.
			// This fixture verifies that ACK is never issued for known drift.
			for i := 0; i < 3; i++ {
				request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: fmt.Sprintf("drift-%d", i), PoolID: "pool-a"}
				exit, raw, err := f.Admit(context.Background(), request)
				if err != nil || exit != garmprotocol.DeferredExitCode {
					t.Fatalf("known drift admitted %d %v", exit, err)
				}
				if _, err := garmprotocol.Parse(exit, raw, request); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := s.ReadSnapshot(context.Background())
			if err != nil || len(snapshot.Reservations) != 0 || snapshot.ExecutionPlanCount != 0 || snapshot.Allocated.Global.Runners != 0 || len(source.rows) != 0 {
				t.Fatal("known drift burned capacity or published row", err)
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("known drift reached backend")
				}
			}
		})
	}
}

func TestOriginalSourceDisabledPoolKeepsPreparationChargedWithoutGrantingCreate(t *testing.T) {
	for _, state := range []string{"pending_create", "creating"} {
		t.Run(state, func(t *testing.T) {
			f, s, backends, _ := multiHostFacade(t)
			source, actor := configureSourceFixture(t, f)
			ctx := context.Background()
			name := "disabled-preparation"
			preflight(t, f, name, "pool-a")
			original := publishOriginalFixture(t, f, source, actor, name)
			source.rows[0].State = state
			source.pool.Enabled = false
			if _, err := f.ValidatePreparation(ctx, original, actor); err == nil {
				t.Fatal("disabled source validation allowed")
			}
			if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err == nil {
				t.Fatal("disabled pending create allowed")
			}
			if err := f.Reconcile(ctx, source); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.ReadSnapshot(ctx)
			if err != nil || !snapshot.Ready || len(snapshot.Inventory) != 0 || snapshot.Allocated.Global.Runners != 1 {
				t.Fatal("temporary disable laundered/released/imported original placeholder", err)
			}
			for _, backend := range backends {
				if backend.calls.Load() != 0 {
					t.Fatal("disabled pool reached backend")
				}
			}
			source.pool.Enabled = true
			if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
				t.Fatal("original could not resume after explicit reenable", err)
			}
		})
	}
}
func TestOriginalSourceDisabledPoolConfirmedReplayIsReadOnly(t *testing.T) {
	f, s, backends, _ := multiHostFacade(t)
	source, actor := configureSourceFixture(t, f)
	ctx := context.Background()
	name := "disabled-confirmed"
	preflight(t, f, name, "pool-a")
	publishOriginalFixture(t, f, source, actor, name)
	if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
		t.Fatal(err)
	}
	source.pool.Enabled = false
	if _, err := f.Create(ctx, bootstrap(name, "pool-a")); err != nil {
		t.Fatal("disabled confirmed replay was not readonly", err)
	}
	calls := int32(0)
	for _, backend := range backends {
		calls += backend.calls.Load()
	}
	snapshot, err := s.ReadSnapshot(ctx)
	if err != nil || calls != 1 || snapshot.Allocated.Global.Runners != 1 {
		t.Fatal("disabled replay dispatched/released", err)
	}
}
