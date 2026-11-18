package gatewayrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStockSeparateLeafAndExactPoolWire(t *testing.T) {
	f, b, store := facadeStore(t)
	var h *StockHandler
	server, credentials, other := rpcTLSRolesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	sum := sha256.Sum256(credentials.Certificates[0].Certificate[0])
	fp := hex.EncodeToString(sum[:])
	scope := StockScope{Controller: "controller-a", PoolID: "pool-a", ProviderName: "stock-provider", ClientFingerprint: fp}
	state, e := store.ReadBrokerGrantState(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	binding, e := store.LookupHostBinding(context.Background(), "a")
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.IssueBrokerGrants(context.Background(), admission.BrokerGrantBatch{ID: strings.Repeat("c", 64), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []admission.BrokerGrantSpec{{Authority: admission.StockProviderAuthority{Controller: scope.Controller, PoolID: scope.PoolID, ProviderName: scope.ProviderName, ClientFingerprint: scope.ClientFingerprint}, Host: "a", HostBindingSequence: binding.Sequence, Resources: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, Execution: admission.ExecutionImage{Controller: "controller-a", Flavor: "small", Image: "linux", Fingerprint: strings.Repeat("a", 64), Profiles: []string{"base"}, Type: "container", Arch: "amd64"}, ExpiresAt: time.Now().Add(time.Hour)}}})
	if e != nil {
		t.Fatal(e)
	}
	base, err := NewHandler(f, []string{strings.Repeat("a", 64)}, false)
	if err != nil {
		t.Fatal(err)
	}
	h, err = NewStockHandler(base, []StockScope{scope})
	if err != nil {
		t.Fatal(err)
	}
	collision, _ := NewHandler(f, []string{fp}, false)
	if _, e := NewStockHandler(collision, []StockScope{scope}); e == nil {
		t.Fatal("ordinary leaf reused")
	}
	client, e := NewClient(server.URL, credentials)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	input := bootstrap()
	input.RepoURL = "https://github.com/example/disposable"
	for i := 0; i < 2; i++ {
		if _, e = client.CreateStockProvider(context.Background(), input); e != nil {
			t.Fatal(e)
		}
	}
	if b.creates.Load() != 1 {
		t.Fatal("wire replay redispatched")
	}
	input.PoolID = "pool-b"
	if _, e = client.CreateStockProvider(context.Background(), input); e == nil {
		t.Fatal("cross-pool accepted")
	}
	alien, e := NewClient(server.URL, other(4))
	if e != nil {
		t.Fatal(e)
	}
	defer alien.Close()
	input.PoolID = "pool-a"
	if _, e = alien.CreateStockProvider(context.Background(), input); e == nil {
		t.Fatal("CA-only peer accepted")
	}
	if _, e = client.Create(context.Background(), input); e == nil {
		t.Fatal("stock listener exposed prepared path")
	}
}
