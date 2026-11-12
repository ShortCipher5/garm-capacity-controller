package garmapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const managedPoolID = "00000000-0000-4000-8000-000000000001"
const managedRepoID = "00000000-0000-4000-8000-000000000002"
const otherID = "00000000-0000-4000-8000-000000000003"

type managementFixture struct {
	mu         sync.Mutex
	pool       map[string]any
	repo       map[string]any
	writes     int
	gets       int
	failWrite  int
	afterWrite func(*managementFixture)
	body       map[string]json.RawMessage
}

func managementServer(t *testing.T, writes bool) (*ManagementClient, *managementFixture) {
	t.Helper()
	f := &managementFixture{pool: map[string]any{"id": managedPoolID, "provider_name": "capacity", "repo_id": managedRepoID, "max_runners": uint64(4), "min_idle_runners": uint64(1), "enabled": true, "generation": uint64(7), "tags": []any{map[string]any{"name": "linux"}}, "priority": uint64(0), "image": "image", "extra_specs": map[string]any{"secret": "private-fixture-value"}, "instances": []any{map[string]any{"name": "running-fixture", "runner_status": "active", "job": map[string]any{"private": "payload"}}}}, repo: map[string]any{"id": managedRepoID, "owner": "example", "name": "app", "pool_balancing_type": "roundrobin", "credentials": map[string]any{"secret": "private-fixture-value"}}}
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer separate-write-fixture-token" {
			t.Error("missing dedicated credential")
			w.WriteHeader(401)
			return
		}
		var data map[string]any
		switch r.URL.Path {
		case "/api/v1/pools/" + managedPoolID:
			data = f.pool
		case "/api/v1/repositories/" + managedRepoID:
			data = f.repo
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodPut {
			f.writes++
			if r.Header.Get("Content-Type") != "application/json" {
				t.Error("missing content type")
			}
			if json.NewDecoder(r.Body).Decode(&f.body) != nil {
				t.Error("bad write body")
			}
			for key, raw := range f.body {
				var value any
				_ = json.Unmarshal(raw, &value)
				if key == "tags" {
					var tags []string
					_ = json.Unmarshal(raw, &tags)
					mapped := []any{}
					for _, tag := range tags {
						mapped = append(mapped, map[string]any{"name": tag})
					}
					data[key] = mapped
				} else if key == "pool_balancer_type" {
					data["pool_balancing_type"] = value
				} else {
					data[key] = value
				}
			}
			if f.afterWrite != nil {
				f.afterWrite(f)
			}
			if f.failWrite != 0 {
				w.Header().Set("Location", "https://untrusted.invalid/")
				w.WriteHeader(f.failWrite)
				_, _ = w.Write([]byte("private-fixture-value"))
				return
			}
		} else if r.Method == http.MethodGet {
			f.gets++
		} else {
			t.Error("unexpected mutation method")
			w.WriteHeader(405)
			return
		}
		_ = json.NewEncoder(w).Encode(data)
	}))
	client, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "separate-write-fixture-token", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, RepositoryIDs: []string{managedRepoID}, AllowWrites: writes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, f
}
func value[T any](v T) *T { return &v }
func TestManagementSanitizedScopedDiscovery(t *testing.T) {
	c, f := managementServer(t, false)
	snapshot, err := c.Snapshot(context.Background())
	if err != nil || len(snapshot.Pools) != 1 || len(snapshot.Repositories) != 1 || snapshot.WritesEnabled {
		t.Fatalf("snapshot %+v: %v", snapshot, err)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, private := range []string{"private-fixture-value", "running-fixture", "extra_specs", "credentials", "instances"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private field escaped: %s", private)
		}
	}
	if snapshot.Pools[0].Fingerprint == "" || snapshot.Repositories[0].Fingerprint == "" {
		t.Fatal("missing stale-edit precondition")
	}
	if _, err = c.Pool(context.Background(), otherID); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("unscoped pool accepted")
	}
	if _, err = c.Repository(context.Background(), otherID); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("unscoped repo accepted")
	}
	if _, err = c.UpdatePool(context.Background(), managedPoolID, snapshot.Pools[0].Fingerprint, PoolUpdate{Enabled: value(false)}); !errors.Is(err, ErrManagementDisabled) {
		t.Fatal("default write gate open")
	}
	if f.gets != 2 || f.writes != 0 {
		t.Fatal("unscoped or disabled request contacted server")
	}
}
func TestManagementAllowlistedPoolUpdateAndReadback(t *testing.T) {
	c, f := managementServer(t, true)
	before, err := c.Pool(context.Background(), managedPoolID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := c.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, PoolUpdate{Tags: value([]string{"self-hosted", "linux"}), Priority: value(uint64(12)), MaxRunners: value(uint64(9)), MinIdleRunners: value(uint64(2)), Enabled: value(false)})
	if err != nil || updated.Generation != before.Generation || updated.MaxRunners != 9 || updated.MinIdleRunners != 2 || updated.Enabled || updated.Priority != 12 || updated.Fingerprint == before.Fingerprint {
		t.Fatalf("update %+v %v", updated, err)
	}
	if f.writes != 1 || f.gets != 3 || len(f.body) != 5 {
		t.Fatalf("request accounting: %d writes %d reads %v", f.writes, f.gets, f.body)
	}
	for _, key := range []string{"image", "flavor", "extra_specs", "enable_shell", "os_type", "template_id", "github-runner-group", "provider_name"} {
		if _, ok := f.body[key]; ok {
			t.Fatal("dangerous setting sent")
		}
	}
	if len(f.pool["instances"].([]any)) != 1 {
		t.Fatal("running resource changed")
	}
}
func TestManagementRejectsStaleAndUnsafeEditsWithoutWrite(t *testing.T) {
	for name, update := range map[string]PoolUpdate{"empty": {}, "max-zero": {MaxRunners: value(uint64(0))}, "max-below-min": {MaxRunners: value(uint64(1)), MinIdleRunners: value(uint64(2))}, "idle-decrease": {MinIdleRunners: value(uint64(0))}, "empty-tags": {Tags: value([]string{})}, "duplicate-tags": {Tags: value([]string{"linux", "linux"})}, "newline-tags": {Tags: value([]string{"bad\nlabel"})}, "oversized-tags": {Tags: value([]string{strings.Repeat("a", 257)})}, "priority-limit": {Priority: value(uint64(1000001))}} {
		t.Run(name, func(t *testing.T) {
			c, f := managementServer(t, true)
			before, err := c.Pool(context.Background(), managedPoolID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, update); !errors.Is(err, ErrManagementUnsafe) {
				t.Fatalf("unsafe change: %v", err)
			}
			if f.writes != 0 {
				t.Fatal("unsafe request dispatched")
			}
		})
	}
	c, f := managementServer(t, true)
	before, _ := c.Pool(context.Background(), managedPoolID)
	f.mu.Lock()
	f.pool["priority"] = 1
	f.mu.Unlock()
	if _, err := c.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, PoolUpdate{Priority: value(uint64(5))}); !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if _, err := c.UpdatePool(context.Background(), managedPoolID, "", PoolUpdate{Priority: value(uint64(5))}); !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("missing precondition: %v", err)
	}
	if f.writes != 0 {
		t.Fatal("stale edit dispatched")
	}
}
func TestManagementRepositoryBalancerUsesActualWriteField(t *testing.T) {
	c, f := managementServer(t, true)
	before, err := c.Repository(context.Background(), managedRepoID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := c.UpdateRepositoryBalancer(context.Background(), managedRepoID, before.Fingerprint, "pack")
	if err != nil || after.Balancer != "pack" {
		t.Fatalf("repo %+v: %v", after, err)
	}
	if len(f.body) != 1 || string(f.body["pool_balancer_type"]) != `"pack"` {
		t.Fatalf("wrong upstream request %v", f.body)
	}
	if _, err = c.UpdateRepositoryBalancer(context.Background(), managedRepoID, after.Fingerprint, ""); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("unsupported default accepted")
	}
	if _, err = c.UpdateRepositoryBalancer(context.Background(), managedRepoID, before.Fingerprint, "roundrobin"); !errors.Is(err, ErrManagementConflict) {
		t.Fatal("stale repo edit dispatched")
	}
	if f.writes != 1 {
		t.Fatal("unexpected write")
	}
}
func TestManagementUncertainWriteNeverRetriedOrLeaksSource(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTemporaryRedirect, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, f := managementServer(t, true)
			before, _ := c.Pool(context.Background(), managedPoolID)
			f.failWrite = status
			_, err := c.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, PoolUpdate{Priority: value(uint64(6))})
			if !errors.Is(err, ErrManagementOutcomeUnknown) || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe outcome %v", err)
			}
			if f.writes != 1 {
				t.Fatal("write retried")
			}
			state, err := c.Pool(context.Background(), managedPoolID)
			if err != nil || state.Priority != 6 {
				t.Fatal("authoritative read did not resolve committed change")
			}
		})
	}
	for _, mutation := range []func(*managementFixture){func(f *managementFixture) { f.pool["generation"] = 8 }, func(f *managementFixture) { f.pool["image"] = "concurrent-image" }, func(f *managementFixture) { delete(f.pool, "priority") }, func(f *managementFixture) { f.pool["provider_name"] = "legacy" }, func(f *managementFixture) { f.pool["priority"] = 100 }} {
		c, f := managementServer(t, true)
		before, _ := c.Pool(context.Background(), managedPoolID)
		f.afterWrite = mutation
		if _, err := c.UpdatePool(context.Background(), managedPoolID, before.Fingerprint, PoolUpdate{Priority: value(uint64(6))}); !errors.Is(err, ErrManagementOutcomeUnknown) {
			t.Fatalf("mismatching readback: %v", err)
		}
		if f.writes != 1 {
			t.Fatal("write retried")
		}
	}
}
func TestManagementRejectsMalformedOrMisScopedReads(t *testing.T) {
	for name, mutation := range map[string]func(*managementFixture){"wrong-id": func(f *managementFixture) { f.pool["id"] = otherID }, "wrong-provider": func(f *managementFixture) { f.pool["provider_name"] = "legacy" }, "missing-generation": func(f *managementFixture) { delete(f.pool, "generation") }, "null-generation": func(f *managementFixture) { f.pool["generation"] = nil }, "negative-max": func(f *managementFixture) { f.pool["max_runners"] = -1 }, "null-enabled": func(f *managementFixture) { f.pool["enabled"] = nil }, "invalid-tags": func(f *managementFixture) { f.pool["tags"] = []string{"linux"} }, "duplicate-tags": func(f *managementFixture) {
		f.pool["tags"] = []any{map[string]any{"name": "linux"}, map[string]any{"name": "linux"}}
	}, "invalid-balancer": func(f *managementFixture) { f.repo["pool_balancing_type"] = "arbitrary" }} {
		t.Run(name, func(t *testing.T) {
			c, f := managementServer(t, false)
			f.mu.Lock()
			mutation(f)
			f.mu.Unlock()
			if snapshot, err := c.Snapshot(context.Background()); err == nil || len(snapshot.Pools) > 0 || len(snapshot.Repositories) > 0 {
				t.Fatalf("partial/malformed snapshot accepted: %+v %v", snapshot, err)
			}
		})
	}
}
func TestManagementInvalidConfiguration(t *testing.T) {
	server, trust := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid config made network request") }))
	base := ManagementConfig{Endpoint: server.URL, Token: "test", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}}
	for _, scope := range [][]string{nil, {"../pools"}, {"invalid"}, {managedPoolID, managedPoolID}, {strings.ToUpper(managedPoolID)}} {
		// Uppercasing this numeric UUID changes nothing; use a letter explicitly.
		if len(scope) == 1 && scope[0] == managedPoolID {
			scope = []string{"ABCDEF00-0000-4000-8000-000000000001"}
		}
		config := base
		config.PoolIDs = scope
		if _, err := NewManagement(config); err == nil {
			t.Fatalf("invalid scope accepted: %v", scope)
		}
	}
}

