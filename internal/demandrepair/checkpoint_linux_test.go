package demandrepair

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPollCheckpointPreservesHistoryAndFailure(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	o := PollOptions{Path: filepath.Join(dir, "state.json"), InitialFrom: time.Now().Add(-time.Hour), Overlap: time.Minute, MinInterval: time.Minute}
	callback := func(context.Context, []Item, time.Time, time.Time) (Result, error) {
		return Result{EnumerationComplete: true, Items: []Item{{RepositoryID: repo.GarmID, Job: Job{ID: 1, Status: "queued"}}}}, nil
	}
	if _, e := poll(context.Background(), "scope", o, callback); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(o.Path)
	var state checkpoint
	json.Unmarshal(original, &state)
	state.Through = time.Now().Add(-2 * time.Minute)
	aged, _ := json.Marshal(state)
	os.WriteFile(o.Path, aged, 0600)
	called := false
	if _, e := poll(context.Background(), "scope", o, func(_ context.Context, b []Item, from, to time.Time) (Result, error) {
		called = true
		if len(b) != 1 || !from.Equal(state.Through.Add(-time.Minute)) {
			t.Fatal("lost history/overlap")
		}
		return Result{}, ErrObservation
	}); e != ErrObservation || !called {
		t.Fatal("failure not surfaced")
	}
	after, _ := os.ReadFile(o.Path)
	if string(after) != string(aged) {
		t.Fatal("advanced failed checkpoint")
	}
	if _, e := poll(context.Background(), "other", o, callback); e != ErrObservation {
		t.Fatal("accepted changed binding")
	}
}
func TestPollRejectsFastRetryAndSymlink(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	o := PollOptions{Path: filepath.Join(dir, "state"), InitialFrom: time.Now().Add(-time.Hour), Overlap: time.Minute, MinInterval: time.Minute}
	calls := 0
	fn := func(context.Context, []Item, time.Time, time.Time) (Result, error) {
		calls++
		return Result{EnumerationComplete: true, Items: []Item{}}, nil
	}
	if _, e := poll(context.Background(), "scope", o, fn); e != nil {
		t.Fatal(e)
	}
	if _, e := poll(context.Background(), "scope", o, fn); e != ErrObservation || calls != 1 {
		t.Fatal("ignored min interval")
	}
	os.Remove(o.Path)
	os.Symlink("target", o.Path)
	if _, e := poll(context.Background(), "scope", o, fn); e != ErrObservation || calls != 1 {
		t.Fatal("accepted symlink")
	}
}
func TestPollGitHubConnectsCurrentStockAndTransport(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`) })
	o := PollOptions{Path: filepath.Join(dir, "checkpoint"), InitialFrom: time.Now().Add(-time.Hour), Overlap: time.Minute, MinInterval: time.Minute}
	got, e := PollGitHub(context.Background(), c, []Repository{repo}, []Baseline{{repo.GarmID, Job{ID: 10, Status: "queued"}}}, o)
	if e != nil || len(got.Items) != 1 || !got.Items[0].MissingFromReader {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestPollReaderOnlyProvenanceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/actions/runs" {
			fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"repository":{"full_name":"owner/repo"}}]}`)
			return
		}
		fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":1,"run_id":7,"status":"queued","labels":["linux"]}]}`)
	})
	o := PollOptions{Path: filepath.Join(dir, "state"), InitialFrom: time.Now().Add(-time.Hour), Overlap: time.Minute, MinInterval: time.Minute}
	first, e := PollGitHub(context.Background(), c, []Repository{repo}, nil, o)
	if e != nil || first.Items[0].Source != "reader" {
		t.Fatalf("%+v %v", first, e)
	}
	data, _ := os.ReadFile(o.Path)
	var cp checkpoint
	json.Unmarshal(data, &cp)
	cp.Through = time.Now().Add(-2 * time.Minute)
	data, _ = json.Marshal(cp)
	os.WriteFile(o.Path, data, 0600)
	second, e := PollGitHub(context.Background(), c, []Repository{repo}, nil, o)
	if e != nil || second.Items[0].Source != "reader" || second.Items[0].PriorSource != "reader" {
		t.Fatalf("invented stock provenance %+v %v", second, e)
	}
}

func TestPollRetiresOnlyFreshCompletedHistoryWithoutStockClaim(t *testing.T) {
	for _, tc := range []struct {
		name, status            string
		stock, missing, failure bool
		want                    int
	}{
		{name: "completed history", status: "completed", want: 0},
		{name: "current stock conflict", status: "completed", stock: true, want: 1},
		{name: "still queued", status: "queued", want: 1},
		{name: "in progress", status: "in_progress", want: 1},
		{name: "absent is unknown", status: "completed", missing: true, want: 1},
		{name: "failed read keeps checkpoint", status: "completed", failure: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			second := false
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if second && tc.failure {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path == "/repos/owner/repo/actions/runs" {
					if second && tc.missing {
						fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
						return
					}
					fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"repository":{"full_name":"owner/repo"}}]}`)
					return
				}
				status := "queued"
				if second {
					status = tc.status
				}
				fmt.Fprintf(w, `{"total_count":1,"jobs":[{"id":1,"run_id":7,"status":%q,"labels":["linux"]}]}`, status)
			})
			o := PollOptions{Path: filepath.Join(dir, "state"), InitialFrom: time.Now().Add(-time.Hour), Overlap: time.Minute, MinInterval: time.Minute}
			if _, e := PollGitHub(context.Background(), c, []Repository{repo}, nil, o); e != nil {
				t.Fatal(e)
			}
			data, _ := os.ReadFile(o.Path)
			var cp checkpoint
			json.Unmarshal(data, &cp)
			cp.Through = time.Now().Add(-2 * time.Minute)
			// An old status in the checkpoint must never substitute for a fresh read.
			cp.Result.Items[0].ReaderStatus = "completed"
			data, _ = json.Marshal(cp)
			os.WriteFile(o.Path, data, 0600)
			second = true
			var stock []Baseline
			if tc.stock {
				stock = []Baseline{{repo.GarmID, Job{ID: 1, Status: "queued", Labels: []string{"linux"}}}}
			}
			got, e := PollGitHub(context.Background(), c, []Repository{repo}, stock, o)
			after, _ := os.ReadFile(o.Path)
			if tc.failure {
				if e != ErrObservation || string(after) != string(data) {
					t.Fatalf("failed read changed history: %v", e)
				}
				return
			}
			if e != nil || len(got.Items) != tc.want {
				t.Fatalf("got %+v, error %v", got, e)
			}
			if e = json.Unmarshal(after, &cp); e != nil || len(cp.Result.Items) != tc.want {
				t.Fatalf("history not persisted: %+v %v", cp, e)
			}
			if tc.stock && (!got.Items[0].Conflict || got.Items[0].ReaderStatus != "completed") {
				t.Fatal("lost current stock disagreement")
			}
			if tc.missing && got.Items[0].ReaderStatus != "" {
				t.Fatal("reused stale terminal status")
			}
		})
	}
}
