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

type rotationConfiguration struct {
	TokenFile   string                    `json:"token_file"`
	Credentials map[string]string         `json:"admin_credential_files"`
	Targets     map[string]rotationTarget `json:"targets"`
}
type rotationTarget struct {
	Plan         firstboot.ManagedRotationPlan `json:"plan"`
	Digest       string                        `json:"review_digest"`
	ArtifactFile string                        `json:"artifact_file"`
	ManifestFile string                        `json:"manifest_file"`
}

func loadManagedRotation(a *adminConfiguration) (map[string]firstboot.ManagedRotationTarget, firstboot.Resolver, firstboot.Resolver, string, error) {
	fail := func() (map[string]firstboot.ManagedRotationTarget, firstboot.Resolver, firstboot.Resolver, string, error) {
		return nil, nil, nil, "", adminview.ErrConfiguration
	}
	c := a.Rotation
	if c == nil || a.SSHInventory == nil || !a.EnableSSHTargetManagement || !a.EnableSSHInventory || len(c.Targets) == 0 || len(c.Targets) > 8 || len(c.Credentials) == 0 || len(c.Credentials) > 8 {
		return fail()
	}
	raw, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return fail()
	}
	token := strings.TrimSpace(string(raw))
	paths := []string{a.TokenFile, a.ManagementTokenFile, a.SSHInventory.TokenFile, a.SSHInventory.TargetManagementTokenFile}
	if a.Bootstrap != nil {
		paths = append(paths, a.Bootstrap.TokenFile)
	}
	if a.Firstboot != nil {
		paths = append(paths, a.Firstboot.TokenFile)
	}
	for _, p := range paths {
		if p != "" {
			b, e := securefiles.Read(p, 256)
			if e != nil || strings.TrimSpace(string(b)) == token {
				return fail()
			}
		}
	}
	admin := sshinventory.PrivateKeyFiles(c.Credentials)
	inventory := sshinventory.PrivateKeyFiles(a.SSHInventory.Keys)
	restricted := map[string]bool{}
	for alias := range a.SSHInventory.Keys {
		k, e := inventory(context.Background(), alias)
		if e != nil {
			return fail()
		}
		restricted[ssh.FingerprintSHA256(k.PublicKey())] = true
	}
	if a.Bootstrap != nil {
		r := sshinventory.PrivateKeyFiles(a.Bootstrap.Credentials)
		for alias := range a.Bootstrap.Credentials {
			k, e := r(context.Background(), alias)
			if e != nil {
				return fail()
			}
			restricted[ssh.FingerprintSHA256(k.PublicKey())] = true
		}
		for _, t := range a.Bootstrap.Targets {
			k, e := hostbootstrap.PublicKey(t.Plan.InventoryKey)
			if e != nil {
				return fail()
			}
			restricted[ssh.FingerprintSHA256(k)] = true
		}
	}
	if a.Firstboot != nil {
		for _, t := range a.Firstboot.Targets {
			k, e := hostbootstrap.PublicKey(t.Plan.Base.InventoryKey)
			if e != nil {
				return fail()
			}
			restricted[ssh.FingerprintSHA256(k)] = true
		}
	}
	for _, t := range c.Targets {
		for _, line := range []string{t.Plan.Remote.OldKey, t.Plan.Remote.NewKey} {
			k, e := hostbootstrap.PublicKey(line)
			if e != nil {
				return fail()
			}
			restricted[ssh.FingerprintSHA256(k)] = true
		}
	}
	targets := map[string]firstboot.ManagedRotationTarget{}
	for id, t := range c.Targets {
		if !admission.ValidOnboardingProfileID(id) || t.Digest != firstboot.ManagedRotationDigest(t.Plan) {
			return fail()
		}
		artifact, e := securefiles.Read(t.ArtifactFile, hostbootstrap.MaxArtifact)
		if e != nil {
			return fail()
		}
		raw, e := securefiles.Read(t.ManifestFile, 4096)
		if e != nil {
			return fail()
		}
		var manifest firstboot.Manifest
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF || manifest != t.Plan.Remote.Installation.Manifest {
			return fail()
		}
		k, e := admin(context.Background(), t.Plan.AdminAlias)
		if e != nil || ssh.FingerprintSHA256(k.PublicKey()) != t.Plan.Remote.Installation.Base.AdminKeyFingerprint || restricted[t.Plan.Remote.Installation.Base.AdminKeyFingerprint] {
			return fail()
		}
		if _, exists := a.SSHInventory.Keys[t.Plan.OldConfig.CredentialRef]; exists {
			old, e := inventory(context.Background(), t.Plan.OldConfig.CredentialRef)
			if e != nil || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(old.PublicKey()))) != strings.TrimSpace(t.Plan.Remote.OldKey) {
				return fail()
			}
		}
		n, e := inventory(context.Background(), t.Plan.NewConfig.CredentialRef)
		if e != nil || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(n.PublicKey()))) != strings.TrimSpace(t.Plan.Remote.NewKey) {
			return fail()
		}
		target := firstboot.ManagedRotationTarget{Plan: t.Plan, Artifact: artifact}
		if firstboot.ValidateManagedRotation(target) != nil {
			return fail()
		}
		targets[id] = target
	}
	return targets, firstboot.Resolver(admin), firstboot.Resolver(inventory), token, nil
}
