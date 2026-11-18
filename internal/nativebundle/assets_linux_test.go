//go:build linux

package nativebundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
)

func assetReferenceFixture() *AssetReference {
	m := nativeassets.Manifest{Version: nativeassets.Version, ID: strings.Repeat("d", 64), Architecture: "amd64", Files: map[string]nativeassets.Entry{}}
	for _, n := range nativeassets.Names() {
		size := int64(64)
		if n == "runtime.raw" {
			size = 64 << 20
		}
		m.Files[n] = nativeassets.Entry{SHA256: digest([]byte(n)), Size: size}
	}
	d, _ := nativeassets.ManifestDigest(m)
	return &AssetReference{m, d}
}
func TestBuildApprovedAssetReferenceMapsEverySourceWithoutExecution(t *testing.T) {
	c, v := fixture(t)
	c.RuntimeAssets = assetReferenceFixture()
	v["storage"].(map[string]any)["QuotaBytes"] = json.Number("9007199254740993")
	writeConfig(t, c, v)
	r, e := Build(t.Context(), c)
	if e != nil || r.RuntimeAssetID != c.RuntimeAssets.Manifest.ID || r.RuntimeAssetManifestDigest != c.RuntimeAssets.ManifestDigest || r.ActivationAuthorized {
		t.Fatal(r, e)
	}
	raw, _ := os.ReadFile(filepath.Join(c.OutputDirectory, "capsule.json"))
	got, e := decodeObject(raw)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(nativeassets.Parent, c.RuntimeAssets.Manifest.ID)
	for _, x := range [][4]string{{"artifacts", "Image", "ImageSHA256", "runtime.raw"}, {"launch", "Nspawn", "NspawnSHA256", "systemd-nspawn"}, {"artifacts", "Binary", "BinarySHA256", "zfs"}, {"storage", "Binary", "BinarySHA256", "zfs"}, {"quota_reader", "Binary", "BinarySHA256", "zfs"}, {"network_tools", "IP", "IPSHA256", "ip"}, {"network_tools", "NFT", "NFTSHA256", "nft"}} {
		m, _ := object(got, x[0])
		if get(m, x[1]) != filepath.Join(root, x[3]) || get(m, x[2]) != c.RuntimeAssets.Manifest.Files[x[3]].SHA256 {
			t.Fatal(x, m)
		}
	}
	ctrl, _ := object(got, "control")
	pool, _ := object(ctrl, "pool_reader")
	if get(pool, "Binary") != filepath.Join(root, "zpool") || get(pool, "BinarySHA256") != c.RuntimeAssets.Manifest.Files["zpool"].SHA256 {
		t.Fatal(pool)
	}
	launch, _ := object(got, "launch")
	for _, k := range []string{"Image", "Registry", "StateParent"} {
		if x := get(launch, k); x != nil && x != "" {
			t.Fatal("fabricated live path", k, x)
		}
	}
	storage, _ := object(got, "storage")
	if get(storage, "QuotaBytes") != json.Number("9007199254740993") {
		t.Fatal("changed storage budget")
	}
	// No staged source path exists or is read by this offline compiler.
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("test must not use live staged assets", e)
	}
}
func TestAssetReferenceRejectsMismatchesBeforeOutput(t *testing.T) {
	for _, mode := range []string{"digest", "architecture", "imagepin", "toolpath", "toolpin", "extra", "id"} {
		t.Run(mode, func(t *testing.T) {
			c, v := fixture(t)
			c.RuntimeAssets = assetReferenceFixture()
			switch mode {
			case "digest":
				c.RuntimeAssets.ManifestDigest = strings.Repeat("a", 64)
			case "architecture":
				c.Architecture = "arm64"
			case "imagepin":
				v["launch"].(map[string]any)["ImageSHA256"] = strings.Repeat("a", 64)
			case "toolpath":
				v["storage"].(map[string]any)["Binary"] = "/usr/bin/zfs"
			case "toolpin":
				v["network_tools"].(map[string]any)["IPSHA256"] = strings.Repeat("a", 64)
			case "extra":
				c.RuntimeAssets.Manifest.Files["extra"] = nativeassets.Entry{SHA256: strings.Repeat("a", 64), Size: 1}
			case "id":
				c.RuntimeAssets.Manifest.ID = "../bad"
			}
			writeConfig(t, c, v)
			if _, e := Build(t.Context(), c); e == nil {
				t.Fatal("accepted mismatch")
			}
			if _, e := os.Stat(c.OutputDirectory); !os.IsNotExist(e) {
				t.Fatal("created output", e)
			}
		})
	}
}
func TestFrozenAssetReferenceOwnsManifest(t *testing.T) {
	r := assetReferenceFixture()
	f, e := FreezeAssetReference(r, "amd64")
	if e != nil {
		t.Fatal(e)
	}
	delete(r.Manifest.Files, "zfs")
	if _, e = FreezeAssetReference(f, "amd64"); e != nil {
		t.Fatal("retained caller map", e)
	}
}

func TestRegistrationFreezesAssetReferenceAndMaterializesLinkedBundle(t *testing.T) {
	catalog, request := registrationFixture(t)
	template := catalog.Templates["standard"]
	reference := assetReferenceFixture()
	template.Configuration.RuntimeAssets = reference
	raw, e := os.ReadFile(filepath.Join(template.Configuration.PrivateDirectory, "capsule.json"))
	if e != nil {
		t.Fatal(e)
	}
	v, e := decodeObject(raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range [][3]string{{"artifacts", "Image", "ImageSHA256"}, {"artifacts", "Binary", "BinarySHA256"}, {"storage", "Binary", "BinarySHA256"}} {
		m, _ := object(v, x[0])
		set(m, x[1], "")
		set(m, x[2], "")
	}
	writeConfig(t, template.Configuration, v)
	catalog.Templates["standard"] = template
	compiler, e := NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	request.Revision = compiler.Revision()
	delete(reference.Manifest.Files, "zfs")
	registration, e := compiler.Compile(request)
	if e != nil {
		t.Fatal(e)
	}
	metadata := registration.Metadata()
	assetID := metadata.Build.RuntimeAssets.Manifest.ID
	delete(metadata.Build.RuntimeAssets.Manifest.Files, "nft")
	metadata = registration.Metadata()
	if _, e = FreezeAssetReference(metadata.Build.RuntimeAssets, "amd64"); e != nil {
		t.Fatal("metadata leaked compiler map", e)
	}
	if e = registration.Materialize(t.Context()); e != nil {
		t.Fatal(e)
	}
	c := metadata.Build
	c.ExpectedBindingFile = ""
	c.OutputDirectory = filepath.Join(catalog.OutputParent, "bundle")
	result, e := Build(t.Context(), c)
	if e != nil || result.RuntimeAssetID != assetID {
		t.Fatal(result, e)
	}
}
