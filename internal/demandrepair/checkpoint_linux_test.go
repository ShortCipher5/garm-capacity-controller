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
	callback := func(context.Context, []Baseline, time.Time, time.Time) (Result, error) {
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
	if _, e := poll(context.Background(), "scope", o, func(_ context.Context, b []Baseline, from, to time.Time) (Result, error) {
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
	fn := func(context.Context, []Baseline, time.Time, time.Time) (Result, error) {
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
