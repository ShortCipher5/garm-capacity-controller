package garmmanagement

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestStockZeroMaximumUsesDurableJournalAndRetainsUnknown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "lost response"}[fail], func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			pool := map[string]any{"id": poolID, "provider_name": "capacity", "repo_id": repoID, "generation": 7, "max_runners": 4, "min_idle_runners": 0, "enabled": true, "tags": []any{map[string]any{"name": "linux"}}}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path != "/api/v1/pools/"+poolID || r.Header.Get("Authorization") != "Bearer synthetic-zero-fixture" {
					w.WriteHeader(401)
					return
				}
				if r.Method == "PUT" {
					writes++
					var body map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || string(body["max_runners"]) != "0" {
						t.Error("wrong zero request")
					}
					pool["max_runners"] = 0
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
			client, e := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "synthetic-zero-fixture", Trust: &tls.Config{RootCAs: roots}, Providers: []string{"capacity"}, PoolIDs: []string{poolID}, RepositoryIDs: []string{repoID}, AllowWrites: true})
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
			got, e := manager.UpdatePool(context.Background(), requestID, poolID, before.Fingerprint, garmapi.PoolUpdate{MaxRunners: ptr(uint64(0))}, "pause future starts only")
			if !fail {
				if e != nil || got.MaxRunners != 0 || !got.Enabled {
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
			journal.Close()
			reopened, e := OpenJournal(path, lineage)
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
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
				t.Fatal("matching zero read cleared unknown")
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
