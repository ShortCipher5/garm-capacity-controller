//go:build linux

package nativeassettransfer

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassembly"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRegistryRealAssemblyOneUseAndRestart(t *testing.T) {
	b, _, _, _ := fixture(t)
	c := registryAssemblyFixture(t, b.manifest)
	dir := protectedTemp(t)
	r, e := NewRegistry(dir, map[string]*Bundle{"slot": b})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	before, _ := os.ReadDir(dir)
	v, e := r.Review("slot", c)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatal("review wrote files")
	}
	if _, e = r.Register("slot", c, strings.Repeat("f", 64)); e == nil {
		t.Fatal("stale review accepted")
	}
	if _, e = r.Review("slot", nativeassembly.Completed{}); e == nil {
		t.Fatal("fabricated completion accepted")
	}
	other, e := NewRegistry(dir, map[string]*Bundle{"slot": b})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan *Service, 2)
	for _, registry := range []*Registry{r, other} {
		wg.Add(1)
		go func() { defer wg.Done(); s, _ := registry.Register("slot", c, v.ReviewDigest); results <- s }()
	}
	wg.Wait()
	close(results)
	var winner *Service
	count := 0
	for s := range results {
		if s != nil {
			winner = s
			count++
		}
	}
	if count != 1 {
		t.Fatalf("winners %d", count)
	}
	if winner.ReviewDigest() != v.TransferDigest || r.State("slot") != "registered" {
		t.Fatal("wrong registered view")
	}
	state, e := winner.Status()
	if e != nil || state.Attempt != nil || state.Receipt != nil {
		t.Fatal("registration initiated transfer", state, e)
	}
	reopened, e := NewRegistry(dir, map[string]*Bundle{"slot": b})
	if e != nil {
		t.Fatal(e)
	}
	restored, e := reopened.Restore("slot", c)
	if e != nil || restored.ReviewDigest() != winner.ReviewDigest() {
		t.Fatal(e)
	}
	if _, e = reopened.Register("slot", c, v.ReviewDigest); e == nil {
		t.Fatal("replayed registration")
	}
	if _, e = NewRegistry(dir, map[string]*Bundle{"renamed": b}); e == nil {
		t.Fatal("discarded history on catalog rename")
	}
	otherAssembly := registryAssemblyFixture(t, b.manifest)
	if _, e = reopened.Restore("slot", otherAssembly); e == nil {
		t.Fatal("restored foreign exact assembly")
	}
	// A partial reservation survives restart and cannot be retried or restored.
	if os.Remove(filepath.Join(dir, "asset-slot-slot-complete.json")) != nil {
		t.Fatal("fixture remove")
	}
	unknown, e := NewRegistry(dir, map[string]*Bundle{"slot": b})
	if e != nil {
		t.Fatal(e)
	}
	if unknown.State("slot") != "consumed_unknown" {
		t.Fatal("released interrupted slot")
	}
	if _, e = unknown.Register("slot", c, v.ReviewDigest); e == nil {
		t.Fatal("retried interrupted slot")
	}
	if _, e = unknown.Restore("slot", c); e == nil {
		t.Fatal("restored interrupted slot")
	}
	reopened.Close()
}
func TestRegistryRejectsMalformedHistoryAndDuplicateID(t *testing.T) {
	b, _, _, _ := fixture(t)
	defer b.Close()
	dir := protectedTemp(t)
	if _, e := NewRegistry(dir, map[string]*Bundle{"one": b, "two": b}); e == nil {
		t.Fatal("duplicate asset identity")
	}
	if e := os.WriteFile(filepath.Join(dir, "asset-slot-one-intent.json"), []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := NewRegistry(dir, map[string]*Bundle{"one": b}); e == nil {
		t.Fatal("malformed history accepted")
	}
}
