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

func disabledSpec() DisabledPoolSpec {
	return DisabledPoolSpec{ProviderName: "capacity", MaxRunners: 2, Image: "linux", Flavor: "small", Arch: "amd64", Tags: []string{"linux"}}
}

type disabledPoolFixture struct {
	beforeGet   func()
	mu          sync.Mutex
	posts, gets int
	status      int
	pool        map[string]any
	body        map[string]json.RawMessage
	afterPost   func(*disabledPoolFixture)
}

func disabledPoolServer(t *testing.T, enabled bool, stock ...bool) (*ManagementClient, *disabledPoolFixture) {
	t.Helper()
	f := &disabledPoolFixture{pool: map[string]any{"id": otherID, "repo_id": managedRepoID, "provider_name": "capacity", "max_runners": 2, "generation": 1, "image": "linux", "flavor": "small", "os_type": "linux", "os_arch": "amd64", "tags": []any{map[string]any{"name": "linux"}}, "enable_shell": false, "extra_specs": map[string]any{}}}
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-stage-token" {
			t.Error("missing separate bearer")
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/repositories/"+managedRepoID:
			json.NewEncoder(w).Encode(map[string]any{"id": managedRepoID, "owner": "example", "name": "app", "pool_balancing_type": "roundrobin"})
		case r.Method == "POST" && r.URL.Path == "/api/v1/repositories/"+managedRepoID+"/pools":
			f.posts++
			if json.NewDecoder(r.Body).Decode(&f.body) != nil {
				t.Error("invalid POST")
			}
			if f.afterPost != nil {
				f.afterPost(f)
			}
			if f.status != 0 {
				w.Header().Set("Location", "https://not-allowed.invalid")
				w.WriteHeader(f.status)
				return
			}
			json.NewEncoder(w).Encode(f.pool)
		case r.Method == "GET" && r.URL.Path == "/api/v1/repositories/"+managedRepoID+"/pools/"+otherID:
			f.gets++
			if f.beforeGet != nil {
				f.beforeGet()
			}
			json.NewEncoder(w).Encode(f.pool)
		default:
			t.Error("unexpected route", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	client, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "fixture-stage-token", Trust: trust, Providers: []string{"capacity"}, RepositoryIDs: []string{managedRepoID}, AllowWrites: true, AllowDisabledPoolCreation: enabled, AllowStockDisabledPoolCreation: len(stock) > 0 && stock[0]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, f
}
func disabledExpected(t *testing.T, c *ManagementClient) string {
	t.Helper()
	repo, err := c.Repository(context.Background(), managedRepoID)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Fingerprint
}
func TestDisabledPoolProtocolIsExplicitNativeZeroAndCannotActivateOrExpandScope(t *testing.T) {
	c, f := disabledPoolServer(t, true)
	receipt, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, disabledExpected(t, c), disabledSpec())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PoolID() != otherID || receipt.RepositoryID() != managedRepoID || receipt.SpecFingerprint() != DisabledPoolSpecFingerprint(disabledSpec()) || f.gets != 0 {
		t.Fatal("UUID/readback boundary changed")
	}
	pool, err := c.VerifyDisabledRepoPool(context.Background(), receipt, disabledSpec())
	if err != nil || pool.Enabled || pool.MinIdleRunners != 0 || f.posts != 1 || f.gets != 1 {
		t.Fatal("invalid disabled readback", err)
	}
	for key, want := range map[string]string{"enabled": "false", "min_idle_runners": "0", "template_id": "0", "enable_shell": "false", "os_type": `"linux"`, "extra_specs": "{}"} {
		if string(f.body[key]) != want {
			t.Fatalf("unsafe creation field %s=%s", key, f.body[key])
		}
	}
	if len(f.body) != 12 {
		t.Fatalf("unexpected POST fields: %v", f.body)
	}
	if _, err = c.UpdatePool(context.Background(), otherID, pool.Fingerprint, PoolUpdate{Enabled: value(true)}); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("new UUID silently became writable")
	}
	if _, err = c.VerifyDisabledRepoPool(context.Background(), DisabledPoolReceipt{}, disabledSpec()); !errors.Is(err, ErrManagementUnsafe) {
		t.Fatal("forged receipt accepted")
	}
}
func TestDisabledPoolFeatureAndScopeDefaultClosed(t *testing.T) {
	c, f := disabledPoolServer(t, false)
	if _, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, disabledExpected(t, c), disabledSpec()); !errors.Is(err, ErrManagementDisabled) || f.posts != 0 {
		t.Fatal("default enabled create")
	}
	c, f = disabledPoolServer(t, true)
	expected := disabledExpected(t, c)
	for _, repo := range []string{otherID, "https://not-allowed.invalid", "00--0000-0000-4000-8000-000000000003"} {
		if _, err := c.PostDisabledRepoPool(context.Background(), repo, expected, disabledSpec()); !errors.Is(err, ErrManagementUnsafe) {
			t.Fatal("unscoped repo accepted")
		}
	}
	for _, edit := range []func(*DisabledPoolSpec){func(s *DisabledPoolSpec) { s.MaxRunners = 0 }, func(s *DisabledPoolSpec) { s.ProviderName = "other" }, func(s *DisabledPoolSpec) { s.Tags = []string{"linux", "linux"} }, func(s *DisabledPoolSpec) { s.Arch = "other" }} {
		s := disabledSpec()
		edit(&s)
		if _, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, expected, s); !errors.Is(err, ErrManagementUnsafe) {
			t.Fatal("unsafe spec accepted")
		}
	}
	if _, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, strings.Repeat("f", 64), disabledSpec()); !errors.Is(err, ErrManagementConflict) {
		t.Fatal("stale repository accepted")
	}
	if f.posts != 0 {
		t.Fatal("invalid request dispatched")
	}
}
func TestDisabledPoolPostErrorsAndMalformedReadbacksNeverRetry(t *testing.T) {
	for _, status := range []int{400, 500, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, f := disabledPoolServer(t, true)
			f.status = status
			if _, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, disabledExpected(t, c), disabledSpec()); !errors.Is(err, ErrManagementOutcomeUnknown) || f.posts != 1 || f.gets != 0 {
				t.Fatal("ambiguous POST was retried or definite failure")
			}
		})
	}
	for name, edit := range map[string]func(map[string]any){"enabled": func(p map[string]any) { p["enabled"] = true }, "casealias": func(p map[string]any) { p["Enabled"] = true }, "legacytemplate": func(p map[string]any) { p["template_id"] = 7 }, "nulltemplate": func(p map[string]any) { p["template_id"] = nil }, "scope": func(p map[string]any) { p["repo_id"] = otherID }, "child": func(p map[string]any) { p["instances"] = []any{map[string]any{"name": "private"}} }} {
		t.Run(name, func(t *testing.T) {
			c, f := disabledPoolServer(t, true)
			f.afterPost = func(f *disabledPoolFixture) { edit(f.pool) }
			receipt, err := c.PostDisabledRepoPool(context.Background(), managedRepoID, disabledExpected(t, c), disabledSpec())
			if !errors.Is(err, ErrManagementOutcomeUnknown) || receipt.PoolID() != otherID || f.posts != 1 || f.gets != 0 {
				t.Fatal("unsafe response lost UUID or authorized readback")
			}
			if _, err = c.VerifyDisabledRepoPool(context.Background(), receipt, disabledSpec()); !errors.Is(err, ErrManagementUnsafe) {
				t.Fatal("invalid POST receipt authorized GET")
			}
		})
	}
}

