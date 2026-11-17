package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

// The wired transport profile must expose the full read-only discovery
// surface, not just the probe identity read: discovery through the configured
// runtime succeeds and degrades per section when a fixture serves no resource
// routes, instead of failing the DiscoverySource assertion before any read.
func TestConfiguredTransportProfileSupportsFullDiscoveryBehindFlag(t *testing.T) {
	fixture := testsupport.Identity(t, "private-fixture-discovery-daemon")
	raw, err := os.ReadFile(fixture.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	var transport struct {
		Endpoint string               `json:"endpoint"`
		TLS      securefiles.TLSFiles `json:"tls"`
	}
	if json.Unmarshal(raw, &transport) != nil {
		t.Fatal("fixture config")
	}
	profile := onboardingProfileConfiguration{Endpoint: transport.Endpoint, Project: "runners", TLS: transport.TLS}
	config := &adminConfiguration{IncusOnboarding: &onboardingConfiguration{Profiles: map[string]onboardingProfileConfiguration{"candidate-b": profile}}, EnableExperimentalOnboardingProbes: true}
	manager, close, err := newOnboardingControl(config, managementStore(t))
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	result, err := manager.Discover(context.Background(), onboarding.Request{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "candidate-b", Reason: "configured read-only sweep"})
	if err != nil {
		t.Fatal(err)
	}
	// The identity-only fixture cannot serve totals/inventory/state, so those
	// sections degrade to blockers; reaching them at all proves the concrete
	// configured client, not a reduced observer, backs the profile.
	if result.Profile != "candidate-b" || !result.Identity.ObservationAvailable || result.Identity.ServerVersion != "6.0.4" {
		t.Fatalf("wired identity section: %+v", result.Identity)
	}
	for _, blocker := range []string{"host_totals_unavailable", "guest_inventory_unavailable", "utilization_unavailable", "physical_capacity_scope_unqualified"} {
		found := false
		for _, got := range result.Blockers {
			if got == blocker {
				found = true
			}
		}
		if !found {
			t.Fatalf("wired discovery blocker %q missing: %v", blocker, result.Blockers)
		}
	}
	if result.Totals != nil || len(result.Guests) != 0 || result.Utilization != nil {
		t.Fatalf("unavailable sections fabricated data: %+v", result)
	}
	// The discovery gate rejects unknown profiles before any transport read.
	if _, err := manager.Discover(context.Background(), onboarding.Request{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "unknown-profile", Reason: "unconfigured sweep"}); !errors.Is(err, onboarding.ErrDenied) {
		t.Fatal("unconfigured profile contacted transport")
	}
}
