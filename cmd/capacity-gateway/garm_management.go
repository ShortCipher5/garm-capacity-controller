package main

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// This operator-only configuration never comes from a browser or job payload.
// The explicit acknowledgement is not an implemented external writer fence.
type garmManagementConfiguration struct {
	Endpoint       string               `json:"endpoint"`
	TokenFile      string               `json:"token_file"`
	TLS            securefiles.TLSFiles `json:"tls"`
	Providers      []string             `json:"providers"`
	PoolIDs        []string             `json:"pool_ids"`
	RepositoryIDs  []string             `json:"repository_ids"`
	JournalLineage string               `json:"journal_lineage"`
	Journal        string               `json:"journal"`
	// Acknowledges separate installed qualification of ALL affected demand,
	// including every pool/provider a managed repository balancer can select,
	// not only the console's pool_ids. This field does not prove that graph.
	QualifiedAdmissionRouting bool `json:"qualified_admission_routing"`
	ExclusiveSettingsWriter   bool `json:"exclusive_settings_writer"`
}

func newGarmControl(config *adminConfiguration, inventoryToken, ledger string) (adminview.GarmManager, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, noop, nil
	}
	if config.GarmManagement == nil {
		if config.EnableExperimentalGarmManagement {
			return nil, noop, adminview.ErrConfiguration
		}
		return nil, noop, nil
	}
	remote := config.GarmManagement
	lineage, err := hex.DecodeString(remote.JournalLineage)
	if err != nil || len(lineage) != 32 || remote.JournalLineage != strings.ToLower(remote.JournalLineage) {
		return nil, noop, adminview.ErrConfiguration
	}
	if config.EnableExperimentalGarmManagement && (!config.EnableExperimentalManagement || !remote.ExclusiveSettingsWriter || !remote.QualifiedAdmissionRouting) {
		return nil, noop, adminview.ErrConfiguration
	}
	ledgerPath, err := filepath.Abs(ledger)
	if err != nil {
		return nil, noop, adminview.ErrConfiguration
	}
	journalPath, err := filepath.Abs(remote.Journal)
	if err != nil || remote.Journal == "" || journalPath == ledgerPath {
		return nil, noop, adminview.ErrConfiguration
	}
	token, err := securefiles.Read(remote.TokenFile, 16384)
	if err != nil {
		return nil, noop, adminview.ErrConfiguration
	}
	token = bytes.TrimSpace(token)
	if inventoryToken == "" || string(token) == strings.TrimSpace(inventoryToken) {
		return nil, noop, adminview.ErrConfiguration
	}
	for _, path := range []string{config.TokenFile, config.ManagementTokenFile} {
		if path == "" {
			continue
		}
		browserToken, err := securefiles.Read(path, 256)
		if err != nil || bytes.Equal(token, bytes.TrimSpace(browserToken)) {
			return nil, noop, adminview.ErrConfiguration
		}
	}
	trust, err := securefiles.LoadTLS(remote.TLS)
	if err != nil {
		return nil, noop, adminview.ErrConfiguration
	}
	client, err := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: remote.Endpoint, Token: string(token), Trust: trust, Providers: remote.Providers, PoolIDs: remote.PoolIDs, RepositoryIDs: remote.RepositoryIDs, AllowWrites: config.EnableExperimentalGarmManagement})
	if err != nil {
		return nil, noop, adminview.ErrConfiguration
	}
	journal, err := garmmanagement.OpenJournal(remote.Journal, remote.JournalLineage)
	if err != nil {
		client.Close()
		return nil, noop, adminview.ErrConfiguration
	}
	manager, err := garmmanagement.New(client, journal)
	if err != nil {
		journal.Close()
		client.Close()
		return nil, noop, adminview.ErrConfiguration
	}
	adapter, err := adminview.NewJournalledGarm(manager, config.EnableExperimentalGarmManagement, remote.PoolIDs, remote.RepositoryIDs)
	if err != nil {
		journal.Close()
		client.Close()
		return nil, noop, err
	}
	return adapter, func() { client.Close(); journal.Close() }, nil
}

// validateGarmManagementRouting rejects obvious scope/placement mismatches at
// startup. Configuration agreement is not proof of the installed Garm binary,
// provider patch or exclusivity; those still require external qualification.
func validateGarmManagementRouting(config configuration) error {
	if config.Admin == nil || !config.Admin.EnableExperimentalGarmManagement {
		return nil
	}
	management := config.Admin.GarmManagement
	if management == nil || !management.QualifiedAdmissionRouting || strings.TrimSuffix(management.Endpoint, "/") != strings.TrimSuffix(config.Garm.Endpoint, "/") {
		return adminview.ErrConfiguration
	}
	providers := map[string]bool{}
	for _, id := range config.Garm.Providers {
		providers[id] = true
	}
	for _, id := range management.Providers {
		if !providers[id] {
			return adminview.ErrConfiguration
		}
	}
	for _, id := range management.PoolIDs {
		if _, ok := config.Pools[id]; !ok {
			return adminview.ErrConfiguration
		}
	}
	return nil
}
