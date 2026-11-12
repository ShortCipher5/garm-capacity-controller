package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

const browserPool = "00000000-0000-4000-8000-000000000001"
const browserRepository = "00000000-0000-4000-8000-000000000002"

// Disposable authenticated TLS Garm API fixture, not a product mock mode. Every
// browser action traverses the real admin handler, journal and scoped HTTP client.
// Priority 999 applies the fixture PUT but deliberately returns500 to exercise
// uncertainty; this fault control exists only in the browser test executable.
func browserGarm(dir string) (adminview.GarmManager, func(), error) {
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, func() {}, err
	}
	var mu sync.Mutex
	pool := map[string]any{"id": browserPool, "provider_name": "capacity", "repo_id": browserRepository, "max_runners": 4, "min_idle_runners": 1, "enabled": true, "generation": 1, "priority": 0, "tags": []any{map[string]string{"name": "linux"}}, "image": "fixture-image"}
	repo := map[string]any{"id": browserRepository, "owner": "example", "name": "app", "pool_balancing_type": "roundrobin"}
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer browser-disposable-garm-write-token" {
			w.WriteHeader(401)
			return
		}
		var target map[string]any
		switch r.URL.Path {
		case "/api/v1/pools/" + browserPool:
			target = pool
		case "/api/v1/repositories/" + browserRepository:
			target = repo
		default:
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodPut {
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			for key, raw := range body {
				var value any
				_ = json.Unmarshal(raw, &value)
				if key == "tags" {
					var tags []string
					_ = json.Unmarshal(raw, &tags)
					converted := []any{}
					for _, tag := range tags {
						converted = append(converted, map[string]string{"name": tag})
					}
					target[key] = converted
				} else if key == "pool_balancer_type" {
					target["pool_balancing_type"] = value
				} else {
					target[key] = value
				}
			}
			if priority, ok := pool["priority"].(float64); ok && priority == 999 {
				w.WriteHeader(500)
				return
			}
		} else if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		_ = json.NewEncoder(w).Encode(target)
	}))
	roots := x509.NewCertPool()
	roots.AddCert(remote.Certificate())
	client, err := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: remote.URL, Token: "browser-disposable-garm-write-token", Trust: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, Providers: []string{"capacity"}, PoolIDs: []string{browserPool}, RepositoryIDs: []string{browserRepository}, AllowWrites: true})
	if err != nil {
		remote.Close()
		return nil, func() {}, err
	}
	journal, err := garmmanagement.InitializeJournal(filepath.Join(dir, "garm-management.db"))
	if err != nil {
		client.Close()
		remote.Close()
		return nil, func() {}, err
	}
	manager, err := garmmanagement.New(client, journal)
	if err != nil {
		journal.Close()
		client.Close()
		remote.Close()
		return nil, func() {}, err
	}
	adapter, err := adminview.NewJournalledGarm(manager, true, []string{browserPool}, []string{browserRepository})
	if err != nil {
		journal.Close()
		client.Close()
		remote.Close()
		return nil, func() {}, err
	}
	return adapter, func() { client.Close(); journal.Close(); remote.Close() }, nil
}
