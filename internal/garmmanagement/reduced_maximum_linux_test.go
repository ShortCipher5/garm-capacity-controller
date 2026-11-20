package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestStockLowerPositiveMaximumUsesDurableJournalAndRetainsUnknown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "lost response"}[fail], func(t *testing.T) {
			const otherPoolID = "00000000-0000-4000-8000-000000000005"
			var mu sync.Mutex
			writes := 0
			pool := map[string]any{"id": poolID, "provider_name": "capacity", "repo_id": repoID, "generation": 7, "max_runners": 4, "min_idle_runners": 1, "enabled": true, "tags": []any{map[string]any{"name": "linux"}}}
			otherPool := map[string]any{"id": otherPoolID, "provider_name": "capacity", "repo_id": repoID, "generation": 1, "max_runners": 5, "min_idle_runners": 1, "enabled": true}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer synthetic-reduction-fixture" {
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/api/v1/pools/"+otherPoolID {
					if r.Method != "GET" {
						writes++
						t.Error("unresolved reduction allowed another pool mutation")
						w.WriteHeader(409)
						return
					}
					json.NewEncoder(w).Encode(otherPool)
					return
				}
				if r.URL.Path != "/api/v1/pools/"+poolID {
					t.Errorf("unexpected fixture path: %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if r.Method == "PUT" {
					writes++
					var body map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || string(body["max_runners"]) != "2" {
						t.Error("wrong reduction request")
					}
					pool["max_runners"] = 2
					if fail {
						w.WriteHeader(503)
						return
					}
				} else if r.Method != "GET" {
					t.Error("unexpected method")
				}
				json.NewEncoder(w).Encode(pool)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			config := garmapi.ManagementConfig{Endpoint: server.URL, Token: "synthetic-reduction-fixture", Trust: &tls.Config{RootCAs: roots}, Providers: []string{"capacity"}, PoolIDs: []string{poolID, otherPoolID}, RepositoryIDs: []string{repoID}, AllowWrites: true}
			client, e := garmapi.NewManagement(config)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			path := filepath.Join(privateJournalDir(t), "journal.db")
			journal, e := InitializeJournal(path)
			if e != nil {
				t.Fatal(e)
			}
			lineage := journal.Lineage()
			defer journal.Close()
			manager, e := New(client, journal)
			if e != nil {
				t.Fatal(e)
			}
			before, e := client.Pool(context.Background(), poolID)
			if e != nil {
				t.Fatal(e)
			}
			got, e := manager.UpdatePool(context.Background(), requestID, poolID, before.Fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(2))}, "pause future starts only")
			if !fail {
				if e != nil || got.MaxRunners != 2 || got.MinIdleRunners != 1 || !got.Enabled {
					t.Fatalf("%+v %v", got, e)
				}
				history, _ := journal.History(context.Background(), 0, 10)
				if len(history) != 1 || history[0].State != "confirmed" {
					t.Fatal("missing durable confirmation")
				}
				return
			}
			if !errors.Is(e, ErrOutcomeUnknown) {
				t.Fatalf("lost response not unknown: %v", e)
			}
			if e = journal.Close(); e != nil {
				t.Fatal(e)
			}
			reopened, e := OpenJournal(path, lineage)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			// A restarted caller with a narrower configured pool scope must
			// still honor the original unknown intent, even if it cannot read A.
			// Pool B is independently readable and its proposed max is valid.
			ctx := t.Context()
			config.PoolIDs = []string{otherPoolID}
			narrow, e := garmapi.NewManagement(config)
			if e != nil {
				t.Fatal(e)
			}
			defer narrow.Close()
			manager, e = New(narrow, reopened)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = manager.ReconcilePending(ctx); !errors.Is(e, garmapi.ErrManagementUnsafe) {
				t.Fatalf("narrower scope unexpectedly read the previously targeted pool: %v", e)
			}
			other, e := narrow.Pool(ctx, otherPoolID)
			if e != nil || other.MaxRunners != 5 || other.MinIdleRunners != 1 || !other.Enabled {
				t.Fatalf("independent second-pool read not usable: %+v %v", other, e)
			}
			if _, e = manager.UpdatePool(ctx, nextID, otherPoolID, other.Fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(4))}, "must hold all pools"); !errors.Is(e, ErrBlocked) {
				t.Fatalf("valid second-pool reduction bypassed the unresolved write: %v", e)
			}
			history, e := reopened.History(ctx, 0, 10)
			if e != nil || len(history) != 1 || history[0].Target != poolID || history[0].RequestID != requestID || history[0].State != "unknown" {
				t.Fatalf("only the exact original unresolved intent may remain: %+v %v", history, e)
			}
			manager, e = New(client, reopened)
			if e != nil {
				t.Fatal(e)
			}
			observations, e := manager.ReconcilePending(context.Background())
			if e != nil || len(observations) != 1 || !observations[0].MatchesDesired {
				t.Fatalf("%+v %v", observations, e)
			}
			state, _ := manager.Status(context.Background())
			if !state.WriteBlocked {
				t.Fatal("matching reduced-maximum read cleared unknown")
			}
			current, _ := client.Pool(context.Background(), poolID)
			_, e = manager.UpdatePool(context.Background(), nextID, poolID, current.Fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(1))}, "must remain blocked")
			if !errors.Is(e, ErrBlocked) {
				t.Fatalf("new write not blocked: %v", e)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != 1 {
				t.Fatal("duplicated remote write")
			}
		})
	}
}
