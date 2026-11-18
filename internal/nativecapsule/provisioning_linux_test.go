//go:build linux

package nativecapsule

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProvisionJournalExclusiveExactAndPermanent(t *testing.T) {
	d := t.TempDir()
	r := provisioningRecord{1, strings.Repeat("a", 64), strings.Repeat("b", 64)}
	if writeProvisionRecord(d, r, true) == nil {
		t.Fatal("complete without attempt")
	}
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if writeProvisionRecord(d, r, false) == nil {
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatal(n.Load())
	}
	other := r
	other.Digest = strings.Repeat("c", 64)
	if writeProvisionRecord(d, other, true) == nil {
		t.Fatal("mismatched completion")
	}
	if writeProvisionRecord(d, r, true) != nil {
		t.Fatal("completion")
	}
	if writeProvisionRecord(d, r, true) == nil || writeProvisionRecord(d, r, false) == nil {
		t.Fatal("replay")
	}
	st, e := os.Stat(filepath.Join(d, "provision-attempt.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("mode", e)
	}
}
func TestProvisionIncompleteAttemptCannotBeAdopted(t *testing.T) {
	d := t.TempDir()
	r := provisioningRecord{1, strings.Repeat("a", 64), strings.Repeat("b", 64)}
	if os.WriteFile(filepath.Join(d, "provision-attempt.json"), []byte("{"), 0600) != nil {
		t.Fatal("fixture")
	}
	if writeProvisionRecord(d, r, false) == nil || writeProvisionRecord(d, r, true) == nil {
		t.Fatal("unknown adopted")
	}
}
func TestProvisionNoFabricatedCapability(t *testing.T) {
	for _, r := range []*Running{nil, {}, new(Running)} {
		if r.BeginProvisioning(strings.Repeat("a", 64)) == nil || r.FinishProvisioning(strings.Repeat("a", 64)) == nil {
			t.Fatal("zero authority")
		}
	}
}
