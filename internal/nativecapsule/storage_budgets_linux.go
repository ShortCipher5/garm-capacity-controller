//go:build linux

package nativecapsule

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"os"
	"path/filepath"
	"sync"
)

type storageBudgetState struct {
	mu        sync.Mutex
	attempted bool
	sealed    *nativezfs.SealedBudgets
}

// SealStorageBudgets is a one-way host-root operation after actual fresh Incus
// resource bootstrap, before accepting any guest execution. It does not grant
// execution or release authority. Every uncertain transition permanently blocks
// further native transport via CheckScope; copied Running handles share state.
func (r *Running) SealStorageBudgets(ctx context.Context, b nativezfs.CategoryBudgets) error {
	if r == nil || r.budgets == nil || r.CheckScope(ctx) != nil {
		return ErrCapsule
	}
	s := r.budgets
	s.mu.Lock()
	if s.attempted {
		s.mu.Unlock()
		return ErrCapsule
	}
	if !completedProvisioning(r.staged.directory, r.BirthID()) {
		s.mu.Unlock()
		return ErrCapsule
	}
	s.attempted = true
	seal, e := r.storage.SealBudgets(ctx, b)
	if e == nil {
		e = seal.Recheck(ctx)
	}
	if e == nil {
		s.sealed = &seal
	}
	s.mu.Unlock()
	if e != nil || r.CheckScope(ctx) != nil {
		return ErrCapsule
	}
	return nil
}
func completedProvisioning(directory, birth string) bool {
	// These records live only in the original root-controlled fresh birth path;
	// they are never imported or used to adopt a restarted native daemon.
	raw, e := os.ReadFile(filepath.Join(directory, "provision-complete.json"))
	if e != nil || len(raw) > 1024 {
		return false
	}
	var rec provisioningRecord
	if json.Unmarshal(raw, &rec) != nil || rec.Version != 1 || rec.BirthID != birth || !hash(rec.Digest) {
		return false
	}
	canonical, _ := json.Marshal(rec)
	if string(raw) != string(canonical) {
		return false
	}
	prior, e := os.ReadFile(filepath.Join(directory, "provision-attempt.json"))
	return e == nil && string(prior) == string(raw)
}
func (r *Running) checkStorage(ctx context.Context) error {
	if r == nil || r.budgets == nil {
		return ErrCapsule
	}
	s := r.budgets
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attempted {
		return r.storage.Recheck(ctx)
	}
	if s.sealed == nil {
		return ErrCapsule
	}
	return s.sealed.Recheck(ctx)
}
func (r *Running) StorageBudgets(ctx context.Context) (nativezfs.CategoryBudgets, error) {
	if r == nil || r.CheckScope(ctx) != nil {
		return nativezfs.CategoryBudgets{}, ErrCapsule
	}
	s := r.budgets
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed == nil || s.sealed.Recheck(ctx) != nil {
		return nativezfs.CategoryBudgets{}, ErrCapsule
	}
	return s.sealed.Budgets(), nil
}
