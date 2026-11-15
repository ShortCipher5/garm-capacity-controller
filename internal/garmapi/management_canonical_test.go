package garmapi

import (
	"context"
	"errors"
	"testing"
)

func TestManagedPoolRefusesAliasedSafetyFieldsBeforeWrite(t *testing.T) {
	for _, key := range []string{"enabled", "min_idle_runners", "max_runners", "priority", "tags", "generation", "repo_id", "provider_name", "id"} {
		t.Run(key, func(t *testing.T) {
			m, f := managementServer(t, true)
			defer m.Close()
			f.mu.Lock()
			alias := ""
			for _, r := range key {
				if r >= 'a' && r <= 'z' {
					r -= 'a' - 'A'
				}
				alias += string(r)
			}
			f.pool[alias] = f.pool[key]
			delete(f.pool, key)
			f.mu.Unlock()
			if p, err := m.Pool(context.Background(), managedPoolID); !errors.Is(err, ErrManagement) || p.ID != "" {
				t.Fatalf("accepted malformed pool: %#v %v", p, err)
			}
			min := uint64(0)
			if _, err := m.UpdatePool(context.Background(), managedPoolID, "untrusted-before", PoolUpdate{MinIdleRunners: &min}); !errors.Is(err, ErrManagement) {
				t.Fatalf("got %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 0 {
				t.Fatalf("unexpected writes: %d", f.writes)
			}
		})
	}
}

func TestManagedPoolRefusesConflictingTagNameAlias(t *testing.T) {
	m, f := managementServer(t, true)
	defer m.Close()
	f.mu.Lock()
	f.pool["tags"] = []any{map[string]any{"name": "linux", "Name": "other"}}
	f.mu.Unlock()
	if _, err := m.Pool(context.Background(), managedPoolID); !errors.Is(err, ErrManagement) {
		t.Fatalf("got %v", err)
	}
}

func TestManagedRepositoryRefusesBalancerAlias(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		m, f := managementServer(t, true)
		f.mu.Lock()
		f.repo["POOL_BALANCING_TYPE"] = "pack"
		if !duplicate {
			delete(f.repo, "pool_balancing_type")
		}
		f.mu.Unlock()
		if _, err := m.Repository(context.Background(), managedRepoID); !errors.Is(err, ErrManagement) {
			t.Fatalf("got %v", err)
		}
		m.Close()
	}
}

func TestCanonicalManagementFieldsPreservesForwardCompatibility(t *testing.T) {
	m, f := managementServer(t, false)
	defer m.Close()
	f.mu.Lock()
	f.pool["future_setting"] = true
	delete(f.pool, "priority")
	f.mu.Unlock()
	p, err := m.Pool(context.Background(), managedPoolID)
	if err != nil || p.Priority != 0 {
		t.Fatalf("valid omitted/unknown fields refused: %v", err)
	}
}
