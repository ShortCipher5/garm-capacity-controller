//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealRequiresActualRunningAndProvisioning(t *testing.T) {
	if (&Running{}).SealStorageBudgets(context.Background(), nativezfs.CategoryBudgets{}) == nil {
		t.Fatal("zero running")
	}
	d := t.TempDir()
	birth := strings.Repeat("a", 64)
	rec := provisioningRecord{1, birth, strings.Repeat("b", 64)}
	if completedProvisioning(d, birth) {
		t.Fatal("missing")
	}
	if writeProvisionRecord(d, rec, false) != nil {
		t.Fatal("attempt")
	}
	if completedProvisioning(d, birth) {
		t.Fatal("attempt isn't success")
	}
	if writeProvisionRecord(d, rec, true) != nil || !completedProvisioning(d, birth) {
		t.Fatal("exact completed")
	}
	if completedProvisioning(d, strings.Repeat("c", 64)) {
		t.Fatal("wrong birth")
	}
	os.WriteFile(filepath.Join(d, "provision-attempt.json"), []byte("{}"), 0600)
	if completedProvisioning(d, birth) {
		t.Fatal("mismatch")
	}
}
func TestFailedSealStateSharedAcrossCopies(t *testing.T) {
	r := Running{budgets: &storageBudgetState{attempted: true}}
	copy := r
	for _, v := range []*Running{&r, &copy} {
		if v.checkStorage(context.Background()) == nil {
			t.Fatal("unknown reopened")
		}
	}
}
