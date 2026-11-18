package main

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"strings"
)

func managedSettingsClient(a *adminConfiguration) (*managedsettings.Client, error) {
	m := a.GarmManagement
	if !m.ManagedSupervisor || m.DiscoveryOnly || m.TokenFile != "" || m.Journal != "" || m.JournalLineage != "" || len(m.RepositoryIDs) != 0 || m.QualifiedAdmissionRouting || m.ExclusiveSettingsWriter || m.ExperimentalConditionalSettings || a.EnableExperimentalGarmManagement && !a.EnableExperimentalManagement {
		return nil, adminview.ErrConfiguration
	}
	trust, e := securefiles.LoadTLS(m.TLS)
	if e != nil {
		return nil, adminview.ErrConfiguration
	}
	return managedsettings.New(managedsettings.Configuration{Endpoint: m.Endpoint, ServerKeyPin: m.ProxyServerKeyPin, ControllerID: m.ControllerID, TLS: trust, Providers: m.Providers, PoolIDs: m.PoolIDs, AllowReductions: a.EnableExperimentalGarmManagement})
}
func validateManagedSettingsRouting(c configuration) error {
	if c.Admin == nil || c.Admin.GarmManagement == nil || !c.Admin.GarmManagement.ManagedSupervisor {
		return adminview.ErrConfiguration
	}
	m := c.Admin.GarmManagement
	if !c.Garm.ManagedProxy || m.ControllerID != c.Controller || m.ProxyServerKeyPin != c.Garm.ProxyServerKeyPin || strings.TrimSuffix(m.Endpoint, "/") != strings.TrimSuffix(c.Garm.Endpoint, "/") {
		return adminview.ErrConfiguration
	}
	providers := map[string]bool{}
	for _, p := range c.Garm.Providers {
		providers[p] = true
	}
	for _, p := range m.Providers {
		if !providers[p] {
			return adminview.ErrConfiguration
		}
	}
	for _, p := range m.PoolIDs {
		if _, ok := c.Pools[p]; !ok {
			return adminview.ErrConfiguration
		}
	}
	return nil
}
