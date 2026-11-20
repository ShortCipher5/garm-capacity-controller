package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var response atomic.Int32
	var calls atomic.Int64
	srv, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instances" || r.Header.Get("Authorization") != "" || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unexpected inventory request")
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		switch response.Load() {
		case 2:
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		case 1:
			_, _ = w.Write([]byte(`[{"provider_name":"foreign-fixture","name":"foreign-row"}]`))
		default:
			_, _ = w.Write([]byte(`[{"provider_name":"capacity","name":"scoped-row","pool_id":"fixture-pool","status":"running"},{"provider_name":"foreign-fixture","name":"foreign-row"}]`))
		}
	}))
	pin := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	client, err := garmapi.NewManagedInventory(srv.URL, hex.EncodeToString(pin[:]), trust, []string{"capacity"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	o := &managedInventoryObserver{ctx: t.Context(), read: client.Instances}
	// Use the real admin handler in process, without starting an admin daemon.
	// Only the existing disposable mTLS inventory fixture uses a socket.
	token := strings.Repeat("a", 64)
	handler, err := adminview.New(managementStore(t), token, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetManagedInventoryObservation(o.snapshot)
	checkOverview := func(wantAvailable bool, wantCount int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/overview", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		var view adminview.Overview
		if out.Code != http.StatusOK || json.Unmarshal(out.Body.Bytes(), &view) != nil || view.ManagedInventoryObservation == nil {
			t.Fatal("inventory observation absent from authenticated overview")
		}
		if view.Ready || view.Provisioning || view.ManagementEnabled || view.GarmManagementAvailable {
			t.Fatal("inventory presentation changed readiness or write authority")
		}
		body := out.Body.String()
		if strings.Contains(body, "scoped-row") || strings.Contains(body, "foreign-row") {
			t.Fatal("inventory identities leaked into overview")
		}
		got := view.ManagedInventoryObservation
		if got.Available != wantAvailable {
			t.Fatal("unexpected inventory availability")
		}
		if wantAvailable {
			if got.SampledAt == nil || got.Count == nil || *got.Count != wantCount {
				t.Fatal("unexpected scoped inventory count or timestamp")
			}
		} else if got.SampledAt != nil || got.Count != nil {
			t.Fatal("unavailable observation exposed stale inventory")
		}
	}
	checkOverview(false, 0)
	if o.snapshot(time.Now()).Available {
		t.Fatal("fresh result before first read")
	}
	o.sample()
	status := o.snapshot(time.Now())
	if !status.Available || status.Count == nil || *status.Count != 1 || status.SampledAt == nil {
		t.Fatal(status)
	}
	for _, now := range []time.Time{status.SampledAt.Add(-time.Nanosecond), status.SampledAt.Add(managedInventoryFreshness)} {
		stale := o.snapshot(now)
		if stale.Available || stale.Count != nil || stale.SampledAt != nil {
			t.Fatal("invalid status", stale)
		}
	}
	checkOverview(true, 1)
	response.Store(1)
	o.sample()
	checkOverview(true, 0)
	response.Store(2)
	o.sample()
	checkOverview(false, 0)
	if current := o.snapshot(time.Now()); current.Available || current.Count != nil || current.SampledAt != nil || calls.Load() != 3 {
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
