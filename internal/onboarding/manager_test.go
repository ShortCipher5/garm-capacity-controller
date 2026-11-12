package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

const requestID = "11111111-1111-4111-8111-111111111111"

type observerFunc func(context.Context) (incusapi.PhysicalServerIdentity, error)

func (f observerFunc) PhysicalIdentity(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
	return f(ctx)
}
func storeFixture(t *testing.T) *admission.SQLiteStore {
	t.Helper()
	budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"active-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"active-a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func snapshot(t *testing.T, store *admission.SQLiteStore) admission.SQLiteSnapshot {
	t.Helper()
	state, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func unchangedExceptAudit(t *testing.T, before, after admission.SQLiteSnapshot) {
	t.Helper()
	before.Audit = nil
	after.Audit = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preflight changed policy/readiness/resources/bindings/history")
	}
}
func TestActualMTLSIdentityProbeAuditsButCannotEnrollOrQualify(t *testing.T) {
	ctx := context.Background()
	store := storeFixture(t)
	fixture := testsupport.Identity(t, "private-fixture-daemon")
	manager, err := New(store, map[string]Observer{"candidate-b": fixture.Client}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, store)
	result, err := manager.Probe(ctx, Request{requestID, "candidate-b", "read-only onboarding check"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ObservationAvailable || result.ServerVersion != "6.0.4" || result.BindingClass != "no_match_observed" || result.EnrollmentPerformed || result.ActivationPerformed || result.CapacityQualified {
		t.Fatalf("misleading preflight result %+v", result)
	}
	after := snapshot(t, store)
	unchangedExceptAudit(t, before, after)
	if len(after.Audit) != len(before.Audit)+2 {
		t.Fatal("missing request/completion audit")
	}
	encoded, _ := json.Marshal(struct {
		Result Result
		Audit  []admission.AuditEvent
	}{result, after.Audit})
	identity, err := fixture.Client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{identity.ServerName(), identity.ServerKeyFingerprint(), identity.OriginFingerprint(), identity.ScopeFingerprint(), "BEGIN CERTIFICATE"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("identity detail or certificate exposed")
		}
	}
}
func TestStoredIdentityMatchIsAWarningNotSeparateCapacity(t *testing.T) {
	ctx := context.Background()
	store := storeFixture(t)
	fixture := testsupport.Identity(t, "fixture-shared-daemon")
	identity, err := fixture.Client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := store.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = store.BindHostIdentities(ctx, fence, []admission.HostBinding{{Host: "active-a", ServerKeyFingerprint: identity.ServerKeyFingerprint(), OriginFingerprint: identity.OriginFingerprint(), ScopeFingerprint: identity.ScopeFingerprint(), ServerName: identity.ServerName(), ServerVersion: identity.ServerVersion(), ObservedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, map[string]Observer{"candidate-b": fixture.Client}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, store)
	result, err := manager.Probe(ctx, Request{requestID, "candidate-b", "check duplicate observation"})
	if err != nil {
		t.Fatal(err)
	}
	if result.BindingClass != "match_observed" || result.CapacityQualified || result.EnrollmentPerformed {
		t.Fatal("stored match became a qualification/enrollment permit")
	}
	unchangedExceptAudit(t, before, snapshot(t, store))
}
func TestUnknownProfileBadInputAndAuditFailureNeverContactObserver(t *testing.T) {
	store := storeFixture(t)
	baselineAudit := len(snapshot(t, store).Audit)
	var calls atomic.Int32
	observer := observerFunc(func(context.Context) (incusapi.PhysicalServerIdentity, error) {
		calls.Add(1)
		return incusapi.PhysicalServerIdentity{}, nil
	})
	manager, err := New(store, map[string]Observer{"candidate-b": observer}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{{requestID, "https://not-allowed.invalid", "test"}, {"bad", "candidate-b", "test"}, {"11--1111-1111-4111-8111-111111111111", "candidate-b", "test"}, {requestID, "candidate-b", "bad\nreason"}} {
		if _, err := manager.Probe(context.Background(), request); !errors.Is(err, ErrDenied) {
			t.Fatal("invalid request accepted")
		}
	}
	if calls.Load() != 0 || len(snapshot(t, store).Audit) != baselineAudit {
		t.Fatal("invalid input caused read/audit")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Probe(context.Background(), Request{requestID, "candidate-b", "audit must precede read"}); !errors.Is(err, ErrUnavailable) || calls.Load() != 0 {
		t.Fatal("failed audit allowed an external read")
	}
}
func TestUnavailableErrorIsSanitizedAndCanceledCallerStillGetsAuditCompletion(t *testing.T) {
	store := storeFixture(t)
	baselineAudit := len(snapshot(t, store).Audit)
	entered := make(chan struct{})
	var calls atomic.Int32
	manager, err := New(store, map[string]Observer{"candidate-b": observerFunc(func(ctx context.Context) (incusapi.PhysicalServerIdentity, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return incusapi.PhysicalServerIdentity{}, errors.New("private-endpoint-and-secret-error")
	})}, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		result, err := manager.Probe(ctx, Request{requestID, "candidate-b", "interrupted read"})
		if err == nil && (result.ObservationAvailable || result.EnrollmentPerformed || result.CapacityQualified) {
			err = errors.New("canceled read qualified capacity")
		}
		done <- err
	}()
	<-entered
	if _, err := manager.Probe(context.Background(), Request{"22222222-2222-4222-8222-222222222222", "candidate-b", "concurrent read"}); !errors.Is(err, ErrBusy) {
		t.Fatal("unbounded concurrent probe")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state := snapshot(t, store)
	if calls.Load() != 1 || len(state.Audit) != baselineAudit+2 {
		t.Fatal("read repeated or canceled completion audit lost")
	}
	encoded, _ := json.Marshal(state.Audit)
	if strings.Contains(string(encoded), "private-endpoint") {
		t.Fatal("private observer error leaked")
	}
}
func TestDiscoveryIsCopyOnlyAndNeverReadsOrAdvertisesEnrollment(t *testing.T) {
	store := storeFixture(t)
	var calls atomic.Int32
	manager, err := New(store, map[string]Observer{"candidate-b": observerFunc(func(context.Context) (incusapi.PhysicalServerIdentity, error) {
		calls.Add(1)
		return incusapi.PhysicalServerIdentity{}, nil
	})}, false)
	if err != nil {
		t.Fatal(err)
	}
	view := manager.Discovery()
	if view.Mode != "read-only-onboarding-preflight" || view.ProbeEnabled || view.EnrollmentSupported || view.CapacityQualified || calls.Load() != 0 {
		t.Fatal("discovery implied qualification or made a request")
	}
	if _, err := manager.Probe(context.Background(), Request{requestID, "candidate-b", "must remain disabled"}); !errors.Is(err, ErrDisabled) || calls.Load() != 0 {
		t.Fatal("disabled manager allowed a probe")
	}
	view.Profiles[0].ID = "changed"
	if manager.Discovery().Profiles[0].ID != "candidate-b" {
		t.Fatal("caller modified profile scope")
	}
}
