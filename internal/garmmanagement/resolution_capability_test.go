package garmmanagement

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"testing"
)

func TestReceiptResolutionCapabilityFailsClosed(t *testing.T) {
	m := &Manager{conditional: &garmapi.ConditionalManagementClient{}}
	good := Intent{Kind: "pool", State: "unknown", Dispatch: encoded(poolDispatch{API: "conditional", ETag: `"1:1"`, Fingerprint: "reviewed"})}
	if !m.CanResolvePoolIntent(good) {
		t.Fatal("conditional receipt path missing")
	}
	for _, mutate := range []func(*Intent){
		func(i *Intent) { i.Kind = "repository" },
		func(i *Intent) { i.State = "confirmed" },
		func(i *Intent) { i.Dispatch = nil },
		func(i *Intent) { i.Dispatch = []byte("{") },
		func(i *Intent) { i.Dispatch = encoded(poolDispatch{API: "legacy"}) },
		func(i *Intent) { i.Dispatch = encoded(poolDispatch{API: "conditional"}) },
	} {
		i := good
		mutate(&i)
		if m.CanResolvePoolIntent(i) {
			t.Fatalf("unsafe resolution offered: %+v", i)
		}
	}
	m.conditional = nil
	if m.CanResolvePoolIntent(good) {
		t.Fatal("flag-off restart advertised receipt resolution")
	}
}
