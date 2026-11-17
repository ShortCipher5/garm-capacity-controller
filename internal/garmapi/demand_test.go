package garmapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func demandPool(id string) map[string]any {
	p := discoveryPool(id, "capacity")
	p["repo_id"] = managedRepoID
	p["enabled"] = true
	return p
}
func demandJob(id int64) map[string]any {
	return map[string]any{"workflow_job_id": id, "repo_id": managedRepoID, "status": "queued", "labels": []string{"LINUX"}, "created_at": time.Now().UTC().Add(-time.Minute), "locked_by": "00000000-0000-0000-0000-000000000000", "name": "private-name", "workflow_run_url": "private-url"}
}
func demandFixture(t *testing.T, jobs string, mutate func(string, int) any) *DemandClient {
	t.Helper()
	var calls atomic.Int32
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("unexpected mutation/auth")
		}
		switch r.URL.Path {
		case "/api/v1/pools":
			n := int(calls.Add(1))
			v := any([]map[string]any{demandPool(managedPoolID), demandPool(otherID)})
			if mutate != nil {
				if x := mutate(r.URL.Path, n); x != nil {
					v = x
				}
			}
			json.NewEncoder(w).Encode(v)
		case "/api/v1/jobs":
			fmt.Fprint(w, jobs)
		case "/api/v1/instances":
			v := any([]any{})
			if mutate != nil {
				if x := mutate(r.URL.Path, 0); x != nil {
					v = x
				}
			}
			json.NewEncoder(w).Encode(v)
		default:
			t.Errorf("unexpected URL %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	c, e := NewDemand(server.URL, "token", trust, []string{"capacity"}, []string{managedRepoID})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c
}
func encodeJobs(t *testing.T, jobs ...map[string]any) string {
	t.Helper()
	b, e := json.Marshal(jobs)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestDemandKeepsOneJobWithMultipleCandidatesAndRedacts(t *testing.T) {
	c := demandFixture(t, encodeJobs(t, demandJob(1)), nil)
	before := time.Now()
	o, e := c.Observe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(o.Jobs) != 1 || len(o.Jobs[0].CandidatePoolIDs) != 2 || o.Jobs[0].Labels[0] != "linux" || o.Atomic || o.MissedWebhookCoverage || o.StartedAt.Before(before) || o.FinishedAt.Before(o.StartedAt) || len(o.Digest) != 64 {
		t.Fatalf("%+v", o)
	}
	b, _ := json.Marshal(o)
	if strings.Contains(string(b), "private-") || strings.Contains(string(b), "token") {
		t.Fatal("private content escaped")
	}
}
func TestDemandRejectsDetectedPoolDrift(t *testing.T) {
	c := demandFixture(t, encodeJobs(t, demandJob(1)), func(path string, n int) any {
		if path == "/api/v1/pools" && n == 2 {
			p := demandPool(managedPoolID)
			p["max_runners"] = 99
			return []any{p, demandPool(otherID)}
		}
		return nil
	})
	if o, e := c.Observe(context.Background()); e == nil || o.Jobs != nil {
		t.Fatal("partial/drifting observation accepted")
	}
}
func TestDemandReportsSupplyWithoutAssumingJobBinding(t *testing.T) {
	c := demandFixture(t, encodeJobs(t, demandJob(1)), func(path string, n int) any {
		if path != "/api/v1/instances" {
			return nil
		}
		out := []any{}
		for i, state := range []string{"running", "pending_create", "running", "error"} {
			r := map[string]any{"name": fmt.Sprint("runner-", i), "provider_name": "capacity", "pool_id": managedPoolID, "status": state}
			switch i {
			case 0:
				r["runner_status"] = "idle"
			case 1:
				r["runner_status"] = "pending"
			case 2:
				r["runner_status"] = "active"
			}
			out = append(out, r)
		}
		return out
	})
	o, e := c.Observe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var found bool
	for _, s := range o.Supply {
		if s.PoolID == managedPoolID {
			found = true
			if s.Idle != 1 || s.Creating != 1 || s.Busy != 1 || s.Other != 1 {
				t.Fatalf("%+v", s)
			}
		}
	}
	if !found || len(o.Jobs) != 1 {
		t.Fatal("supply consumed queue or missing")
	}
}
func TestDemandRejectsMalformedAndDuplicateJobs(t *testing.T) {
	base := demandJob(1)
	dup := encodeJobs(t, base, base)
	cases := []string{"null", "{}", "[] trailing", dup}
	for _, field := range []string{"repo_id", "workflow_job_id", "labels", "created_at", "status", "locked_by"} {
		j := demandJob(1)
		j[field] = nil
		cases = append(cases, encodeJobs(t, j))
	}
	j := demandJob(1)
	j["Labels"] = j["labels"]
	cases = append(cases, encodeJobs(t, j))
	j = demandJob(1)
	j["created_at"] = time.Now().Add(time.Hour)
	cases = append(cases, encodeJobs(t, j))
	j = demandJob(1)
	j["labels"] = []string{"linux", "LINUX"}
	cases = append(cases, encodeJobs(t, j))
	for i, body := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			c := demandFixture(t, body, nil)
			if _, e := c.Observe(context.Background()); e == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
}
func TestDemandScopeStatusesLockedAndDisabledPools(t *testing.T) {
	j := demandJob(1)
	j["locked_by"] = managedRepoID
	done := demandJob(2)
	done["status"] = "completed"
	other := demandJob(3)
	other["repo_id"] = otherID
	c := demandFixture(t, encodeJobs(t, j, done, other), func(path string, n int) any {
		if path == "/api/v1/pools" {
			p := demandPool(otherID)
			p["enabled"] = false
			return []any{demandPool(managedPoolID), p}
		}
		return nil
	})
	o, e := c.Observe(context.Background())
	if e != nil || len(o.Jobs) != 1 || !o.Jobs[0].Locked || len(o.Jobs[0].CandidatePoolIDs) != 1 {
		t.Fatalf("%+v %v", o, e)
	}
}
func TestDemandCancelledReturnsNoPartialObservation(t *testing.T) {
	c := demandFixture(t, "[]", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if o, e := c.Observe(ctx); e == nil || o.Pools != nil {
		t.Fatal("cancelled read accepted")
	}
}
func TestDemandRequestStartAgeIsNotCompletionTime(t *testing.T) {
	c := demandFixture(t, "[]", func(path string, n int) any {
		if path == "/api/v1/instances" {
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	})
	o, e := c.Observe(context.Background())
	if e != nil || o.FinishedAt.Sub(o.StartedAt) < 20*time.Millisecond {
		t.Fatalf("%+v %v", o, e)
	}
}

func TestDemandHTTPFailuresAndRedirectDoNotYieldPartial(t *testing.T) {
	for _, status := range []int{401, 403, 302, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/pools" {
					json.NewEncoder(w).Encode([]any{demandPool(managedPoolID)})
					return
				}
				w.Header().Set("Location", "/unexpected")
				w.WriteHeader(status)
			}))
			c, e := NewDemand(server.URL, "token", trust, []string{"capacity"}, []string{managedRepoID})
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			if o, e := c.Observe(context.Background()); e == nil || o.Jobs != nil {
				t.Fatal("failed collection accepted")
			}
		})
	}
}
func TestDemandBoundsCollectionAndOutput(t *testing.T) {
	oversized := `["` + strings.Repeat("x", 8<<20) + `"]`
	tooMany := `[` + strings.Repeat(`{"status":"completed"},`, 10000) + `{"status":"completed"}]`
	for _, body := range []string{oversized, tooMany} {
		c := demandFixture(t, body, nil)
		if _, e := c.Observe(context.Background()); e == nil {
			t.Fatal("unbounded jobs accepted")
		}
	}
}
