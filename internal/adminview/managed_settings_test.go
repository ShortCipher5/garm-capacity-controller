package adminview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedSettingsAdapterRejectsEveryNonMaximumField(t *testing.T) {
	v := &managedSettingsView{writes: true, pools: map[string]bool{"pool": true}, client: &managedsettings.Client{}}
	n := uint64(2)
	yes := true
	tags := []string{"linux"}
	for _, u := range []garmapi.PoolUpdate{{}, {MaxRunners: &n, Enabled: &yes}, {MaxRunners: &n, Priority: &n}, {MaxRunners: &n, MinIdleRunners: &n}, {MaxRunners: &n, Tags: &tags}} {
		if _, e := v.UpdatePool(context.Background(), "id", "pool", "fp", u, "reason"); !errors.Is(e, garmapi.ErrManagementUnsafe) {
			t.Fatal(e)
		}
	}
	if _, e := v.ResolvePool(context.Background(), "id"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
	if _, e := v.UpdateRepositoryBalancer(context.Background(), "id", "repo", "fp", "pack", "reason"); !errors.Is(e, garmapi.ErrManagementDisabled) {
		t.Fatal(e)
	}
}

func TestOptionalHistoryDeadlinePreservesRequiredSettings(t *testing.T) {
	poolID := "11111111-1111-4111-8111-111111111111"
	controller := "22222222-2222-4222-8222-222222222222"
	pending := garmmanagement.Intent{
		RequestID: "33333333-3333-4333-8333-333333333333", Kind: "pool", Target: poolID,
		State: "pending", CreatedAt: "2026-11-18T10:00:00Z",
	}
	unknown := pending
	unknown.State = "unknown"
	var settings atomic.Bool
	cancelled := make(chan struct{})
	server, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(managedsource.SourceHeader, strings.Repeat("a", 64))
		w.Header().Set(managedsource.ControllerHeader, controller)
		switch r.URL.Path {
		case "/v1/managed/status":
			json.NewEncoder(w).Encode(garmmanagement.Status{WriteBlocked: true, Pending: []garmmanagement.Intent{pending}})
		case "/v1/managed/settings":
			settings.Store(true)
			json.NewEncoder(w).Encode(garmmanagement.Snapshot{ManagementSnapshot: garmapi.ManagementSnapshot{WritesEnabled: true, Pools: []garmapi.ManagedPool{{ID: poolID, ProviderName: "capacity", Fingerprint: strings.Repeat("b", 64), MaxRunners: 3, MinIdleRunners: 1}}, Repositories: []garmapi.ManagedRepository{}}, Status: garmmanagement.Status{WriteBlocked: true, Pending: []garmmanagement.Intent{unknown}}})
		case "/v1/managed/history":
			if !settings.Load() {
				t.Error("optional read preceded required settings")
			}
			<-r.Context().Done()
			close(cancelled)
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	pin := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	client, e := managedsettings.New(managedsettings.Configuration{Endpoint: server.URL, ServerKeyPin: hex.EncodeToString(pin[:]), ControllerID: controller, TLS: trust, Providers: []string{"capacity"}, PoolIDs: []string{poolID}})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	v, e := NewManagedSettingsView(client, false, []string{poolID})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, e := v.Snapshot(ctx)
	if e != nil || !out.SettingsAvailable || out.ManagedHistoryAvailable || !out.HistoryTruncated || len(out.Settings.Pools) != 1 || !out.WriteBlocked || out.Pending == nil || out.Pending.State != "unknown" || out.Pending.ResolutionAvailable {
		t.Fatal(out, e)
	}
	if ctx.Err() != nil {
		t.Fatal("optional read exhausted caller deadline")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("history request not cancelled")
	}
}
