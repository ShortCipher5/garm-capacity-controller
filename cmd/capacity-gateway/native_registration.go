package main

import (
	"context"
	"encoding/hex"
	"os"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"golang.org/x/crypto/ssh"
)

type nativeRegistrationConfiguration struct {
	Catalog              nativebundle.RegistrationCatalog `json:"catalog"`
	Directory            string                           `json:"registration_directory"`
	SelectionDirectory   string                           `json:"selection_directory"`
	PlanningTokenFile    string                           `json:"planning_token_file"`
	PreparationTokenFile string                           `json:"preparation_token_file"`
	AdminCredentialFiles map[string]string                `json:"admin_credential_files"`
}

func nativeRegistrationModeAllowed(a *adminConfiguration) bool {
	return a != nil && (!a.EnableNativeRegistration || (a.EnableSSHInventory && a.SSHInventory != nil && a.NativeRegistration != nil && !a.EnableNativeAssembly && !a.EnableNativePlanning && !a.EnableNativePreparation && a.NativePlanning == nil && a.NativePreparation == nil))
}
func loadNativeRegistration(a *adminConfiguration) (string, string, map[string]func() (ssh.Signer, error), error) {
	fail := func() (string, string, map[string]func() (ssh.Signer, error), error) {
		return "", "", nil, adminview.ErrConfiguration
	}
	if !nativeRegistrationModeAllowed(a) || !a.EnableNativeRegistration {
		return fail()
	}
	c := a.NativeRegistration
	if nativeplanning.ValidateDirectory(c.Directory) != nil || nativeplanning.ValidateDirectory(c.SelectionDirectory) != nil || len(c.AdminCredentialFiles) != len(c.Catalog.Administrators) {
		return fail()
	}
	for _, path := range []string{c.Directory, c.SelectionDirectory} {
		st, e := os.Lstat(path)
		if e != nil || st.Mode() != os.ModeDir|0700 {
			return fail()
		}
	}
	// This reuses all static preparation token/restricted signer exclusions, without
	// creating a fake bundle or relaxing the static constructor's nonempty targets.
	preparation, resolve, forbidden, e := nativePreparationAccess(a, &nativePreparationConfiguration{TokenFile: c.PreparationTokenFile, Credentials: c.AdminCredentialFiles})
	if e != nil || !validNativeRegistrationToken(preparation) {
		return fail()
	}
	raw, e := securefiles.Read(c.PlanningTokenFile, 256)
	if e != nil {
		return fail()
	}
	planning := strings.TrimSpace(string(raw))
	if !validNativeRegistrationToken(planning) || planning == preparation {
		return fail()
	}
	// The handler repeats all token-role checks; config-check must catch them too.
	paths := []string{a.TokenFile, a.ManagementTokenFile}
	if a.SSHInventory != nil {
		paths = append(paths, a.SSHInventory.TokenFile, a.SSHInventory.TargetManagementTokenFile)
	}
	if a.Bootstrap != nil {
		paths = append(paths, a.Bootstrap.TokenFile)
	}
	if a.Firstboot != nil {
		paths = append(paths, a.Firstboot.TokenFile)
	}
	if a.Rotation != nil {
		paths = append(paths, a.Rotation.TokenFile)
	}
	for _, p := range paths {
		if p != "" {
			b, e := securefiles.Read(p, 256)
			if e != nil || (strings.TrimSpace(string(b)) == planning || strings.TrimSpace(string(b)) == preparation) {
				return fail()
			}
		}
	}
	owned := map[string]func() (ssh.Signer, error){}
	for alias, scope := range c.Catalog.Administrators {
		if !admission.ValidOnboardingProfileID(alias) || c.AdminCredentialFiles[alias] == "" {
			return fail()
		}
		key, e := resolve(context.Background(), alias)
		if e != nil {
			return fail()
		}
		fingerprint := ssh.FingerprintSHA256(key.PublicKey())
		if forbidden[fingerprint] || fingerprint != scope.AdminKeyFingerprint {
			return fail()
		}
		// Retain the parsed private signer, not a mutable path resolved after approval.
		signer := key
		owned[alias] = func() (ssh.Signer, error) { return signer, nil }
	}
	if _, e := nativebundle.NewRegistrationCompiler(c.Catalog); e != nil {
		return fail()
	}
	return planning, preparation, owned, nil
}

func validNativeRegistrationToken(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
