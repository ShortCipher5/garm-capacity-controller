package gatewayrpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

type preparationSource struct{ rows []gateway.GarmInstance }

func (s *preparationSource) Instances(context.Context) ([]gateway.GarmInstance, error) {
	return s.rows, nil
}
func (s *preparationSource) PreparationPool(_ context.Context, id string) (gateway.PreparationPoolObservation, error) {
	return gateway.PreparationPoolObservation{PoolID: id, ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github", FieldsKnown: true, DefaultBootstrap: true, ShellDisabled: true, Enabled: true, Image: "linux", Flavor: "small", OSType: "linux", OSArch: "amd64"}, nil
}
func leafFingerprint(c *tls.Config) string {
	hash := sha256.Sum256(c.Certificates[0].Certificate[0])
	return hex.EncodeToString(hash[:])
}
func TestPreparationRoleIsDistinctRouteScopedAndAcknowledgementIsNotDispatch(t *testing.T) {
	var handler *Handler
	var loseReceiptACK atomic.Bool
	server, provider, mint := rpcTLSRolesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/record-preparation" && loseReceiptACK.CompareAndSwap(true, false) {
			// Execute the actual authenticated handler to COMMIT, then lose only
			// its response. No client retry or row publication is manufactured.
			capture := httptest.NewRecorder()
			handler.ServeHTTP(capture, r)
			if capture.Code != http.StatusOK {
				t.Errorf("lost ACK fixture did not commit: %d", capture.Code)
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	sourceA, sourceB, unknown := mint(4), mint(5), mint(6)
	budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
	ctx := context.Background()
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}, "pool-b": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &backend{instances: map[string]incusapi.Instance{}, identity: testsupport.Identity(t, "source-role-fixture").Client}
	policy := gateway.PoolPolicy{Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}
	source := &preparationSource{}
	gen := uint64(0)
	jit := false
	policies := map[string]gateway.PreparationPoolPolicy{}
	for id, actor := range map[string]string{"pool-a": leafFingerprint(sourceA), "pool-b": leafFingerprint(sourceB)} {
		policies[id] = gateway.PreparationPoolPolicy{ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github", Generation: &gen, JITEnabled: &jit, SourceFingerprints: []string{actor}}
	}
	f, err := gateway.New(ctx, store, "controller-a", map[string]gateway.PoolPolicy{"pool-a": policy, "pool-b": policy}, map[string]gateway.Backend{"a": b}, gateway.PreparationSettings{Endpoint: "https://garm.example.invalid/api/v1/instances", Providers: []string{"capacity"}, Policies: policies, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	handler, err = NewHandler(f, []string{leafFingerprint(provider)}, true, []string{leafFingerprint(sourceA), leafFingerprint(sourceB)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewHandler(f, []string{leafFingerprint(provider)}, true, []string{leafFingerprint(provider), leafFingerprint(sourceA), leafFingerprint(sourceB)}); err == nil {
		t.Fatal("dual role leaf accepted")
	}
	if err = f.Reconcile(ctx, source); err != nil {
		t.Fatal(err)
	}
	ordinary, err := NewClient(server.URL, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	original := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: "runner-1", PoolID: "pool-a", InstanceID: "11111111-1111-4111-8111-111111111111", ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github"}
	if exit, _, err := ordinary.Admit(ctx, garmprotocol.Request{Protocol: garmprotocol.Version, Name: original.Name, PoolID: original.PoolID}); err != nil || exit != 0 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(original)
	post := func(credentials *tls.Config, path string, body []byte, bearer bool) int {
		t.Helper()
		transport := &http.Transport{TLSClientConfig: credentials}
		defer transport.CloseIdleConnections()
		request, _ := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if bearer {
			request.Header.Set("Authorization", "Bearer admin-token-is-not-source-authority")
		}
		response, err := (&http.Client{Transport: transport}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	for _, route := range []string{"/v1/record-preparation", "/v1/validate-preparation"} {
		for _, credentials := range []*tls.Config{provider, sourceB, unknown} {
			if status := post(credentials, route, raw, true); status != http.StatusForbidden {
				t.Fatalf("unauthorized %s status%d", route, status)
			}
		}
	}
	for _, route := range []string{"/v1/admit", "/v1/create", "/v1/cancel-admission"} {
		if status := post(sourceA, route, []byte(`{}`), false); status != http.StatusForbidden {
			t.Fatalf("source gained provider role %s status%d", route, status)
		}
	}
	issuer, err := NewClient(server.URL, sourceA)
	if err != nil {
		t.Fatal(err)
	}
	defer issuer.Close()
	loseReceiptACK.Store(true)
	if _, err := issuer.RecordPreparation(ctx, original); err == nil {
		t.Fatal("lost ACK became known success")
	}
	committed, err := store.LookupOriginalPreparation(ctx, keyForPreparationFixture("controller-a", "pool-a", "runner-1"))
	if err != nil || committed.RecordSequence <= 0 || len(source.rows) != 0 || b.creates.Load() != 0 {
		t.Fatal("lost ACK lost durable charged receipt or published row", err)
	}
	receipt, err := issuer.RecordPreparation(ctx, original)
	if err != nil || receipt.RecordSequence != committed.RecordSequence || receipt.Replay == nil || !*receipt.Replay {
		t.Fatal("exact lost-ACK retry changed original", err)
	}
	if err != nil || receipt.RecordSequence <= 0 {
		t.Fatal("source acknowledgement", err)
	}
	if _, err = issuer.ValidatePreparation(ctx, original); err == nil {
		t.Fatal("receipt invented actual DB row")
	}
	if _, err = ordinary.Create(ctx, bootstrap()); err == nil {
		t.Fatal("direct create ignored absent DB row")
	}
	source.rows = []gateway.GarmInstance{{Name: original.Name, PoolID: original.PoolID, ProviderName: original.ProviderName, RowID: original.InstanceID, IdentityFieldsKnown: true, State: "pending_create", SourcePreparationPending: true}}
	if _, err = issuer.ValidatePreparation(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err = f.Reconcile(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err = ordinary.Create(ctx, bootstrap()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil || snapshot.Allocated.Global.Runners != 1 || b.creates.Load() != 1 {
		t.Fatal("source ack/replay changed charge", err)
	}
}

func keyForPreparationFixture(controller, pool, name string) string {
	sum := sha256.Sum256([]byte(controller + "\x00" + pool + "\x00" + name))
	return hex.EncodeToString(sum[:])
}
