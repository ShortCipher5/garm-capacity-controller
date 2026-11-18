package main

import (
	"context"
	"crypto/tls"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// These transport references are distinct from enrolled backend/ledger hosts.
// No address/path/key may be supplied by a browser or a job payload.
type onboardingProfileConfiguration struct {
	ServerKeySHA256 string               `json:"server_key_sha256,omitempty"`
	Endpoint        string               `json:"endpoint"`
	Project         string               `json:"project"`
	TLS             securefiles.TLSFiles `json:"tls"`
}
type onboardingConfiguration struct {
	Targets  map[string]onboardingProfileConfiguration `json:"connection_targets,omitempty"`
	Profiles map[string]onboardingProfileConfiguration `json:"profiles"`
}

func newOnboardingControl(config *adminConfiguration, store *admission.SQLiteStore) (*onboarding.Manager, func(), error) {
	noop := func() {}
	// Every startup is a new authority-policy epoch, including withdrawing all
	// dynamic targets or disabling onboarding entirely.
	if store != nil {
		if err := store.InvalidateConnectionReviews(context.Background()); err != nil {
			return nil, noop, adminview.ErrConfiguration
		}
	}

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
	if (len(profiles) == 0 && len(config.IncusOnboarding.Targets) == 0) || len(profiles) > 32 || len(config.IncusOnboarding.Targets) > 32 {
		return nil, noop, adminview.ErrConfiguration
	}
	persisted, readErr := store.Connections(context.Background())
	if readErr != nil {
		return nil, noop, adminview.ErrConfiguration
	}
	for _, c := range persisted {
		if _, exists := profiles[c.ID]; exists {
			return nil, noop, adminview.ErrConfiguration
		}
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
		credentials, err := onboardingTLS(profile)
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
	var connections *onboarding.ConnectionService
	if len(config.IncusOnboarding.Targets) > 0 {
		targets := map[string]onboarding.ConnectionTarget{}
		for id, target := range config.IncusOnboarding.Targets {
			if !admission.ValidConnectionEndpoint(target.Endpoint) {
				closeAll()
				return nil, noop, adminview.ErrConfiguration
			}
			trust, err := onboardingTLS(target)
			if err != nil {
				closeAll()
				return nil, noop, adminview.ErrConfiguration
			}
			checked, err := incusapi.New(target.Endpoint, target.Project, trust)
			if err != nil {
				closeAll()
				return nil, noop, adminview.ErrConfiguration
			}
			checked.Close()
			targets[id] = onboarding.ConnectionTarget{Endpoint: target.Endpoint, Project: target.Project, ExpectedKey: target.ServerKeySHA256, Factory: func(c admission.Connection) (onboarding.Observer, func(), error) {
				configured := target
				configured.ServerKeySHA256 = c.ServerKeySHA256
				credentials, err := onboardingTLS(configured)
				if err != nil {
					return nil, nil, err
				}
				client, err := incusapi.New(c.Endpoint, c.Project, credentials)
				if err != nil {
					return nil, nil, err
				}
				return client, client.Close, nil
			}}
		}
		ids := []string{}
		for id := range observers {
			ids = append(ids, id)
		}
		var err error
		connections, err = onboarding.NewConnectionService(store, targets, ids)
		if err != nil {
			closeAll()
			return nil, noop, adminview.ErrConfiguration
		}
	}
	var manager *onboarding.Manager
	var err error
	if len(observers) == 0 {
		manager, err = onboarding.NewDynamic(store, connections, config.EnableExperimentalOnboardingProbes)
	} else {
		manager, err = onboarding.New(store, observers, config.EnableExperimentalOnboardingProbes)
		if err == nil {
			manager.SetConnections(connections)
		}
	}

	if err != nil {
		closeAll()
		return nil, noop, adminview.ErrConfiguration
	}
	return manager, closeAll, nil
}

// onboardingTLS is shared with the offline config checker so runtime cannot
// bypass a malformed or rejected pin accepted by a different validation path.
func onboardingTLS(profile onboardingProfileConfiguration) (*tls.Config, error) {
	credentials, err := securefiles.LoadTLS(profile.TLS)
	if err != nil {
		return nil, err
	}
	if profile.ServerKeySHA256 != "" {
		return securefiles.WithServerKeyPin(credentials, profile.ServerKeySHA256)
	}
	return credentials, nil
}
