package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"strings"
)

type bootstrapConfiguration struct {
	TokenFile   string                     `json:"token_file"`
	Credentials map[string]string          `json:"admin_credential_files"`
	Targets     map[string]bootstrapTarget `json:"targets"`
}
type bootstrapTarget struct {
	Plan            hostbootstrap.Plan `json:"plan"`
	Digest          string             `json:"review_digest"`
	CredentialAlias string             `json:"admin_credential_alias"`
	ArtifactFile    string             `json:"artifact_file"`
}

// loadBootstrap freezes artifact bytes and verifies admin keys locally. It never
// opens SSH and no request may choose these paths, aliases or plans.
func loadBootstrap(a *adminConfiguration) (map[string]hostbootstrap.DeploymentTarget, hostbootstrap.Resolver, string, error) {
	fail := func() (map[string]hostbootstrap.DeploymentTarget, hostbootstrap.Resolver, string, error) {
		return nil, nil, "", adminview.ErrConfiguration
	}
	c := a.Bootstrap
	if c == nil || len(c.Targets) == 0 || len(c.Targets) > 8 || len(c.Credentials) == 0 || len(c.Credentials) > 8 {
		return fail()
	}
	raw, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return fail()
	}
	token := strings.TrimSpace(string(raw))
	paths := []string{a.TokenFile, a.ManagementTokenFile}
	if a.SSHInventory != nil {
		paths = append(paths, a.SSHInventory.TokenFile, a.SSHInventory.TargetManagementTokenFile)
	}
	for _, p := range paths {
		if p != "" {
			v, e := securefiles.Read(p, 256)
			if e != nil || strings.TrimSpace(string(v)) == token {
				return fail()
			}
		}
	}
	resolve := sshinventory.PrivateKeyFiles(c.Credentials)
	inventory := map[string]bool{}
	if a.SSHInventory != nil {
		r := sshinventory.PrivateKeyFiles(a.SSHInventory.Keys)
		for alias := range a.SSHInventory.Keys {
			k, e := r(context.Background(), alias)
			if e != nil {
				return fail()
			}
			inventory[ssh.FingerprintSHA256(k.PublicKey())] = true
		}
	}
	for _, t := range c.Targets {
		k, e := hostbootstrap.PublicKey(t.Plan.InventoryKey)
		if e != nil {
			return fail()
		}
		inventory[ssh.FingerprintSHA256(k)] = true
	}
	targets := map[string]hostbootstrap.DeploymentTarget{}
	for id, t := range c.Targets {
		if !admission.ValidOnboardingProfileID(id) || !admission.ValidOnboardingProfileID(t.CredentialAlias) {
			return fail()
		}
		b, e := securefiles.Read(t.ArtifactFile, hostbootstrap.MaxArtifact)
		if e != nil {
			return fail()
		}
		k, e := resolve(context.Background(), t.CredentialAlias)
		if e != nil || ssh.FingerprintSHA256(k.PublicKey()) != t.Plan.AdminKeyFingerprint || inventory[t.Plan.AdminKeyFingerprint] {
			return fail()
		}
		req := hostbootstrap.Request{Plan: t.Plan, ReviewDigest: t.Digest, Artifact: b}
		if hostbootstrap.Validate(req) != nil {
			return fail()
		}
		targets[id] = hostbootstrap.DeploymentTarget{Request: req, CredentialAlias: t.CredentialAlias}
	}
	return targets, hostbootstrap.Resolver(resolve), token, nil
}
