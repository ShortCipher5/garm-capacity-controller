package gatewayrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	common "github.com/cloudbase/garm-provider-common/params"
)

type deleteGarmSource []gateway.GarmInstance

func (s deleteGarmSource) Instances(context.Context) ([]gateway.GarmInstance, error) {
	return append([]gateway.GarmInstance(nil), s...), nil
}

func deleteFacade(t *testing.T) (*gateway.Facade, *testsupport.FakeIncus) {
	t.Helper()
	budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fake := testsupport.NewFakeIncus(t, "rpc-delete-daemon")
	policy := gateway.PoolPolicy{Host: "a", CPU: 1000, RAM: 1024, Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: common.Amd64}
	f, err := gateway.New(context.Background(), store, "controller-a", map[string]gateway.PoolPolicy{"pool-a": policy}, map[string]gateway.Backend{"a": fake.Client})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(context.Background(), source{}); err != nil {
		t.Fatal(err)
	}
	request := garmprotocol.Request{Protocol: garmprotocol.Version, Name: "runner-1", PoolID: "pool-a"}
	if exit, _, err := f.Admit(context.Background(), request); err != nil || exit != 0 {
		t.Fatalf("admit %d %v", exit, err)
	}
	if _, err := f.Create(context.Background(), bootstrap()); err != nil {
		t.Fatal(err)
	}
	rows := []gateway.GarmInstance{{Name: "runner-1", ProviderID: "runner-1", PoolID: "pool-a", State: "running", Idle: true}}
	if err := f.Reconcile(context.Background(), deleteGarmSource(rows)); err != nil {
		t.Fatal(err)
	}
	return f, fake
}

func runningDeleteFixture(t *testing.T, provision bool) (*Client, *testsupport.FakeIncus) {
	t.Helper()
	f, fake := deleteFacade(t)
	var handler *Handler
	server, credentials := rpcTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	hash := sha256.Sum256(credentials.Certificates[0].Certificate[0])
	var err error
	handler, err = NewHandler(f, []string{hex.EncodeToString(hash[:])}, provision)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(server.URL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, fake
}

func TestAuthenticatedDeleteReleasesAndReplayIsIdempotentOverWire(t *testing.T) {
	ctx := context.Background()
	client, fake := runningDeleteFixture(t, true)
	request := DeletionRequest{Controller: "controller-a", Name: "runner-1", PoolID: "pool-a"}
	response, err := client.Delete(ctx, request)
	if err != nil || response.Outcome != OutcomeReleased || response.Resources == nil || response.Resources.CPU != 1000 || response.Resources.RAM != 1024 || response.Resources.Runners != 1 {
		t.Fatalf("delete %+v %v", response, err)
	}
	replay, err := client.Delete(ctx, request)
	if err != nil || replay.Outcome != OutcomeAlreadyReleased || replay.Resources != nil {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if fake.Deletes.Load() != 1 {
		t.Fatalf("replay repeated backend delete %d", fake.Deletes.Load())
	}
	unknown := DeletionRequest{Controller: "controller-a", Name: "missing", PoolID: "pool-a"}
	if _, err := client.Delete(ctx, unknown); err == nil {
		t.Fatal("unknown runner deleted over wire")
	}
}

func TestDeleteRouteRefusedWhenReadOnlyAndClientRejectsIncompleteRequests(t *testing.T) {
	ctx := context.Background()
	readOnly, _ := runningDeleteFixture(t, false)
	request := DeletionRequest{Controller: "controller-a", Name: "runner-1", PoolID: "pool-a"}
	if _, err := readOnly.Delete(ctx, request); err == nil {
		t.Fatal("read-only listener deleted")
	}
	provisioned, _ := runningDeleteFixture(t, true)
	for _, bad := range []DeletionRequest{{Name: "runner-1", PoolID: "pool-a"}, {Controller: "controller-a", PoolID: "pool-a"}, {Controller: "controller-a", Name: "runner-1"}} {
		if _, err := provisioned.Delete(ctx, bad); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("client did not refuse incomplete request %+v: %v", bad, err)
		}
	}
}
