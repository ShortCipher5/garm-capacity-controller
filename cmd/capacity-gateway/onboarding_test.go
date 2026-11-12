package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestOnboardingRuntimeUsesOnlyPrivateConfiguredTransportAndExplicitProbeOptIn(t *testing.T) {
	fixture := testsupport.Identity(t, "private-fixture-onboarding-daemon")
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
	config := &adminConfiguration{IncusOnboarding: &onboardingConfiguration{Profiles: map[string]onboardingProfileConfiguration{"candidate-b": profile}}}
	store := managementStore(t)
	manager, close, err := newOnboardingControl(config, store)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Discovery().ProbeEnabled || manager.Discovery().CapacityQualified {
		t.Fatal("read-only config enabled probing or qualification")
	}
	if _, err := manager.Probe(context.Background(), onboarding.Request{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "candidate-b", Reason: "disabled"}); err != onboarding.ErrDisabled {
		t.Fatal("disabled runtime probe allowed")
	}
	close()
	config.EnableExperimentalOnboardingProbes = true
	manager, close, err = newOnboardingControl(config, store)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	result, err := manager.Probe(context.Background(), onboarding.Request{RequestID: "11111111-1111-4111-8111-111111111111", ProfileID: "candidate-b", Reason: "explicit configured read"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ObservationAvailable || result.ServerVersion != "6.0.4" || result.EnrollmentPerformed || result.ActivationPerformed || result.CapacityQualified {
		t.Fatal("runtime preflight overstated outcome")
	}
	config.IncusOnboarding.Profiles = map[string]onboardingProfileConfiguration{"https://not-a-ref.invalid": profile}
	if _, close, err := newOnboardingControl(config, store); err == nil {
		close()
		t.Fatal("arbitrary URL used as profile ref")
	}
	config.IncusOnboarding.Profiles = map[string]onboardingProfileConfiguration{"candidate-b": {Endpoint: "http://not-https.invalid", Project: profile.Project, TLS: profile.TLS}}
	if _, close, err := newOnboardingControl(config, store); err == nil {
		close()
		t.Fatal("unverified HTTP target configured")
	}
}

func TestProbeOnlyRuntimeNeverEnablesLedgerPolicyWrites(t *testing.T) {
	keys := credentials(t)
	store := managementStore(t)
	dir := t.TempDir()
	readPath, writePath := filepath.Join(dir, "read.token"), filepath.Join(dir, "probe.token")
	for path, token := range map[string]string{readPath: strings.Repeat("a", 64), writePath: strings.Repeat("c", 64)} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := onboarding.New(store, map[string]onboarding.Observer{"candidate-b": nilObserver{}}, true)
	if err != nil {
		t.Fatal(err)
	}
	config := &adminConfiguration{Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: readPath, ManagementTokenFile: writePath, IncusOnboarding: &onboardingConfiguration{}, onboardingRuntime: manager, EnableExperimentalOnboardingProbes: true}
	server, close, err := newAdminServer(config, store, false)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/policy", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("c", 64))
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("probe flag enabled policy writes")
	}
	for _, token := range []string{"", strings.Repeat("a", 64), "invalid"} {
		if err := os.WriteFile(writePath, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		if _, close, err := newAdminServer(config, store, false); err == nil {
			close()
			t.Fatal("invalid probe credential accepted")
		}
	}
}

type nilObserver struct{}

func (nilObserver) PhysicalIdentity(context.Context) (incusapi.PhysicalServerIdentity, error) {
	return incusapi.PhysicalServerIdentity{}, nil
}
