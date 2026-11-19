package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestManagedInventoryObservationRequiresExplicitReadOnlyConfig(t *testing.T) {
	var config configuration
	if err := json.Unmarshal([]byte(`{}`), &config); err != nil || config.ManagedInventoryObservation ||
		!managedInventoryObservationAllowed(config, true) {
		t.Fatal("default configuration changed")
	}
	if err := json.Unmarshal([]byte(`{"managed_inventory_observation":true}`), &config); err != nil {
		t.Fatal(err)
	}
	config.Admin = &adminConfiguration{}
	config.Garm.ManagedProxy, config.Garm.ProxyServerKeyPin = true, "checked-by-managed-client"
	if !managedInventoryObservationAllowed(config, false) || managedInventoryObservationAllowed(config, true) {
		t.Fatal("incorrect provisioning scope")
	}
	for name, edit := range map[string]func(*configuration){
		"unmanaged":             func(c *configuration) { c.Garm.ManagedProxy = false },
		"missing pin":           func(c *configuration) { c.Garm.ProxyServerKeyPin = "" },
		"stock token":           func(c *configuration) { c.Garm.TokenFile = "not used" },
		"no admin":              func(c *configuration) { c.Admin = nil },
		"management configured": func(c *configuration) { c.Admin = &adminConfiguration{GarmManagement: &garmManagementConfiguration{}} },
		"management enabled":    func(c *configuration) { c.Admin = &adminConfiguration{EnableExperimentalGarmManagement: true} },
		"policy management":     func(c *configuration) { c.Admin = &adminConfiguration{EnableExperimentalManagement: true} },
		"stock provider":        func(c *configuration) { c.EnableStockProvider = true },
		"allocator loop":        func(c *configuration) { c.AllocatorLoop = &allocatorLoopConfiguration{} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config
			edit(&cfg)
			if managedInventoryObservationAllowed(cfg, false) {
				t.Fatal("unsupported combination accepted")
			}
		})
	}
}

func TestManagedInventoryObservationDoesNotLeakAcrossRuns(t *testing.T) {
	original := &adminConfiguration{IncusOnboarding: &onboardingConfiguration{}, EnableExperimentalOnboardingProbes: true}
	enabled := original.forRuntime()
	enabled.managedInventoryObservation = func(time.Time) adminview.ManagedInventoryObservation {
		return adminview.ManagedInventoryObservation{Available: true}
	}
	disabled := original.forRuntime()
	if original.managedInventoryObservation != nil || disabled.managedInventoryObservation != nil {
		t.Fatal("the first run's sampler leaked into a disabled run")
	}
	if enabled.managedInventoryObservation == nil {
		t.Fatal("the enabled run lost its own sampler")
	}
	if !disabled.EnableExperimentalOnboardingProbes || disabled.IncusOnboarding != original.IncusOnboarding {
		t.Fatal("independent onboarding configuration did not survive the copy")
	}
	var absent *adminConfiguration
	if absent.forRuntime() != nil {
		t.Fatal("the copy enabled absent admin configuration")
	}
}

func TestManagedInventoryObservationPinnedReadFreshnessAndFailure(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instances" || r.Header.Get("Authorization") != "" || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unexpected inventory request")
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		if fail.Load() {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	pin := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	client, err := garmapi.NewManagedInventory(srv.URL, hex.EncodeToString(pin[:]), trust, []string{"capacity"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	o := &managedInventoryObserver{ctx: t.Context(), read: client.Instances}
	if o.snapshot(time.Now()).Available {
		t.Fatal("fresh result before first read")
	}
	o.sample()
	status := o.snapshot(time.Now())
	if !status.Available || status.Count == nil || *status.Count != 0 || status.SampledAt == nil {
		t.Fatal(status)
	}
	for _, now := range []time.Time{status.SampledAt.Add(-time.Nanosecond), status.SampledAt.Add(managedInventoryFreshness)} {
		stale := o.snapshot(now)
		if stale.Available || stale.Count != nil || stale.SampledAt != nil {
			t.Fatal("invalid status", stale)
		}
	}
	fail.Store(true)
	o.sample()
	if current := o.snapshot(time.Now()); current.Available || current.Count != nil || current.SampledAt != nil || calls.Load() != 2 {
		t.Fatal("retained invalid sample", current, calls.Load())
	}
}

func TestManagedInventoryObserverCancelAndJoinActiveRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan struct{})
	o := &managedInventoryObserver{ctx: ctx, read: func(ctx context.Context) ([]gateway.GarmInstance, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > managedInventoryReadTimeout {
			t.Error("unbounded read")
		}
		close(entered)
		<-ctx.Done()
		return []gateway.GarmInstance{}, nil // A late success cannot restore availability.
	}}
	go func() { defer close(done); o.run() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read was not joined")
	}
	if o.snapshot(time.Now()).Available {
		t.Fatal("canceled read restored availability")
	}
}
