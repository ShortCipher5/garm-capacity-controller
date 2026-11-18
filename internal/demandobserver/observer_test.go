package demandobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func fixture(t *testing.T) (Config, *bool, *string) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	failed := false
	secret := "test-token"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("mutation %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("incorrect credential")
			w.WriteHeader(401)
			return
		}
		if failed {
			w.WriteHeader(503)
			fmt.Fprint(w, "private-upstream-content")
			return
		}
		switch r.URL.Path {
		case "/api/v1/pools", "/api/v1/jobs", "/api/v1/instances":
			fmt.Fprint(w, "[]")
		case "/repos/owner/repo/actions/runs":
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(dir, "ca")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600)
	key := filepath.Join(dir, "token")
	os.WriteFile(key, []byte(secret), 0600)
	return Config{Enabled: true, GarmEndpoint: server.URL, GarmTokenFile: key, GarmCAFile: ca, GitHubEndpoint: server.URL, GitHubAuthority: "github.example", GitHubTokenFile: key, GitHubCAFile: ca, Providers: []string{"capacity"}, Repositories: []demandrepair.Repository{{GarmID: "11111111-1111-4111-8111-111111111111", Host: "github.example", Owner: "owner", Name: "repo"}}, CheckpointPath: filepath.Join(dir, "checkpoint"), InitialFrom: time.Now().Add(-time.Hour), IntervalSeconds: 60, MaxBackoffSeconds: 240}, &failed, &secret
}
func TestObservationUsesRealReadOnlyTransportsAndCheckpoint(t *testing.T) {
	c, failed, secret := fixture(t)
	*failed = true
	first := Observe(context.Background(), c)
	if first.Status != "garm_unavailable" || first.Counts != nil {
		t.Fatalf("%+v", first)
	}
	*failed = false
	*secret = "rotated-token"
	os.WriteFile(c.GarmTokenFile, []byte(*secret), 0600)
	next := Observe(context.Background(), c)
	if next.Status != "observed" || next.Counts == nil || next.Counts.ObservedDemand != 0 || next.Atomic || next.MissedWebhookCoverage {
		t.Fatalf("%+v", next)
	}
	if _, e := os.Stat(c.CheckpointPath); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(next)
	for _, s := range []string{*secret, c.GarmEndpoint, c.GarmTokenFile, "private-upstream-content"} {
		if strings.Contains(string(data), s) {
			t.Fatal("sensitive content emitted")
		}
	}
	// A too-soon checkpoint retry is a failure, not fresh zero demand.
	retry := Observe(context.Background(), c)
	if retry.Status != "github_or_checkpoint_unavailable" || retry.Counts != nil {
		t.Fatalf("%+v", retry)
	}
}
func TestLoadAndCancellation(t *testing.T) {
	c, _, _ := fixture(t)
	path := filepath.Join(filepath.Dir(c.CheckpointPath), "config")
	data, _ := json.Marshal(c)
	os.WriteFile(path, data, 0600)
	if _, e := Load(path); e != nil {
		t.Fatal(e)
	}
	os.Chmod(path, 0644)
	if _, e := Load(path); e == nil {
		t.Fatal("public config accepted")
	}
	os.Chmod(path, 0600)
	data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
	os.WriteFile(path, data, 0600)
	if _, e := Load(path); e == nil {
		t.Fatal("unknown option accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := Run(ctx, c, io.Discard, false); e == nil {
		t.Fatal("cancel ignored")
	}
	c.Enabled = false
	if e := Run(context.Background(), c, io.Discard, true); e == nil {
		t.Fatal("disabled observer enabled")
	}
}
func TestBackoffAndStaleCounts(t *testing.T) {
	c := Config{IntervalSeconds: 60, MaxBackoffSeconds: 240}
	calls := 0
	var delays []time.Duration
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poll := func(context.Context, Config) Event {
		calls++
		status := "garm_unavailable"
		if calls == 4 {
			status = "observed"
		}
		return Event{Mode: "observation_only", Status: status, FinishedAt: time.Unix(int64(calls), 0), Counts: &Counts{ObservedDemand: 4}}
	}
	sleep := func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		if len(delays) == 5 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if e := run(ctx, c, &out, false, poll, sleep); e != context.Canceled {
		t.Fatal(e)
	}
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 60 * time.Second, 60 * time.Second}
	if fmt.Sprint(delays) != fmt.Sprint(want) {
		t.Fatalf("delays %v", delays)
	}
	decoder := json.NewDecoder(&out)
	for i := 1; i <= 5; i++ {
		var event Event
		if e := decoder.Decode(&event); e != nil {
			t.Fatal(e)
		}
		if i != 4 && event.Counts != nil {
			t.Fatal("stale counts shown")
		}
		if i >= 4 && (event.LastSuccessAt == nil || event.LastSuccessAt.Unix() != 4) {
			t.Fatal("lost success age")
		}
	}
}
func TestOnceFailureAndOutputFailure(t *testing.T) {
	c := Config{IntervalSeconds: 60, MaxBackoffSeconds: 240}
	fail := func(context.Context, Config) Event { return Event{Status: "garm_unavailable"} }
	if e := run(context.Background(), c, io.Discard, true, fail, wait); e != ErrObserver {
		t.Fatal(e)
	}
	if e := run(context.Background(), c, badWriter{}, true, fail, wait); e != ErrObserver {
		t.Fatal(e)
	}
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestSnapshotPortReturnsRealScopedDataWithoutSummaryDisclosure(t *testing.T) {
	c, _, _ := fixture(t)
	event, snapshot := ObserveSnapshot(context.Background(), c)
	if event.Status != "observed" || snapshot == nil {
		t.Fatalf("missing successful snapshot: %+v", event)
	}
	if snapshot.Stock.Digest == "" || snapshot.Stock.StartedAt.IsZero() ||
		snapshot.Stock.FinishedAt.Before(snapshot.Stock.StartedAt) ||
		!snapshot.Repair.EnumerationComplete || snapshot.Repair.StartedAt.IsZero() {
		t.Fatalf("lost source observations: %+v", snapshot)
	}
	if snapshot.Stock.Atomic || snapshot.Stock.MissedWebhookCoverage {
		t.Fatal("invented authority")
	}
	raw, err := json.Marshal(event)
	if err != nil || strings.Contains(string(raw), snapshot.Stock.Digest) ||
		strings.Contains(string(raw), "Stock") || strings.Contains(string(raw), "Repair") {
		t.Fatal("full data leaked into public summary")
	}
	// Checkpoint throttling must not return the last successful snapshot.
	event, snapshot = ObserveSnapshot(context.Background(), c)
	if event.Status == "observed" || snapshot != nil {
		t.Fatal("failed refresh returned old or partial source data")
	}
}

func TestSnapshotPortFailuresNeverReturnPartialDemand(t *testing.T) {
	for _, failure := range []string{"garm", "github", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			c, failed, _ := fixture(t)
			ctx := context.Background()
			switch failure {
			case "garm":
				*failed = true
			case "github":
				// GARM succeeds, then the independently refreshed GitHub
				// credential fails. Even complete stock rows must be discarded.
				c.GitHubTokenFile = filepath.Join(filepath.Dir(c.CheckpointPath), "missing")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			event, snapshot := ObserveSnapshot(ctx, c)
			if event.Status == "observed" || event.Counts != nil || snapshot != nil {
				t.Fatalf("partial snapshot escaped: %+v", event)
			}
		})
	}
}
