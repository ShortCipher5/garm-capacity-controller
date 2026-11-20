package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

func TestCIHealthStartupRequiresSeparateOptInAndExactScope(t *testing.T) {
	var disabled configuration
	if service, err := buildCIHealth(disabled, nil); err != nil || service != nil {
		t.Fatalf("default CI startup was not off: %v", err)
	}
	valid := configuration{
		EnableCIHealth:           true,
		Admin:                    &adminConfiguration{},
		DemandObserverConfigFile: "/owner-private/current-demand-observer.json",
		CIHealth:                 &ciHealthConfiguration{Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: "repo-a", GitHubID: 12}}},
	}
	if validateCIHealthConfiguration(valid) != nil {
		t.Fatal("valid static shape rejected")
	}
	if service, err := buildCIHealth(valid, nil); !errors.Is(err, errCIHealthConfiguration) || service != nil {
		t.Fatalf("startup inferred an existing read credential: %v", err)
	}
	for name, mutate := range map[string]func(*configuration){
		"configuration only": func(c *configuration) { c.EnableCIHealth = false },
		"flag only":          func(c *configuration) { c.CIHealth = nil },
		"no admin":           func(c *configuration) { c.Admin = nil },
		"no source":          func(c *configuration) { c.DemandObserverConfigFile = "" },
		"duplicate locator": func(c *configuration) {
			c.CIHealth.Repositories = append(c.CIHealth.Repositories, demandobserver.CIHealthRepositoryPin{GarmID: "repo-a", GitHubID: 13})
		},
		"duplicate numeric ID": func(c *configuration) {
			c.CIHealth.Repositories = append(c.CIHealth.Repositories, demandobserver.CIHealthRepositoryPin{GarmID: "repo-b", GitHubID: 12})
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			c.CIHealth = &ciHealthConfiguration{Repositories: append([]demandobserver.CIHealthRepositoryPin(nil), valid.CIHealth.Repositories...)}
			mutate(&c)
			if service, err := buildCIHealth(c, nil); !errors.Is(err, errCIHealthConfiguration) || service != nil {
				t.Fatalf("invalid startup was accepted: %v", err)
			}
		})
	}
}

func TestCIHealthStartupDoesNotRetainAttachmentOnDisabledAdminReuse(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := strings.Repeat("a", 64)
	path := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &adminConfiguration{Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: path}
	source, err := adminview.NewCIHealthSnapshot([]adminview.CIHealthRepository{{Source: "github", RepositoryID: 12}})
	if err != nil {
		t.Fatal(err)
	}
	enabled, clean, err := newAdminServerWithCIHealth(cfg, store, false, source)
	if err != nil {
		t.Fatal(err)
	}
	defer clean()
	disabled, clean, err := newAdminServer(cfg, store, false)
	if err != nil {
		t.Fatal(err)
	}
	defer clean()
	get := func(s *http.Server, route string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/"+route, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler.ServeHTTP(rec, r)
		return rec
	}
	if get(enabled, "ci-health").Code != 200 || get(disabled, "ci-health").Code != 404 ||
		strings.Contains(get(disabled, "overview").Body.String(), "ci_health_available") {
		t.Fatal("CI route or availability leaked across admin startup instances")
	}
}
