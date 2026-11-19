package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAssetsRequireExplicitModeAndDistinctToken(t *testing.T) {
	a := &adminConfiguration{NativeAssets: &nativeAssetsConfiguration{Targets: map[string]nativeAssetsRecipe{"one": {}}}}
	if _, _, close, e := loadNativeAssets(a); e == nil {
		close()
		t.Fatal("implicit asset mode")
	}
	a.EnableNativeAssets = true
	if _, _, close, e := loadNativeAssets(a); e == nil {
		close()
		t.Fatal("missing actual preparation workflow")
	}
	dir, e := os.MkdirTemp(".", ".asset-config-")
	if e != nil {
		t.Fatal(e)
	}
	dir, _ = filepath.Abs(dir)
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "token")
	os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0600)
	a.EnableNativePreparation = true
	a.NativeAssets.TokenFile = path
	a.TokenFile = path
	if _, _, close, e := loadNativeAssets(a); e == nil {
		close()
		t.Fatal("read token reused")
	}
	a.NativeAssets.TokenFile = filepath.Join(dir, "absent")
	if _, _, close, e := loadNativeAssets(a); e == nil {
		close()
		t.Fatal("missing protected token")
	}
}

func TestNativeAssetSlotConfigurationFailsClosed(t *testing.T) {
	if r, e := loadNativeAssetRegistry(&nativeAssetsConfiguration{}); e != nil || r != nil {
		t.Fatal("legacy empty slot configuration", e)
	}
	if _, e := loadNativeAssetRegistry(&nativeAssetsConfiguration{RegistryDirectory: "/not-configured"}); e == nil {
		t.Fatal("orphan registry accepted")
	}
	if _, e := loadNativeAssetRegistry(&nativeAssetsConfiguration{RegistryDirectory: "/not-configured", Slots: map[string]nativeAssetsSlot{"slot": {BundleDirectory: "/missing", HelperPath: "/missing"}}}); e == nil {
		t.Fatal("unverified slot sources accepted")
	}
}
