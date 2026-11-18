package main

import (
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassettransfer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type nativeAssetsConfiguration struct {
	TokenFile string                        `json:"token_file"`
	Targets   map[string]nativeAssetsRecipe `json:"targets"`
}
type nativeAssetsRecipe struct {
	PreparationID    string                     `json:"preparation_id"`
	Target           nativeassettransfer.Target `json:"target"`
	BundleDirectory  string                     `json:"bundle_directory"`
	HelperPath       string                     `json:"helper_path"`
	ManifestSHA256   string                     `json:"manifest_sha256"`
	HelperSHA256     string                     `json:"helper_sha256"`
	JournalDirectory string                     `json:"journal_directory"`
	ReviewDigest     string                     `json:"review_digest"`
}

func loadNativeAssets(a *adminConfiguration) (map[string]adminview.NativeAssetsTarget, string, func(), error) {
	services := []*nativeassettransfer.Service{}
	closeAll := func() {
		for _, s := range services {
			s.Close()
		}
	}
	fail := func() (map[string]adminview.NativeAssetsTarget, string, func(), error) {
		closeAll()
		return nil, "", func() {}, adminview.ErrConfiguration
	}
	if a == nil || !a.EnableNativeAssets || (!a.EnableNativePreparation && !a.EnableNativeRegistration) || a.NativeAssets == nil || len(a.NativeAssets.Targets) < 1 || len(a.NativeAssets.Targets) > 8 {
		return fail()
	}
	c := a.NativeAssets
	raw, e := securefiles.Read(c.TokenFile, 256)
	if e != nil {
		return fail()
	}
	token := strings.TrimSpace(string(raw))
	if !validNativeRegistrationToken(token) {
		return fail()
	}
	paths := []string{a.TokenFile, a.ManagementTokenFile}
	if a.NativePreparation != nil {
		paths = append(paths, a.NativePreparation.TokenFile)
	}
	if a.NativePlanning != nil {
		paths = append(paths, a.NativePlanning.TokenFile)
	}
	if a.NativeRegistration != nil {
		paths = append(paths, a.NativeRegistration.PlanningTokenFile, a.NativeRegistration.PreparationTokenFile)
	}
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
	for _, path := range paths {
		if path != "" {
			v, e := securefiles.Read(path, 256)
			if e != nil || strings.TrimSpace(string(v)) == token {
				return fail()
			}
		}
	}
	targets := map[string]adminview.NativeAssetsTarget{}
	seen := map[string]bool{}
	for id, r := range c.Targets {
		if !admission.ValidOnboardingProfileID(id) || !validNativeRegistrationToken(r.PreparationID) {
			return fail()
		}
		b, e := nativeassettransfer.LoadBundle(r.BundleDirectory, r.HelperPath, r.ManifestSHA256, r.HelperSHA256)
		if e != nil {
			return fail()
		}
		s, e := nativeassettransfer.NewService(b, r.Target, r.JournalDirectory)
		if e != nil {
			b.Close()
			return fail()
		}
		services = append(services, s)
		v, e := s.View(id)
		if e != nil || s.ReviewDigest() != r.ReviewDigest || seen[v.AssetID] {
			return fail()
		}
		seen[v.AssetID] = true
		targets[id] = adminview.NativeAssetsTarget{Service: s, PreparationID: r.PreparationID}
	}
	return targets, token, closeAll, nil
}
