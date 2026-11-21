package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
)

const testHistorySnapshot = "ci-health-history-v1.json"

type historyListenerAddress struct {
	net.Listener
	address *net.TCPAddr
}

func (l historyListenerAddress) Addr() net.Addr { return l.address }

func testReviewShape() configuration {
	n := int64(0)
	return configuration{
		EnableCIHealth: true, EnableCIHealthHistory: true, DemandObserverConfigFile: "fixture",
		Admin: &adminConfiguration{Listen: "127.0.0.1:0"},
		CIHealth: &ciHealthConfiguration{
			Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: "fixture", GitHubID: 621}},
			History: &ciHealthHistoryConfiguration{
				Directory: "/var/lib/fixture-ci/history", Device: 1, Inode: 2,
				SnapshotSHA256: strings.Repeat("a", 64), SourceID: strings.Repeat("b", 64),
				Workflows: []ciHealthHistoryScope{{RepositoryID: 621, WorkflowID: 622, Revision: &n, Checkpoint: &n}},
			},
		},
	}
}

func TestCIHistoryStartupOptInShape(t *testing.T) {
	for name, mutate := range map[string]func(*configuration){
		"missing flags": func(c *configuration) { c.EnableCIHealthHistory = false },
		"missing block": func(c *configuration) { c.CIHealth.History = nil },
		"no timing":     func(c *configuration) { c.EnableCIHealth = false },
		"no CI config":  func(c *configuration) { c.CIHealth = nil },
		"remote admin":  func(c *configuration) { c.Admin.Listen = "0.0.0.0:8080" },
		"relative root": func(c *configuration) { c.CIHealth.History.Directory = "./history" },
		"unclean root":  func(c *configuration) { c.CIHealth.History.Directory = "/var/../history" },
		"missing pin":   func(c *configuration) { c.CIHealth.History.Inode = 0 },
		"bad digest":    func(c *configuration) { c.CIHealth.History.SnapshotSHA256 = strings.Repeat("A", 64) },
		"bad source":    func(c *configuration) { c.CIHealth.History.SourceID = strings.Repeat("0", 64) },
		"foreign repo":  func(c *configuration) { c.CIHealth.History.Workflows[0].RepositoryID++ },
		"no workflow":   func(c *configuration) { c.CIHealth.History.Workflows[0].WorkflowID = 0 },
		"duplicate":     func(c *configuration) { h := c.CIHealth.History; h.Workflows = append(h.Workflows, h.Workflows[0]) },
		"no revision":   func(c *configuration) { c.CIHealth.History.Workflows[0].Revision = nil },
		"no checkpoint": func(c *configuration) { c.CIHealth.History.Workflows[0].Checkpoint = nil },
		"bad zero cursor": func(c *configuration) {
			n := int64(1)
			c.CIHealth.History.Workflows[0].Checkpoint = &n
		},
		"nonadvancing revision": func(c *configuration) {
			n := int64(math.MaxInt64)
			c.CIHealth.History.Workflows[0].Revision = &n
		},
	} {
		t.Run(name, func(t *testing.T) {
			conf := testReviewShape()
			mutate(&conf)
			if err := validateCIHealthConfiguration(conf); err != errCIHealthConfiguration {
				t.Fatalf("invalid startup shape accepted: %v", err)
			}
		})
	}
	conf := testReviewShape()
	if err := validateCIHealthConfiguration(conf); err != nil {
		t.Fatal("static shape tried to open operator storage", err)
	}
	conf.EnableCIHealthHistory, conf.CIHealth.History = false, nil
	if err := attachConfiguredCIHistory(t.Context(), conf, nil); err != nil {
		t.Fatal("ordinary timing touched history", err)
	}
	if err := attachConfiguredCIHistory(t.Context(), configuration{}, nil); err != nil {
		t.Fatal("default startup touched history", err)
	}
}

func testReviewPin(t *testing.T, dir string) ciHealthHistoryConfiguration {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return ciHealthHistoryConfiguration{Directory: dir, Device: uint64(stat.Dev), Inode: stat.Ino}
}

