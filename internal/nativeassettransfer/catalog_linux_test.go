//go:build linux

package nativeassettransfer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAssetCatalogAppendPreservesCompletedAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "unknown"}[unknown], func(t *testing.T) {
			b1, _, _, _ := fixtureWithAssetID(t, strings.Repeat("a", 64))
			b2, _, _, _ := fixtureWithAssetID(t, strings.Repeat("b", 64))
			defer b1.Close()
			defer b2.Close()
			c1 := registryAssemblyFixture(t, b1.manifest, "one")
			c2 := registryAssemblyFixture(t, b2.manifest, "two")
			dir := protectedTemp(t)
			old, e := NewRegistry(dir, map[string]*Bundle{"one": b1})
			if e != nil {
				t.Fatal(e)
			}
			defer old.Close()
			v, e := old.Review("one", c1)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = old.Register("one", c1, v.ReviewDigest); e != nil {
				t.Fatal(e)
			}
			intentPath := filepath.Join(dir, "asset-slot-one-intent.json")
			original, _ := os.ReadFile(intentPath)
			if unknown {
				if os.Remove(filepath.Join(dir, "asset-slot-one-complete.json")) != nil {
					t.Fatal("fixture remove")
				}
			}
			next, e := NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2})
			if e != nil {
				t.Fatal(e)
			}
			defer next.Close()
			if e = next.AnchorCatalog(); e != nil {
				t.Fatal(e)
			}
			restored, e := next.Restore("one", c1)
			if unknown {
				if e == nil || next.State("one") != "consumed_unknown" {
					t.Fatal("unknown reservation freed")
				}
			} else if e != nil || restored.ReviewDigest() != v.TransferDigest {
				t.Fatal("old completion changed", e)
			}
			conflict := registryAssemblyFixture(t, b2.manifest, "one")
			cv, e := next.Review("two", conflict)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = next.Register("two", conflict, cv.ReviewDigest); e == nil {
				t.Fatal("historically consumed target reused across revisions")
			}
			nv, e := next.Review("two", c2)
			if e != nil {
				t.Fatal(e)
			}
			if nv.CatalogDigest == v.CatalogDigest {
				t.Fatal("catalog revision unchanged")
			}
			if _, e = next.Register("two", c2, nv.ReviewDigest); e != nil {
				t.Fatal("new approved slot refused", e)
			}
			after, _ := os.ReadFile(intentPath)
			if !bytes.Equal(after, original) {
				t.Fatal("historical bytes rewritten")
			}
			if _, e = next.Register("one", c1, v.ReviewDigest); e == nil {
				t.Fatal("old consumed identity reused")
			}
			if e = old.AnchorCatalog(); e == nil {
				t.Fatal("stale process accepted newer catalog")
			}
			if _, e = NewRegistry(dir, map[string]*Bundle{"two": b2}); e == nil {
				t.Fatal("old slot removed")
			}
			if os.Remove(filepath.Join(dir, catalogName(next.revision))) != nil {
				t.Fatal("fixture remove")
			}
			if _, e = NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2}); e == nil {
				t.Fatal("missing newer snapshot mistaken for legacy")
			}
		})
	}
}
func TestLegacyCatalogMustBeAnchoredBeforeAddition(t *testing.T) {
	b1, _, _, _ := fixtureWithAssetID(t, strings.Repeat("a", 64))
	b2, _, _, _ := fixtureWithAssetID(t, strings.Repeat("b", 64))
	defer b1.Close()
	defer b2.Close()
	c := registryAssemblyFixture(t, b1.manifest)
	dir := protectedTemp(t)
	old, e := NewRegistry(dir, map[string]*Bundle{"one": b1})
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	v, _ := old.Review("one", c)
	if _, e = old.Register("one", c, v.ReviewDigest); e != nil {
		t.Fatal(e)
	}
	// Existing v1 intents predate catalog snapshot files; retain their exact bytes.
	if os.Remove(filepath.Join(dir, catalogName(old.revision))) != nil {
		t.Fatal("fixture remove")
	}
	if _, e = NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2}); e == nil {
		t.Fatal("inferred missing original baseline")
	}
	before, _ := os.ReadDir(dir)
	baseline, e := NewRegistry(dir, map[string]*Bundle{"one": b1})
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatal("constructor modified legacy state")
	}
	if e = baseline.AnchorCatalog(); e != nil {
		t.Fatal(e)
	}
	next, e := NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2})
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e = next.Restore("one", c); e != nil {
		t.Fatal("legacy recovery changed", e)
	}
}
func TestCatalogProtectsUnusedEntriesAndRejectsMalformedSnapshot(t *testing.T) {
	b1, _, _, _ := fixtureWithAssetID(t, strings.Repeat("a", 64))
	b2, _, _, _ := fixtureWithAssetID(t, strings.Repeat("b", 64))
	defer b1.Close()
	defer b2.Close()
	dir := protectedTemp(t)
	r, e := NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.AnchorCatalog(); e != nil {
		t.Fatal(e)
	}
	if _, e = NewRegistry(dir, map[string]*Bundle{"one": b1}); e == nil {
		t.Fatal("forgot unused approved entry")
	}
	if _, e = NewRegistry(dir, map[string]*Bundle{"one": b1, "renamed": b2}); e == nil {
		t.Fatal("renamed entry")
	}
	if os.WriteFile(filepath.Join(dir, catalogName(r.revision)), []byte("{"), 0600) != nil {
		t.Fatal("fixture write")
	}
	if _, e = NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2}); e == nil {
		t.Fatal("repaired partial catalog")
	}
}

func TestConcurrentAssetCatalogBranchesHaveOneWinner(t *testing.T) {
	b1, _, _, _ := fixtureWithAssetID(t, strings.Repeat("a", 64))
	b2, _, _, _ := fixtureWithAssetID(t, strings.Repeat("b", 64))
	b3, _, _, _ := fixtureWithAssetID(t, strings.Repeat("c", 64))
	defer b1.Close()
	defer b2.Close()
	defer b3.Close()
	dir := protectedTemp(t)
	baseline, e := NewRegistry(dir, map[string]*Bundle{"one": b1})
	if e != nil {
		t.Fatal(e)
	}
	if e = baseline.AnchorCatalog(); e != nil {
		t.Fatal(e)
	}
	left, e := NewRegistry(dir, map[string]*Bundle{"one": b1, "two": b2})
	if e != nil {
		t.Fatal(e)
	}
	right, e := NewRegistry(dir, map[string]*Bundle{"one": b1, "three": b3})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, r := range []*Registry{left, right} {
		wg.Add(1)
		go func() { defer wg.Done(); out <- r.AnchorCatalog() }()
	}
	wg.Wait()
	close(out)
	winners := 0
	for e := range out {
		if e == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("catalog branches won %d times", winners)
	}
	if baseline.AnchorCatalog() == nil {
		t.Fatal("old process erased newer catalog")
	}
}
