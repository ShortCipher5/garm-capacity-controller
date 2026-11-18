package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"io"
	"strings"
)

type firstbootConfiguration struct {
	TokenFile   string                     `json:"token_file"`
	Credentials map[string]string          `json:"admin_credential_files"`
	Targets     map[string]firstbootTarget `json:"targets"`
}
type firstbootTarget struct {
	PreflightSHA256 string         `json:"preflight_sha256,omitempty"`
	Plan            firstboot.Plan `json:"plan"`
	Digest          string         `json:"review_digest"`
	CredentialAlias string         `json:"admin_credential_alias"`
	ManifestFile    string         `json:"manifest_file"`
	ArtifactFile    string         `json:"artifact_file"`
}

// loadFirstboot freezes artifact bytes and verifies admin keys locally. It never
// opens SSH and no request may choose these paths, aliases or plans.
func loadFirstboot(a *adminConfiguration) (map[string]firstboot.DeploymentTarget, firstboot.Resolver, string, error) {
	fail := func() (map[string]firstboot.DeploymentTarget, firstboot.Resolver, string, error) {
		return nil, nil, "", adminview.ErrConfiguration
	}
	c := a.Firstboot
	if c == nil || len(c.Targets) == 0 || len(c.Targets) > 8 || len(c.Credentials) == 0 || len(c.Credentials) > 8 {
		return fail()
	}
	raw, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return fail()
	}
	token := strings.TrimSpace(string(raw))
	paths := []string{a.TokenFile, a.ManagementTokenFile}
	if a.Bootstrap != nil {
		paths = append(paths, a.Bootstrap.TokenFile)
	}
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
	// Full-admin authority must not reuse any prepared-helper or inventory credential.
	if a.Bootstrap != nil {
		for _, t := range a.Bootstrap.Targets {
			k, e := hostbootstrap.PublicKey(t.Plan.InventoryKey)
			if e != nil {
				return fail()
			}
			inventory[ssh.FingerprintSHA256(k)] = true
		}
		r := sshinventory.PrivateKeyFiles(a.Bootstrap.Credentials)
		for alias := range a.Bootstrap.Credentials {
			k, e := r(context.Background(), alias)
			if e != nil {
				return fail()
			}
			inventory[ssh.FingerprintSHA256(k.PublicKey())] = true
		}
	}
	for _, t := range c.Targets {
		k, e := hostbootstrap.PublicKey(t.Plan.Base.InventoryKey)
		if e != nil {
			return fail()
		}
		inventory[ssh.FingerprintSHA256(k)] = true
	}
	targets := map[string]firstboot.DeploymentTarget{}
	for id, t := range c.Targets {
		if !admission.ValidOnboardingProfileID(id) || !admission.ValidOnboardingProfileID(t.CredentialAlias) {
			return fail()
		}
		b, e := securefiles.Read(t.ArtifactFile, hostbootstrap.MaxArtifact)
		if e != nil {
			return fail()
		}
		k, e := resolve(context.Background(), t.CredentialAlias)
		if e != nil || ssh.FingerprintSHA256(k.PublicKey()) != t.Plan.Base.AdminKeyFingerprint || inventory[t.Plan.Base.AdminKeyFingerprint] {
			return fail()
		}
		rawManifest, e := securefiles.Read(t.ManifestFile, 4096)
		if e != nil {
			return fail()
		}
		var manifest firstboot.Manifest
		d := json.NewDecoder(bytes.NewReader(rawManifest))
		d.DisallowUnknownFields()
		if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF || manifest != t.Plan.Manifest {
			return fail()
		}
		req := firstboot.Request{Plan: t.Plan, ReviewDigest: t.Digest, Artifact: b}
		if firstboot.Validate(req) != nil {
			return fail()
		}
		targets[id] = firstboot.DeploymentTarget{Request: req, CredentialAlias: t.CredentialAlias, PreflightSHA256: t.PreflightSHA256}
	}
	return targets, firstboot.Resolver(resolve), token, nil
}
