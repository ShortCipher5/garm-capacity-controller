//go:build linux

package nativeassembly

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
)

func TestActualSavedSelectionBuildRetainsApprovedAssetReferenceAcrossRestart(t *testing.T) {
	_, planning, recipes, saved := fixture(t)
	m := nativeassets.Manifest{Version: nativeassets.Version, ID: strings.Repeat("d", 64), Architecture: "amd64", Files: map[string]nativeassets.Entry{}}
	for _, n := range nativeassets.Names() {
		size := int64(64)
		if n == "runtime.raw" {
			size = 64 << 20
		}
		m.Files[n] = nativeassets.Entry{SHA256: strings.Repeat("e", 64), Size: size}
	}
	digest, _ := nativeassets.ManifestDigest(m)
	r := recipes["host"]
	r.Build.RuntimeAssets = &nativebundle.AssetReference{Manifest: m, ManifestDigest: digest}
	recipes["host"] = r
	s, e := NewService(planning, recipes)
	if e != nil {
		t.Fatal(e)
	}
	// Caller mutation after construction cannot change the reviewed recipe.
	delete(m.Files, "nft")
	completed, e := s.Build(t.Context(), "host", saved.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	report := completed.Report()
	if report.RuntimeAssetID != strings.Repeat("d", 64) || report.RuntimeAssetManifestDigest != digest || report.ActivationAuthorized {
		t.Fatal(report)
	}
	reopened, e := NewService(planning, s.recipes)
	if e != nil {
		t.Fatal(e)
	}
	got, e := reopened.InspectCompleted(t.Context(), "host", saved.SelectionDigest)
	if e != nil || got.Report() != report {
		t.Fatal(got.Report(), e)
	}
	// A self-consistent altered report cannot detach the reviewed asset reference.
	recipe := s.recipes["host"]
	path := filepath.Join(recipe.OutputParent, recipe.stem(saved.SelectionDigest)+".receipt.json")
	record, e := readRecord(path)
	if e != nil {
		t.Fatal(e)
	}
	record.Report.RuntimeAssetID = strings.Repeat("f", 64)
	record.Report.AssemblyDigest = assemblyDigest(record)
	raw, _ := json.Marshal(record)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("write")
	}
	if _, e = reopened.InspectCompleted(t.Context(), "host", saved.SelectionDigest); e == nil {
		t.Fatal("accepted foreign asset report")
	}
}