func testReviewDirectory(t *testing.T) string {
	t.Helper()
	// Hosted runners usually have a world-writable /tmp. The checkout is an
	// alternative only if its actual ancestors satisfy the same launch rules.
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{t.TempDir(), workspace} {
		dir, err := os.MkdirTemp(parent, ".ci-history-reviewed-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		root, err := openCIHistoryRoot(t.Context(), testReviewPin(t, dir))
		if err == nil {
			root.Close()
			return dir
		}
	}
	t.Skip("test needs a permitted private directory with trusted non-writable ancestors")
	return ""
}

func TestCIHistoryStartupRefusesPathAliasesAndUntrustedAncestors(t *testing.T) {
	base := testReviewDirectory(t)
	dir := filepath.Join(base, "history")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pin := testReviewPin(t, dir)
	for _, test := range []struct{ name, target, alias string }{
		{"final", dir, filepath.Join(base, "alias")},
		{"ancestor", base, filepath.Join(base, "alias", "history")},
	} {
		t.Run(test.name, func(t *testing.T) {
			link := filepath.Join(base, "alias")
			if err := os.Symlink(test.target, link); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(link)
			p := pin
			p.Directory = test.alias
			if r, err := openCIHistoryRoot(t.Context(), p); r != nil || err != errCIHealthConfiguration {
				t.Fatal("followed a symlink into the same pinned inode", err)
			}
		})
	}
	for _, mode := range []os.FileMode{0770, 0777 | os.ModeSticky} {
		if err := os.Chmod(base, mode); err != nil {
			t.Fatal(err)
		}
		if r, err := openCIHistoryRoot(t.Context(), pin); r != nil || err != errCIHealthConfiguration {
			t.Fatal("accepted writable ancestor", err)
		}
	}
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if r, err := openCIHistoryRoot(t.Context(), pin); r != nil || err != errCIHealthConfiguration {
		t.Fatal("accepted loose directory mode", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r, err := openCIHistoryRoot(ctx, pin); r != nil || err != errCIHealthConfiguration {
		t.Fatal("canceled readiness retained custody", err)
	}
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if r, err := openCIHistoryRoot(t.Context(), pin); r != nil || err != errCIHealthConfiguration {
		t.Fatal("accepted path replacement despite different inode", err)
	}
}

func TestCIHistoryStartupReviewedStoreAndSameOwnerRoute(t *testing.T) {
	dir := testReviewDirectory(t)
	f := newCIHistoryFixture(t)
	ctx := t.Context()
	conf := f.startup
	conf.Admin.Listen = "127.0.0.1:0"
	conf.EnableCIHealthHistory = true
	conf.CIHealth.History = new(ciHealthHistoryConfiguration)
	*conf.CIHealth.History = testReviewPin(t, dir)
	review := conf.CIHealth.History
	review.SourceID = f.runtime.source.SourceID()
	review.SnapshotSHA256 = strings.Repeat("a", 64)
	scope := f.expect[0].Scope
	zero, one := int64(0), int64(1)
	review.Workflows = []ciHealthHistoryScope{{RepositoryID: scope.RepositoryID, WorkflowID: scope.WorkflowID, Revision: &zero, Checkpoint: &zero}}
	openRuntime := func() *ciHealthRuntime {
		t.Helper()
		r, err := buildCIHealth(conf, &f.demand)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.close)
		return r
	}
	runtime := openRuntime()
	if err := attachConfiguredCIHistory(ctx, conf, runtime); err != errCIHealthConfiguration || runtime.activeHistory() != nil {
		t.Fatal("missing snapshot initialized/adopted", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("startup changed empty directory", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Independent in-process store constructor, not a gateway initialization path.
	seed, err := cihealth.OpenHistory(ctx, cihealth.HistoryInput{Enabled: true, Root: root, Initialize: true})
	if err != nil {
		t.Fatal(err)
	}
	digest := func() string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, testHistorySnapshot))
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	review.SnapshotSHA256 = digest()
	if err := attachConfiguredCIHistory(ctx, conf, runtime); err != errCIHealthConfiguration {
		t.Fatal("competing held writer was accepted", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	initial := review.SnapshotSHA256
	demandPath := filepath.Join(t.TempDir(), "demand.json")
	rawDemand, err := json.Marshal(f.demand)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(demandPath, rawDemand, 0600); err != nil {
		t.Fatal(err)
	}
	conf.DemandObserverConfigFile = demandPath
	makeListener := func() net.Listener {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		return l
	}
	foreignListener := historyListenerAddress{Listener: makeListener(), address: &net.TCPAddr{IP: net.IPv4zero}}
	if err := serveOnListeners(ctx, conf, false, false, makeListener(), foreignListener); err != gatewayrpc.ErrConfiguration {
		t.Fatal("trusting a loopback config let a supplied non-loopback listener through", err)
	}
	review.SnapshotSHA256 = strings.Repeat("a", 64)
	// The missing ledger configuration would fail next. Returning the history
	// configuration error proves the exact reviewed path runs first on the
	// real serving path, not just in the adapter fixture below.
	if err := serveOnListeners(ctx, conf, false, false, makeListener(), nil); err != errCIHealthConfiguration ||
		f.reads.Load() != 0 || digest() != initial {
		t.Fatal("real startup bypassed local review or touched state before review", err)
	}
	review.SnapshotSHA256 = initial
	for _, name := range []string{"unrelated", "ci-health-history-v1.intent", "ci-health-history-v1.stage"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := attachConfiguredCIHistory(ctx, conf, runtime); err != errCIHealthConfiguration {
			t.Fatal("unrelated or unresolved store adopted", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	review.SourceID = strings.Repeat("b", 64)
	if err := attachConfiguredCIHistory(ctx, conf, runtime); err != errCIHealthConfiguration {
		t.Fatal("another source approved by name/repository alone", err)
	}
	review.SourceID = f.runtime.source.SourceID()
	if err := attachConfiguredCIHistory(ctx, conf, runtime); err != nil || runtime.activeHistory() == nil || !runtime.hasHistoryTrends() ||
		f.reads.Load() != 0 || digest() != initial {
		t.Fatal("exact startup did not attach without mutation or source requests", err)
	}
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}},
		Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "local.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ownerToken := strings.Repeat("a", 64)
	handler, err := adminview.New(store, ownerToken, nil, false)
	if err != nil || handler.SetCIHealthSnapshot(runtime.snapshot) != nil {
		t.Fatal("could not attach existing owner endpoint", err)
	}
	read := func(token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/ci-health", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		handler.ServeHTTP(w, r)
		return w
	}
	if res := read(ownerToken); res.Code != 200 || strings.Contains(res.Body.String(), `"history":`) {
		t.Fatal("local store alone made history visible")
	}
	if !runtime.runCycle(ctx) {
		t.Fatal("positive fixture cycle failed")
	}
	if res := read(ownerToken); res.Code != 200 || !strings.Contains(res.Body.String(), `"history":`) || strings.Contains(res.Body.String(), initial) {
		t.Fatal("wrong owner projection after newly authorized observation")
	}
	if res := read(strings.Repeat("b", 64)); res.Code != 401 || strings.Contains(res.Body.String(), `"history":`) {
		t.Fatal("other role saw history")
	}
	runtime.close()
	if res := read(ownerToken); res.Code != 200 || strings.Contains(res.Body.String(), `"history":`) {
		t.Fatal("history view survived startup owner's closure")
	}
	changed := digest()
	if changed == initial {
		t.Fatal("fixture did not commit a revised store")
	}
	restart := openRuntime()
	readsBeforeRestart := f.reads.Load()
	if err := attachConfiguredCIHistory(ctx, conf, restart); err != errCIHealthConfiguration {
		t.Fatal("stale review silently reopened changed state", err)
	}
	review.SnapshotSHA256 = changed
	if err := attachConfiguredCIHistory(ctx, conf, restart); err != errCIHealthConfiguration {
		t.Fatal("new bytes alone authorized the stale reviewed revision", err)
	}
	review.Workflows[0].Revision = &one
	review.Workflows[0].Checkpoint = &one
	if err := attachConfiguredCIHistory(ctx, conf, restart); err != errCIHealthConfiguration {
		t.Fatal("reviewed revision masked a mismatched checkpoint", err)
	}
	review.Workflows[0].Checkpoint = &zero
	if err := attachConfiguredCIHistory(ctx, conf, restart); err != nil || f.reads.Load() != readsBeforeRestart || digest() != changed {
		t.Fatal("exact independent new revision could not reopen read-only", err)
	}
	restart.close()
	fixed, err := cihealth.OpenHistory(ctx, cihealth.HistoryInput{Enabled: true, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	foreign := scope
	foreign.WorkflowID++
	if err := fixed.Apply(ctx, foreign, cihealth.HistoryCursor{}, cihealth.HistoryCursor{Revision: 1}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	fixed.Close()
	review.SnapshotSHA256 = digest()
	if err := attachConfiguredCIHistory(ctx, conf, openRuntime()); err != errCIHealthConfiguration {
		t.Fatal("reviewed directory adopted an unapproved stored workflow checkpoint", err)
	}
}
