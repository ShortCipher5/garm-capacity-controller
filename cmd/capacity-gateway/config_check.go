package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	common "github.com/cloudbase/garm-provider-common/params"
)

var errStaticConfig = errors.New("static configuration check failed")

// checkStaticConfig checks only local syntax, credentials and explicit static
// constraints. It opens no database/journal/listener and makes no network call.
// It cannot establish persisted policy, reachability, fencing or readiness.
func checkStaticConfig(c configuration, provision bool) error {
	fail := func() error { return errStaticConfig }
	loopback := func(address string) bool {
		host, port, e := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		n, pe := strconv.Atoi(port)
		return e == nil && ip != nil && ip.IsLoopback() && pe == nil && n >= 0 && n <= 65535
	}
	if !loopback(c.Listen) || c.Controller == "" || strings.ContainsRune(c.Controller, 0) || c.Database == "" || c.IdentityFile == "" || len(c.Pools) == 0 || len(c.Hosts) == 0 {
		return fail()
	}
	if provision && (c.BootstrapMode != "gateway-owned-default-cloudinit/v1" || len(c.PreparationPools) != len(c.Pools) || len(c.SourceFingerprints) == 0) {
		return fail()
	}
	seenFP := map[string]bool{}
	for _, group := range [][]string{c.ProviderFingerprints, c.SourceFingerprints} {
		for _, fp := range group {
			raw, e := hex.DecodeString(fp)
			if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != fp || seenFP[fp] {
				return fail()
			}
			seenFP[fp] = true
		}
	}
	if len(c.ProviderFingerprints) == 0 {
		return fail()
	}
	if len(c.PreparationPools) > 0 {
		if len(c.PreparationPools) != len(c.Pools) {
			return fail()
		}
		providers := map[string]bool{}
		for _, v := range c.Garm.Providers {
			if v == "" || providers[v] {
				return fail()
			}
			providers[v] = true
		}
		allowed := map[string]bool{}
		for _, v := range c.SourceFingerprints {
			allowed[v] = true
		}
		for id, p := range c.PreparationPools {
			if _, ok := c.Pools[id]; !ok || p.Generation == nil || p.JITEnabled == nil || !providers[p.ProviderName] || len(p.SourceFingerprints) == 0 || len(p.SourceFingerprints) > 64 {
				return fail()
			}
			request := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: "source-policy-validation", PoolID: id, InstanceID: "11111111-1111-4111-8111-111111111111", Generation: *p.Generation, ProviderName: p.ProviderName, EntityType: p.EntityType, EntityID: p.EntityID, ForgeEndpointName: p.ForgeEndpointName, JITEnabled: *p.JITEnabled}
			if request.JITEnabled {
				request.AgentID = 1
			}
			if request.Validate() != nil {
				return fail()
			}
			local := map[string]bool{}
			for _, fp := range p.SourceFingerprints {
				raw, e := hex.DecodeString(fp)
				if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != fp || local[fp] || !allowed[fp] {
					return fail()
				}
				local[fp] = true
			}
		}
	}
	for id, h := range c.Hosts {
		if id == "" {
			return fail()
		}
		tls, e := securefiles.LoadTLS(h.TLS)
		if e != nil {
			return fail()
		}
		client, e := incusapi.New(h.Endpoint, h.Project, tls)
		if e != nil {
			return fail()
		}
		client.Close()
	}
	for id, p := range c.Pools {
		hosts := append([]string(nil), p.Hosts...)
		if p.Host != "" {
			if len(hosts) != 0 {
				return fail()
			}
			hosts = []string{p.Host}
		}
		if id == "" || len(hosts) == 0 || len(hosts) > 128 || p.Flavor == "" || p.Image == "" || (p.Arch != common.Amd64 && p.Arch != common.Arm64) || (p.Strategy != "" && p.Strategy != "first_fit" && p.Strategy != "least_runners") {
			return fail()
		}
		seen := map[string]bool{}
		for _, h := range hosts {
			if h == "" || seen[h] {
				return fail()
			}
			if _, ok := c.Hosts[h]; !ok {
				return fail()
			}
			seen[h] = true
		}
		if incusapi.ValidateSpec(incusapi.InstanceSpec{Name: "policy-validation", Type: p.Type, Profiles: p.Profiles, ImageFingerprint: p.Fingerprint, CPUMillis: p.CPU, RAMMiB: p.RAM, UserData: "validation", ControllerID: c.Controller, PoolID: id, ReservationKey: strings.Repeat("0", 64)}) != nil {
			return fail()
		}
	}
	token, e := securefiles.Read(c.Garm.TokenFile, 16384)
	if e != nil {
		return fail()
	}
	token = bytes.TrimSpace(token)
	if len(token) == 0 {
		return fail()
	}
	tls, e := securefiles.LoadTLS(c.Garm.TLS)
	if e != nil {
		return fail()
	}
	client, e := garmapi.New(c.Garm.Endpoint, string(token), tls, c.Garm.Providers, c.Garm.ScaleSets)
	if e != nil {
		return fail()
	}
	client.Close()
	server, e := securefiles.LoadTLS(c.ServerTLS)
	if e != nil {
		return fail()
	}
	if _, e = gatewayrpc.ServerTLS(server.Certificates[0], server.RootCAs); e != nil {
		return fail()
	}
	if validateGarmManagementRouting(c) != nil {
		return fail()
	}
	if c.Admin == nil {
		return nil
	}
	a := c.Admin
	if !loopback(a.Listen) {
		return fail()
	}
	adminToken, e := securefiles.Read(a.TokenFile, 256)
	if e != nil {
		return fail()
	}
	adminToken = bytes.TrimSpace(adminToken)
	validToken := func(v []byte) bool {
		raw, e := hex.DecodeString(string(v))
		return e == nil && len(raw) == 32 && hex.EncodeToString(raw) == string(v)
	}
	if !validToken(adminToken) {
		return fail()
	}
	if _, e = securefiles.LoadTLS(a.TLS); e != nil {
		return fail()
	}
	needsWrite := a.EnableExperimentalManagement || a.EnableExperimentalOnboardingProbes
	if needsWrite != (a.ManagementTokenFile != "") {
		return fail()
	}
	if needsWrite {
		write, e := securefiles.Read(a.ManagementTokenFile, 256)
		if e != nil {
			return fail()
		}
		write = bytes.TrimSpace(write)
		if !validToken(write) || bytes.Equal(write, adminToken) {
			return fail()
		}
	}
	if a.EnableExperimentalGarmManagement && (!a.EnableExperimentalManagement || a.GarmManagement == nil) {
		return fail()
	}
	if a.GarmManagement != nil {
		m := a.GarmManagement
		if a.EnableExperimentalGarmManagement && (!m.ExclusiveSettingsWriter || !m.QualifiedAdmissionRouting) {
			return fail()
		}
		digest, e := hex.DecodeString(m.JournalLineage)
		if e != nil || len(digest) != 32 || hex.EncodeToString(digest) != m.JournalLineage {
			return fail()
		}
		db, e := filepath.Abs(c.Database)
		if e != nil {
			return fail()
		}
		journal, e := filepath.Abs(m.Journal)
		if e != nil || m.Journal == "" || journal == db {
			return fail()
		}
		up, e := securefiles.Read(m.TokenFile, 16384)
		if e != nil {
			return fail()
		}
		up = bytes.TrimSpace(up)
		if len(up) == 0 || bytes.Equal(up, token) || bytes.Equal(up, adminToken) {
			return fail()
		}
		if a.ManagementTokenFile != "" {
			w, e := securefiles.Read(a.ManagementTokenFile, 256)
			if e != nil || bytes.Equal(up, bytes.TrimSpace(w)) {
				return fail()
			}
		}
		trust, e := securefiles.LoadTLS(m.TLS)
		if e != nil {
			return fail()
		}
		mc, e := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: m.Endpoint, Token: string(up), Trust: trust, Providers: m.Providers, PoolIDs: m.PoolIDs, RepositoryIDs: m.RepositoryIDs, AllowWrites: a.EnableExperimentalGarmManagement})
		if e != nil {
			return fail()
		}
		mc.Close()
	}
	if a.EnableExperimentalOnboardingProbes && a.IncusOnboarding == nil {
		return fail()
	}
	if a.IncusOnboarding != nil {
		if len(a.IncusOnboarding.Profiles) == 0 || len(a.IncusOnboarding.Profiles) > 32 {
			return fail()
		}
		for id, p := range a.IncusOnboarding.Profiles {
			if !admission.ValidOnboardingProfileID(id) {
				return fail()
			}
			trust, e := securefiles.LoadTLS(p.TLS)
			if e != nil {
				return fail()
			}
			ic, e := incusapi.New(p.Endpoint, p.Project, trust)
			if e != nil {
				return fail()
			}
			ic.Close()
		}
	}
	return nil
}
