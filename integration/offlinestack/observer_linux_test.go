//go:build linux

package offlinestack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/jobobserver"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func observeTerminal(t *testing.T, f *nativeBridgeFixture, repository, permit string, reopen func()) {
	t.Helper()
	ctx := context.Background()
	f.owner.mu.Lock()
	started := f.owner.start.ClaimedAt.Add(time.Nanosecond)
	f.owner.mu.Unlock()
	completed := time.Now().UTC()
	f.owner.mu.Lock()
	f.owner.journalAt = completed
	f.owner.mu.Unlock()
	var terminal atomic.Bool
	var reads atomic.Int32
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	requestCanceled := make(chan struct{}, 1)
	var api string
	gh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if block.Load() {
			entered <- struct{}{}
			<-r.Context().Done()
			requestCanceled <- struct{}{}
			return
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer github-fixture" {
			t.Error("unexpected GitHub auth/mutation")
			w.WriteHeader(401)
			return
		}
		status := "in_progress"
		if terminal.Load() {
			status = "completed"
		}
		switch r.URL.Path {
		case "/repos/example/disposable/actions/runs":
			json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 9, "repository": map[string]any{"full_name": "example/disposable"}}}})
		case "/repos/example/disposable/actions/runs/9/jobs":
			json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": 7, "run_id": 9, "status": status, "labels": []string{"linux"}}}})
		case "/repos/example/disposable/actions/jobs/7":
			var conclusion, ended any
			if terminal.Load() {
				conclusion = "success"
				ended = completed
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "run_id": 9, "run_attempt": 1, "runner_id": 123, "runner_name": "runner-test", "url": api + "/repos/example/disposable/actions/jobs/7", "run_url": api + "/repos/example/disposable/actions/runs/9", "status": status, "conclusion": conclusion, "started_at": started, "completed_at": ended})
		default:
			t.Error("unscoped GitHub path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer gh.Close()
	api = gh.URL
	roots := x509.NewCertPool()
	roots.AddCert(gh.Certificate())
	repo := demandrepair.Repository{GarmID: repository, Host: "github.com", Owner: "example", Name: "disposable"}
	github, e := demandrepair.NewGitHub(gh.URL, "github.com", "fixture", func(context.Context, string) (string, error) { return "github-fixture", nil }, &tls.Config{RootCAs: roots}, []demandrepair.Repository{repo})
	if e != nil {
		t.Fatal(e)
	}
	defer github.Close()
	observer := func() *jobobserver.Service {
		s, e := jobobserver.New(jobobserver.Configuration{Store: f.s, GitHub: github, Repositories: []demandrepair.Repository{repo}, Owners: []jobobserver.Owner{{Connection: f.config.Owner, Policy: f.config.Policy}}, Lookback: time.Hour, Interval: time.Second})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if f.owner.mode == "success" {
		block.Store(true)
		runctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		service := observer()
		go func() { done <- service.Run(runctx) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("observer did not reach real HTTP request")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("observer did not return cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("observer did not join")
		}
		select {
		case <-requestCanceled:
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP request context was not canceled")
		}
		block.Store(false)
		if _, err := f.s.LookupNativeRootFence(ctx, permit); err == nil {
			t.Fatal("canceled observation created receipt")
		}
		snapshot, err := f.s.Snapshot(ctx)
		if err != nil || snapshot.Allocated.Global.Runners != 1 {
			t.Fatal("canceled observation released charge", err)
		}
	}
	first, e := observer().Cycle(ctx)
	if e != nil || first.JobsRecorded != 1 || first.RootFencesRecorded != 0 || first.Unresolved != 1 {
		t.Fatalf("positive progress discovery %+v %v", first, e)
	}
	reopen()
	terminal.Store(true)
	next, e := observer().Cycle(ctx)
	if e != nil || next.JobsRecorded != 1 {
		t.Fatalf("durable job retry %+v %v", next, e)
	}
	if f.owner.mode == "journal-ack-lost" {
		if next.RootFencesRecorded != 0 || next.Unresolved != 1 {
			t.Fatal("missing journal reported fence", next)
		}
		if _, e = f.s.LookupNativeRootFence(ctx, permit); e == nil {
			t.Fatal("lost receipt persisted authority")
		}
		before := reads.Load()
		reopen()
		next, e = observer().Cycle(ctx)
		if e != nil || next.JobsRecorded != 0 || next.RootFencesRecorded != 1 || reads.Load() != before {
			t.Fatalf("terminal restart did not retry only root receipt: %+v %v", next, e)
		}
	} else if next.RootFencesRecorded != 1 {
		t.Fatal("root receipt not durably correlated", next)
	}
	fence, e := f.s.LookupNativeRootFence(ctx, permit)
	if e != nil || fence.JobID != 7 || fence.Qualification != "local_network_closed_containment_unqualified" {
		t.Fatal("wrong historical fence", fence, e)
	}
	before := reads.Load()
	f.owner.mu.Lock()
	journals := f.owner.journalCalls
	f.owner.mu.Unlock()
	reopen()
	last, e := observer().Cycle(ctx)
	if e != nil || last.JobsRecorded != 0 || last.RootFencesRecorded != 0 || reads.Load() != before {
		t.Fatal("historical fence replay caused source operation", last, e)
	}
	f.owner.mu.Lock()
	if f.owner.journalCalls != journals {
		t.Error("historical journal retried as live authority")
	}
	f.owner.mu.Unlock()
	snapshot, e := f.s.Snapshot(ctx)
	if e != nil || snapshot.Allocated.Global.Runners != 1 {
		t.Fatal("terminal/root closure released capacity", e)
	}
}
