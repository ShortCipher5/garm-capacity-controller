package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func TestCIHealthRuntimeReadsExactGitHubTimingToOwnerAPI(t *testing.T) {
	const (
		repoID    int64 = 9007199254740993
		workflow  int64 = 9007199254740995
		runID     int64 = 9007199254740997
		jobID     int64 = 9007199254740999
		attempt   int64 = 2
		repoRoot        = "/repos/fixture-owner/fixture-repo"
		roleToken       = "fixture-github-action-reader-token"
	)
	dir := t.TempDir()
	writePrivate := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	repo := map[string]any{"id": repoID, "full_name": "fixture-owner/fixture-repo"}
	start := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
	run := map[string]any{
		"id": runID, "workflow_id": workflow, "run_attempt": attempt, "name": "fixture-sensitive-run-name",
		"repository": repo, "created_at": start.Add(-time.Minute), "run_started_at": start, "status": "completed", "conclusion": "success",
	}
	jobStart := start.Add(5 * time.Second)
	jobs := []any{map[string]any{
		"id": jobID, "run_id": runID, "run_attempt": attempt, "status": "completed", "conclusion": "success",
		"started_at": jobStart, "completed_at": jobStart.Add(20 * time.Second), "name": "fixture-sensitive-job-name",
		"runner_id": 9, "runner_name": "fixture-sensitive-runner-name",
		"steps": []any{
			map[string]any{"number": 3, "name": "Run fixture-sensitive-shell", "status": "completed", "conclusion": "neutral",
				"started_at": jobStart.Add(time.Second), "completed_at": jobStart.Add(8 * time.Second)},
			map[string]any{"number": 9, "name": "fixture-sensitive-step-name", "status": "waiting", "conclusion": nil},
		},
	}}
	exactAttempt := fmt.Sprintf("%s/actions/runs/%d/attempts/%d", repoRoot, runID, attempt)
	var reads atomic.Int32
	var jobReads atomic.Int32
	github := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || r.TLS == nil || r.Header.Get("Authorization") != "Bearer "+roleToken {
			t.Errorf("unexpected upstream method, transport or role")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		emit := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		q := r.URL.Query()
		switch r.URL.Path {
		case repoRoot + "/actions/runs":
			if q.Get("page") != "1" || q.Get("per_page") != "50" {
				t.Error("unexpected pagination")
			}
			switch q.Get("status") {
			case "queued", "in_progress", "requested", "waiting", "pending":
				if q.Has("created") {
					t.Error("active scan was incorrectly age-filtered")
				}
				emit(map[string]any{"total_count": 0, "workflow_runs": []any{}})
			case "":
				if !q.Has("created") {
					t.Error("unbounded historical scan")
				}
				emit(map[string]any{"total_count": 1, "workflow_runs": []any{run}})
			default:
				t.Error("unexpected workflow status selector")
				w.WriteHeader(http.StatusBadRequest)
			}
		case repoRoot:
			emit(repo)
		case exactAttempt:
			emit(run)
		case exactAttempt + "/jobs":
			if q.Get("page") != "1" || q.Get("per_page") != "50" {
				t.Error("unexpected exact-attempt jobs page")
			}
			jobReads.Add(1)
			emit(map[string]any{"total_count": len(jobs), "jobs": jobs})
		default:
			t.Errorf("request escaped the fixture repository read surface: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	token := writePrivate("github.token", []byte(roleToken))
	trust := writePrivate("github.ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: github.Certificate().Raw}))

	const githubHost = "github.fixture"
	const garmID = "fixture-repo"
	demandConfig := demandobserver.Config{
		Enabled: true, GitHubEndpoint: github.URL, GitHubAuthority: githubHost, GitHubTokenFile: token, GitHubCAFile: trust,
		Repositories: []demandrepair.Repository{
			{GarmID: garmID, Host: githubHost, Owner: "fixture-owner", Name: "fixture-repo"},
			{GarmID: "not-enrolled", Host: githubHost, Owner: "fixture-owner", Name: "different-repo"},
		},
		CheckpointPath: filepath.Join(dir, "unused-demand-checkpoint"), InitialFrom: start, IntervalSeconds: 60, MaxBackoffSeconds: 90,
	}
	raw, err := json.Marshal(demandConfig)
	if err != nil {
		t.Fatal(err)
	}
	demandPath := writePrivate("demand-observer.json", raw)
	loaded, err := demandobserver.Load(demandPath)
	if err != nil {
		t.Fatal(err)
	}
	keys := credentials(t)
	owner := strings.Repeat("a", 64)
	management := strings.Repeat("b", 64)
	adminCfg := &adminConfiguration{
		Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: writePrivate("owner.token", []byte(owner)),
		EnableExperimentalManagement: true, ManagementTokenFile: writePrivate("management.token", []byte(management)),
	}
	config := configuration{Admin: adminCfg, DemandObserverConfigFile: demandPath, EnableCIHealth: true,
		CIHealth: &ciHealthConfiguration{Repositories: []demandobserver.CIHealthRepositoryPin{{GarmID: garmID, GitHubID: repoID}}},
	}
	runtime, err := buildCIHealth(config, &loaded)
	if err != nil || runtime == nil {
		t.Fatalf("CI Health runtime not created: %v", err)
	}
	defer runtime.close()
	if reads.Load() != 0 {
		t.Fatal("construction polled GitHub")
	}
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}},
		Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enabled, cleanup, err := newAdminServerWithCIHealth(adminCfg, store, false, runtime.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	disabled, cleanup, err := newAdminServer(adminCfg, store, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	const route = "/admin/api/v1/ci-health"
	off := httptest.NewRequest(http.MethodGet, "https://127.0.0.1"+route, nil)
	off.Header.Set("Authorization", "Bearer "+owner)
	offResponse := httptest.NewRecorder()
	disabled.Handler.ServeHTTP(offResponse, off)
	if offResponse.Code != http.StatusNotFound {
		t.Fatal("disabled startup left CI Health available")
	}

	listener, err := net.Listen("tcp", enabled.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() { served <- enabled.ServeTLS(listener, "", "") }()
	defer func() {
		_ = enabled.Close()
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("fixture admin listener shutdown: %v", err)
		}
	}()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: keys.root, MinVersion: tls.VersionTLS13},
	}}
	defer client.CloseIdleConnections()
	get := func(method, credential string) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, "https://"+listener.Addr().String()+route, nil)
		if err != nil {
			t.Fatal(err)
		}
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(b)
	}
	if status, body := get(http.MethodGet, owner); status != 200 || !strings.Contains(body, `"state":"unavailable"`) || reads.Load() != 0 {
		t.Fatalf("unstarted service did not remain unavailable: %d %s", status, body)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); runtime.run(ctx) }()
	var stopChecked, stopped bool
	stop := func() bool {
		if stopChecked {
			return stopped
		}
		stopChecked = true
		cancel()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			stopped = true
		case <-timer.C:
			github.CloseClientConnections()
		}
		return stopped
	}
	defer func() {
		if !stop() {
			t.Error("canceled CI Health poller did not exit")
		}
	}()
	type span struct {
		Basis       string `json:"basis"`
		Nanoseconds string `json:"nanoseconds"`
	}
	type row struct {
		Repository string `json:"repository_id"`
		Workflow   string `json:"workflow_id"`
		Run        string `json:"run_id"`
		Attempt    string `json:"attempt"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Wall       *span  `json:"wall"`
		Jobs       []struct {
			ID         string `json:"id"`
			Conclusion string `json:"conclusion"`
			Queue      *span  `json:"queue"`
			Execution  *span  `json:"execution"`
			Steps      []struct {
				Number     string `json:"number"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				Execution  *span  `json:"execution"`
				Elapsed    *span  `json:"elapsed"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	var view struct {
		State    string `json:"state"`
		Coverage string `json:"coverage"`
		Timing   struct {
			Attempts []row `json:"attempts"`
		} `json:"timing"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := get(http.MethodGet, owner)
		if code != 200 || json.Unmarshal([]byte(body), &view) != nil {
			t.Fatalf("unexpected timing response: %d %s", code, body)
		}
		for _, private := range []string{roleToken, "fixture-sensitive", github.URL} {
			if strings.Contains(body, private) {
				t.Fatal("view exposed raw upstream or secret")
			}
		}
		if view.State == "observed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not publish: reads=%d exact jobs=%d view=%s", reads.Load(), jobReads.Load(), body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Coverage != "observed_subset" || len(view.Timing.Attempts) != 1 || jobReads.Load() != 1 {
		t.Fatalf("stopped, unscoped, or inaccurate cohort: %+v", view)
	}
	a := view.Timing.Attempts[0]
	if a.Repository != fmt.Sprint(repoID) || a.Workflow != fmt.Sprint(workflow) || a.Run != fmt.Sprint(runID) || a.Attempt != "2" ||
		a.Status != "completed" || a.Conclusion != "success" || a.Wall != nil || len(a.Jobs) != 1 {
		t.Fatalf("exact rerun identity/unknown execution boundaries changed: %+v", a)
	}
	job := a.Jobs[0]
	if job.ID != fmt.Sprint(jobID) || job.Conclusion != "success" || job.Queue != nil || job.Execution == nil || *job.Execution != (span{"observed_endpoints", "20000000000"}) ||
		len(job.Steps) != 2 || job.Steps[0].Number != "3" || job.Steps[0].Conclusion != "other_terminal" || job.Steps[0].Execution == nil ||
		*job.Steps[0].Execution != (span{"observed_endpoints", "7000000000"}) || job.Steps[1].Number != "9" ||
		job.Steps[1].Status != "unknown" || job.Steps[1].Conclusion != "" || job.Steps[1].Execution != nil || job.Steps[1].Elapsed != nil {
		t.Fatalf("job/step timing or unknown evidence changed: %+v", job)
	}
	before := reads.Load()
	for _, role := range []string{"", management} {
		if status, body := get(http.MethodGet, role); status != 401 || body != "" {
			t.Fatalf("non-owner role saw timing: %d %s", status, body)
		}
	}
	if status, body := get(http.MethodPost, owner); status != 405 || body != "" {
		t.Fatalf("timing route accepted a write: %d %s", status, body)
	}
	if !stop() {
		return // Deferred cleanup reports the failure without joining a second time.
	}
	if code, body := get(http.MethodGet, owner); code != 200 || !strings.Contains(body, `"state":"unavailable"`) ||
		strings.Contains(body, fmt.Sprint(runID)) || reads.Load() != before {
		t.Fatalf("canceled collector retained timing or scheduled another read: %d %s", code, body)
	}
	if _, err := os.Stat(loaded.CheckpointPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CI Health reused the demand observer's checkpoint")
	}
}
