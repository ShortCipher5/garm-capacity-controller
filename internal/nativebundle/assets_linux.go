//go:build linux

package nativebundle

import (
	"path/filepath"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
)

// AssetReference is approved configuration, never evidence that files exist or
// that an image, toolchain, host or execution boundary is qualified.
type AssetReference struct {
	Manifest       nativeassets.Manifest `json:"manifest"`
	ManifestDigest string                `json:"manifest_digest"`
}

// FreezeAssetReference validates and owns the manifest map. Nil preserves the
// legacy explicitly configured source paths without changing recipe digests.
func FreezeAssetReference(r *AssetReference, architecture string) (*AssetReference, error) {
	if r == nil {
		return nil, nil
	}
	d, e := nativeassets.ManifestDigest(r.Manifest)
	if e != nil || d != r.ManifestDigest || r.Manifest.Architecture != architecture {
		return nil, ErrBundle
	}
	owned := *r
	owned.Manifest.Files = make(map[string]nativeassets.Entry, len(r.Manifest.Files))
	for n, entry := range r.Manifest.Files {
		owned.Manifest.Files[n] = entry
	}
	return &owned, nil
}

func applyAssetReference(config map[string]any, r *AssetReference, architecture string) error {
	r, e := FreezeAssetReference(r, architecture)
	if e != nil || r == nil {
		return e
	}
	root := filepath.Join(nativeassets.Parent, r.Manifest.ID)
	// These are source paths only. Launch.Image and all live artifact/registry
	// paths remain empty, exclusively derived by the real owner constructors.
	mappings := []struct{ section, path, hash, name string }{
		{"artifacts", "Image", "ImageSHA256", "runtime.raw"},
		{"launch", "Nspawn", "NspawnSHA256", "systemd-nspawn"},
		{"artifacts", "Binary", "BinarySHA256", "zfs"},
		{"storage", "Binary", "BinarySHA256", "zfs"},
		{"quota_reader", "Binary", "BinarySHA256", "zfs"},
		{"network_tools", "IP", "IPSHA256", "ip"},
		{"network_tools", "NFT", "NFTSHA256", "nft"},
	}
	bind := func(m map[string]any, key, expected string) error {
		if old := get(m, key); old != nil && old != "" && old != expected {
			return ErrBundle
		}
		set(m, key, expected)
		return nil
	}
	for _, x := range mappings {
		m, e := object(config, x.section)
		if e != nil || bind(m, x.path, filepath.Join(root, x.name)) != nil || bind(m, x.hash, r.Manifest.Files[x.name].SHA256) != nil {
			return ErrBundle
		}
	}
	launch, e := object(config, "launch")
	if e != nil || bind(launch, "ImageSHA256", r.Manifest.Files["runtime.raw"].SHA256) != nil {
		return ErrBundle
	}
	control, e := object(config, "control")
	if e != nil {
		return ErrBundle
	}
	pool, e := object(control, "pool_reader")
	if e != nil || bind(pool, "Binary", filepath.Join(root, "zpool")) != nil || bind(pool, "BinarySHA256", r.Manifest.Files["zpool"].SHA256) != nil {
		return ErrBundle
	}
	return nil
}