func TestManagementReadBoundsRejectOversizedSettings(t *testing.T) {
	for name, mutate := range map[string]func(*managementFixture){
		"too-many-tags": func(f *managementFixture) {
			tags := []any{}
			for i := 0; i < 101; i++ {
				tags = append(tags, map[string]any{"name": strings.Repeat("x", i+1)})
			}
			f.pool["tags"] = tags
		},
		"tag-too-long":      func(f *managementFixture) { f.pool["tags"] = []any{map[string]any{"name": strings.Repeat("x", 257)}} },
		"tag-control":       func(f *managementFixture) { f.pool["tags"] = []any{map[string]any{"name": "unsafe\tlabel"}} },
		"oversized-count":   func(f *managementFixture) { f.pool["max_runners"] = 1000001 },
		"unsafe-generation": func(f *managementFixture) { f.pool["generation"] = uint64(9007199254740992) },
		"owner-too-long":    func(f *managementFixture) { f.repo["owner"] = strings.Repeat("x", 257) },
	} {
		t.Run(name, func(t *testing.T) {
			client, f := managementServer(t, false)
			f.mu.Lock()
			mutate(f)
			f.mu.Unlock()
			if _, err := client.Snapshot(context.Background()); err == nil {
				t.Fatal("unbounded setting accepted")
			}
		})
	}
}
