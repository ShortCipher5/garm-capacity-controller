package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// These transport references are distinct from enrolled backend/ledger hosts.
// No address/path/key may be supplied by a browser or a job payload.
type onboardingProfileConfiguration struct {
	Endpoint string               `json:"endpoint"`
	Project  string               `json:"project"`
	TLS      securefiles.TLSFiles `json:"tls"`
}
type onboardingConfiguration struct {
	Profiles map[string]onboardingProfileConfiguration `json:"profiles"`
}

func newOnboardingControl(config *adminConfiguration, store *admission.SQLiteStore) (*onboarding.Manager, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, noop, nil
	}
	if config.IncusOnboarding == nil {
		if config.EnableExperimentalOnboardingProbes {
			return nil, noop, adminview.ErrConfiguration
		}
		return nil, noop, nil
	}
	if store == nil {
		return nil, noop, adminview.ErrConfiguration
	}
	profiles := config.IncusOnboarding.Profiles
	if len(profiles) == 0 || len(profiles) > 32 {
		return nil, noop, adminview.ErrConfiguration
	}
	observers := map[string]onboarding.Observer{}
	clients := []*incusapi.Client{}
	closeAll := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	for id, profile := range profiles {
		if !admission.ValidOnboardingProfileID(id) || profile.Project == "" {
			closeAll()
			return nil, noop, adminview.ErrConfiguration
		}
		credentials, err := securefiles.LoadTLS(profile.TLS)
		if err != nil {
			closeAll()
			return nil, noop, adminview.ErrConfiguration
		}
		client, err := incusapi.New(profile.Endpoint, profile.Project, credentials)
		if err != nil {
			closeAll()
			return nil, noop, adminview.ErrConfiguration
		}
		clients = append(clients, client)
		observers[id] = client
	}
	manager, err := onboarding.New(store, observers, config.EnableExperimentalOnboardingProbes)
	if err != nil {
		closeAll()
		return nil, noop, adminview.ErrConfiguration
	}
	return manager, closeAll, nil
}
