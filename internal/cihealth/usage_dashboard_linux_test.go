package cihealth_test

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestOwnerRouteShowsQualifiedLocalUsageOnlyForCurrentExactCycle(t *testing.T) {
	ctx := t.Context()
	const prefix = "/repos/fixture/example"
	const jobID = int64(9007199254740993)
	to := time.Now().UTC().Truncate(time.Second)
	start := to.Add(-10 * time.Second)
	end := start.Add(6 * time.Second)
	repoData := map[string]any{"id": 1, "full_name": "fixture/example"}
	run := map[string]any{"id": 3, "workflow_id": 2, "run_attempt": 1, "repository": repoData, "status": "completed", "conclusion": "success", "created_at": start.Add(-time.Second), "run_started_at": start}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error(r.Method)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var out any
		switch r.URL.Path {
		case prefix:
			out = repoData
		case prefix + "/actions/runs":
			rows := []map[string]any{}
			if r.URL.Query().Get("status") == "" {
				rows = append(rows, run)
			}
			out = map[string]any{"workflow_runs": rows, "total_count": len(rows)}
		case prefix + "/actions/runs/3/attempts/1":
			out = run
		case prefix + "/actions/runs/3/attempts/1/jobs":
			out = map[string]any{"total_count": 1, "jobs": []map[string]any{{"id": jobID, "run_id": 3, "run_attempt": 1, "status": "completed", "conclusion": "success", "started_at": start, "completed_at": end}}}
		default:
			t.Errorf("unexpected scoped read %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token, ca := filepath.Join(dir, "token"), filepath.Join(dir, "ca")
	if err := os.WriteFile(token, []byte("local-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	repo := demandrepair.Repository{GarmID: "fixture", Host: "github.example", Owner: "fixture", Name: "example"}
	cfg := demandobserver.Config{Enabled: true, GitHubEndpoint: server.URL, GitHubAuthority: repo.Host, GitHubTokenFile: token, GitHubCAFile: ca, Repositories: []demandrepair.Repository{repo}, CheckpointPath: filepath.Join(dir, "unused"), InitialFrom: to.Add(-time.Hour), IntervalSeconds: 60, MaxBackoffSeconds: 60}
	input := demandobserver.CIHealthInput{Enabled: true, Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: repo.GarmID, GitHubID: 1}}}
	open := func() *demandobserver.CIHealthSource {
		s, err := demandobserver.NewCIHealthSource(cfg, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	owner, shadow := open(), open()
	cycle, err := cihealthsource.ReadCycle(ctx, owner, []string{repo.GarmID}, to.Add(-time.Minute), to)
	if err != nil || len(cycle.Facts()) != 1 || len(cycle.Facts()[0].Jobs) != 1 {
		t.Fatalf("fixture cycle %v: %v", cycle.State(), err)
	}
	fact := cycle.Facts()[0]
	store, history, cursor := cihealth.RetainedDisplayFixture(t, fact)
	key := fact.Jobs[0].Key
	scope := cihealth.HistoryScope{Source: key.Attempt.Source, RepositoryID: 1, WorkflowID: 2}
	adapter, err := cihealthsource.NewCycleHistory(history, owner, []cihealth.HistoryScope{scope})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := adapter.ObserveJobUsage(ctx, cycle, store, cursor, key)
	if !ok {
		t.Fatal("positive scoped observation missing")
	}
	if _, _, ok := u.ForCycle(cycle, shadow); ok {
		t.Fatal("shadow source accepted")
	}
	another, err := cihealthsource.ReadCycle(ctx, owner, []string{repo.GarmID}, to.Add(-time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := u.ForCycle(another, owner); ok {
		t.Fatal("independent cycle reused observation")
	}
	foreign := key
	foreign.Attempt.Attempt++
	if _, ok := adapter.ObserveJobUsage(ctx, cycle, store, cursor, foreign); ok {
		t.Fatal("foreign attempt accepted")
	}

	budget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	ledger, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: to}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })
	auth := strings.Repeat("a", 64)
	handler, err := adminview.New(ledger, auth, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := adminview.NewCIHealthSnapshot([]adminview.CIHealthRepository{{Source: scope.Source, RepositoryID: 1}})
	if err != nil || handler.SetCIHealthSnapshot(snap) != nil {
		t.Fatal("snapshot init", err)
	}
	if err := snap.PublishLiveTimingWithUsage(cycle, owner, []cihealthsource.ObservedJobUsage{u}); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/ci-health", nil)
		r.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		return w.Body.Bytes()
	}
	var result struct {
		Timing struct {
			Attempts []struct {
				Jobs []struct {
					ID    string
					Usage *cihealth.Wire `json:"resource_usage"`
				}
			}
		}
	}
	body := read()
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	job := result.Timing.Attempts[0].Jobs[0]
	if job.ID != "9007199254740993" || job.Usage == nil || job.Usage.State != cihealth.VerifiedUsagePartial || job.Usage.Measured == nil {
		t.Fatalf("missing scoped partial evidence: %s", body)
	}
	m := job.Usage.Measured
	if m.CPU == nil || m.RAM == nil || m.CPU.CoveredNanoseconds != "2000000000" || m.Disk.Read == nil || m.Disk.Read.Bytes != "0" || m.Disk.Write != nil || m.Disk.Used != nil {
		t.Fatal("coverage, measured zero or missing disk lost", m)
	}
	owner.Close()
	after := read()
	if bytes.Contains(after, []byte("resource_usage")) || !bytes.Contains(after, []byte("9007199254740993")) {
		t.Fatal("source revocation lost timing or retained usage")
	}
}
