package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

// The disposable discovery fixture must stay behind the real audited
// onboarding manager so the browser exercises the same structs and audit
// pairs as production code.
func TestBrowserDiscoverySourceBehindRealManager(t *testing.T) {
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	store, err := admission.OpenSQLite(t.TempDir()+"/ledger.db", admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := &browserDiscoverySource{}
	manager, err := onboarding.New(store, map[string]onboarding.Observer{"candidate-discover": source}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Discover(context.Background(), onboarding.Request{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "candidate-discover", Reason: "fixture discovery run"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != "candidate-discover" || result.Totals == nil || result.Totals.CPUMillis == nil || *result.Totals.CPUMillis != 8000 || !result.Totals.Complete {
		t.Fatalf("fixture totals not observed: %+v", result.Totals)
	}
	if len(result.Guests) != 2 || result.Guests[0].Name != "fixture-runner" || !result.Guests[0].Managed || result.Guests[1].Managed {
		t.Fatalf("fixture guests not observed: %+v", result.Guests)
	}
	if result.SuggestedBudget.CPU != 6000 || result.SuggestedBudget.RAM != 30720 || result.SuggestedBudget.Disk != 524288-524288/10 || result.SuggestedBudget.Runners != 0 {
		t.Fatalf("suggested budget drifted from the documented headroom formula: %+v", result.SuggestedBudget)
	}
	found := false
	for _, blocker := range result.Blockers {
		if blocker == "physical_capacity_scope_unqualified" {
			found = true
		}
	}
	if !found {
		t.Fatalf("qualification blocker missing: %v", result.Blockers)
	}
	warning := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warning, "unmanaged guests") {
		t.Fatalf("unmanaged warning missing: %q", warning)
	}
	snapshot, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, event := range snapshot.Audit {
		kinds[event.Kind] = true
	}
	if !kinds["onboarding_discovery_requested"] || !kinds["onboarding_discovery_completed"] {
		t.Fatalf("discovery audit pair missing: %+v", snapshot.Audit)
	}
	if len(snapshot.Config.Hosts) != 1 {
		t.Fatal("read-only discovery changed ledger policy")
	}
}
