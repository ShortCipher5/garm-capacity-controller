package main

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"strings"
)

type nativePreparationConfiguration struct {
	TokenFile   string                             `json:"token_file"`
	Credentials map[string]string                  `json:"admin_credential_files"`
	Targets     map[string]nativePreparationTarget `json:"targets"`
}
type nativePreparationTarget struct {
	Target           nativeprovision.Target `json:"target"`
	CredentialAlias  string                 `json:"admin_credential_alias"`
	BundleDirectory  string                 `json:"bundle_directory"`
	JournalDirectory string                 `json:"journal_directory"`
	ReviewDigest     string                 `json:"review_digest"`
}

func loadNativePreparation(a *adminConfiguration) (map[string]adminview.NativePreparationTarget, string, func(), error) {
	services := []*nativeprovision.Service{}
	closeAll := func() {
		for _, s := range services {
			s.Close()
		}
	}
	fail := func() (map[string]adminview.NativePreparationTarget, string, func(), error) {
		closeAll()
		return nil, "", func() {}, adminview.ErrConfiguration
	}
	c := a.NativePreparation
	if c == nil || len(c.Targets) < 1 || len(c.Targets) > 8 || len(c.Credentials) < 1 || len(c.Credentials) > 8 {
		return fail()
	}
	raw, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return fail()
	}
	token := strings.TrimSpace(string(raw))
	// This is a separate privilege from both normal management and restricted
	// inventory/helper access. Full-admin firstboot credentials may be reused.
	paths := []string{a.TokenFile, a.ManagementTokenFile}
	if a.Firstboot != nil {
		paths = append(paths, a.Firstboot.TokenFile)
	}
	if a.Bootstrap != nil {
		paths = append(paths, a.Bootstrap.TokenFile)
	}
	if a.SSHInventory != nil {
		paths = append(paths, a.SSHInventory.TokenFile, a.SSHInventory.TargetManagementTokenFile)
	}
	for _, path := range paths {
		if path != "" {
			v, e := securefiles.Read(path, 256)
			if e != nil || strings.TrimSpace(string(v)) == token {
				return fail()
			}
		}
	}
	forbidden := map[string]bool{}
	if a.SSHInventory != nil {
		resolve := sshinventory.PrivateKeyFiles(a.SSHInventory.Keys)
		for alias := range a.SSHInventory.Keys {
			k, e := resolve(context.Background(), alias)
			if e != nil {
				return fail()
			}
			forbidden[ssh.FingerprintSHA256(k.PublicKey())] = true
		}
	}
	if a.Bootstrap != nil {
		resolve := sshinventory.PrivateKeyFiles(a.Bootstrap.Credentials)
		for alias := range a.Bootstrap.Credentials {
			k, e := resolve(context.Background(), alias)
			if e != nil {
				return fail()
			}
			forbidden[ssh.FingerprintSHA256(k.PublicKey())] = true
		}
	}
	if a.Bootstrap != nil {
		for _, target := range a.Bootstrap.Targets {
			k, e := hostbootstrap.PublicKey(target.Plan.InventoryKey)
			if e != nil {
				return fail()
			}
			forbidden[ssh.FingerprintSHA256(k)] = true
		}
	}
	if a.Firstboot != nil {
		for _, target := range a.Firstboot.Targets {
			k, e := hostbootstrap.PublicKey(target.Plan.Base.InventoryKey)
			if e != nil {
				return fail()
			}
			forbidden[ssh.FingerprintSHA256(k)] = true
		}
	}
	resolve := sshinventory.PrivateKeyFiles(c.Credentials)
	targets := map[string]adminview.NativePreparationTarget{}
	seenID := map[string]bool{}
	for id, t := range c.Targets {
		if !admission.ValidOnboardingProfileID(id) || !admission.ValidOnboardingProfileID(t.CredentialAlias) {
			return fail()
		}
		key, e := resolve(context.Background(), t.CredentialAlias)
		if e != nil || forbidden[ssh.FingerprintSHA256(key.PublicKey())] || ssh.FingerprintSHA256(key.PublicKey()) != t.Target.AdminKeyFingerprint {
			return fail()
		}
		bundle, e := nativeprovision.LoadBundle(t.BundleDirectory)
		if e != nil {
			return fail()
		}
		service, e := nativeprovision.NewService(bundle, t.Target, t.JournalDirectory)
		if e != nil {
			bundle.Close()
			return fail()
		}
		services = append(services, service)
		view, e := service.View(id)
		if e != nil || seenID[view.PreparationID] || service.ReviewDigest() != t.ReviewDigest {
			return fail()
		}
		seenID[view.PreparationID] = true
		alias := t.CredentialAlias
		targets[id] = adminview.NativePreparationTarget{Service: service, Resolve: func() (ssh.Signer, error) { return resolve(context.Background(), alias) }}
	}
	return targets, token, closeAll, nil
}
