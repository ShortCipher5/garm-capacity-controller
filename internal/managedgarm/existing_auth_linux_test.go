package managedgarm

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestExistingAuthenticationMatchesBootstrappedExchangeWithoutWrites(t *testing.T) {
	files := bootstrapFixture(t)
	count := 0
	id := strings.Repeat("b", 64)
	expected, _, err := bootstrapProtocol(context.Background(), files, id, bootstrapResponder(&count, false))
	if err != nil {
		t.Fatal(err)
	}
	before := authFiles(t, files)
	type call struct{ Method, Path, Token, Body string }
	var calls []call
	exchange := func(ctx context.Context, m, p, token string, body any) ([]byte, error) {
		if p == "/api/v1/first-run" {
			t.Fatal("existing authentication attempted first-run")
		}
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		calls = append(calls, call{m, p, token, string(raw)})
		return bootstrapResponder(&count, false)(ctx, m, p, token, body)
	}
	legacy, legacyToken, err := bootstrapProtocol(context.Background(), files, id, exchange)
	if err != nil {
		t.Fatal(err)
	}
	legacyCalls := append([]call(nil), calls...)
	calls = nil
	got, token, err := authenticateExistingProtocol(context.Background(), files, id, expected, exchange)
	if err != nil || got != legacy || token != legacyToken || !reflect.DeepEqual(calls, legacyCalls) || len(calls) != 2 || calls[0].Method != "POST" || calls[0].Path != "/api/v1/auth/login" || calls[1].Method != "GET" || calls[1].Path != "/api/v1/controller" {
		t.Fatal("existing authentication exchange changed", err)
	}
	if count != 1 || !reflect.DeepEqual(before, authFiles(t, files)) {
		t.Fatal("existing authentication changed bootstrap files")
	}
}

func authFiles(t *testing.T, files *bootstrapFiles) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(files.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		raw, err := files.root.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = string(raw)
	}
	return out
}

func TestExistingAuthenticationRefusesLostOrChangedLineageWithoutRepair(t *testing.T) {
	for _, kind := range []string{"missing-attempt", "changed-credential", "missing-record-during-login", "changed-controller", "login-failure", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			files := bootstrapFixture(t)
			count := 0
			id := strings.Repeat("b", 64)
			expected, _, err := bootstrapProtocol(context.Background(), files, id, bootstrapResponder(&count, false))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing-attempt" {
				if err := files.root.Remove("bootstrap-attempt.json"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "changed-credential" {
				var c credential
				if files.read("bootstrap-user.json", &c) != nil {
					t.Fatal("credential fixture")
				}
				c.Password = strings.Repeat("c", 64)
				raw, _ := json.Marshal(c)
				if err := os.WriteFile(files.root.Name()+"/bootstrap-user.json", raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := authFiles(t, files)
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			exchange := func(ctx context.Context, m, p, token string, body any) ([]byte, error) {
				calls++
				if p == "/api/v1/first-run" {
					t.Fatal("first-run reached")
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if p == "/api/v1/auth/login" {
					if kind == "login-failure" {
						return nil, ErrBoundary
					}
					if kind == "missing-record-during-login" {
						if err := files.root.Remove("bootstrap-record.json"); err != nil {
							t.Fatal(err)
						}
						delete(before, "bootstrap-record.json")
					}
				}
				if p == "/api/v1/controller" && kind == "changed-controller" {
					return []byte(`{"controller_id":"33333333-3333-4333-8333-333333333333"}`), nil
				}
				return bootstrapResponder(&count, false)(ctx, m, p, token, body)
			}
			got, token, err := authenticateExistingProtocol(ctx, files, id, expected, exchange)
			if err == nil || got != (bootstrapRecord{}) || token != "" {
				t.Fatal("failed authentication exposed session material")
			}
			if (kind == "missing-attempt" || kind == "changed-credential") && calls != 0 {
				t.Fatal("invalid lineage reached authentication")
			}
			if count != 1 || !reflect.DeepEqual(before, authFiles(t, files)) {
				t.Fatal("failed authentication repaired or changed state")
			}
		})
	}
}