func TestStockDisabledCreationPinsPositiveTemplateAndSeparateOptIn(t *testing.T) {
	ctx := t.Context()
	spec := disabledSpec()
	spec.TemplateID = 12
	native, fixture := disabledPoolServer(t, true)
	if _, err := native.PostDisabledRepoPool(ctx, managedRepoID, disabledExpected(t, native), spec); !errors.Is(err, ErrManagementDisabled) || fixture.posts != 0 {
		t.Fatalf("native permission dispatched stock create: %v", err)
	}

	stock, fixture := disabledPoolServer(t, false, true)
	expected := disabledExpected(t, stock)
	if _, err := stock.PostDisabledRepoPool(ctx, managedRepoID, expected, disabledSpec()); !errors.Is(err, ErrManagementDisabled) || fixture.posts != 0 {
		t.Fatalf("stock permission dispatched native create: %v", err)
	}
	fixture.pool["template_id"] = spec.TemplateID
	receipt, err := stock.PostDisabledRepoPool(ctx, managedRepoID, expected, spec)
	if err != nil || receipt.SpecFingerprint() == DisabledPoolSpecFingerprint(disabledSpec()) {
		t.Fatalf("stock receipt did not pin protocol and template: %v", err)
	}
	if string(fixture.body["template_id"]) != "12" || fixture.posts != 1 || fixture.gets != 0 || len(fixture.body) != 12 {
		t.Fatalf("unexpected stock create calls or payload: %v", fixture.body)
	}
	pool, err := stock.VerifyDisabledRepoPool(ctx, receipt, spec)
	if err != nil || pool.Enabled || pool.MinIdleRunners != 0 {
		t.Fatalf("verified stock pool was unavailable or unsafe: %+v %v", pool, err)
	}
	fixture.mu.Lock()
	delete(fixture.pool, "template_id")
	fixture.mu.Unlock()
	if _, err := stock.VerifyDisabledRepoPool(ctx, receipt, spec); !errors.Is(err, ErrManagementOutcomeUnknown) {
		t.Fatalf("readback accepted an omitted stock template: %v", err)
	}
}

func TestStockDisabledCreationDoesNotAuthorizeReadbackAfterWrongTemplate(t *testing.T) {
	stock, fixture := disabledPoolServer(t, false, true)
	spec := disabledSpec()
	spec.TemplateID = 12
	fixture.pool["template_id"] = 99
	receipt, err := stock.PostDisabledRepoPool(t.Context(), managedRepoID, disabledExpected(t, stock), spec)
	if !errors.Is(err, ErrManagementOutcomeUnknown) || receipt.PoolID() != otherID || fixture.posts != 1 {
		t.Fatalf("mismatched template lost the original acknowledgement: %+v %v", receipt, err)
	}
	if _, err := stock.VerifyDisabledRepoPool(t.Context(), receipt, spec); !errors.Is(err, ErrManagementUnsafe) || fixture.gets != 0 {
		t.Fatalf("invalid acknowledgement authorized readback: %v", err)
	}
}
