package demandrepair

import (
	"context"
	"errors"
	"testing"
	"time"
)

type readerFunc func(context.Context, Repository, time.Time, time.Time, string) (Page, error)

func (f readerFunc) ListJobs(c context.Context, r Repository, a, b time.Time, s string) (Page, error) {
	return f(c, r, a, b, s)
}

var repo = Repository{"garm-repo", "github.example", "owner", "repo"}

func observe(r Reader, b []Baseline) (Result, error) {
	now := time.Now().UTC()
	return Observe(context.Background(), r, []Repository{repo}, b, now.Add(-time.Hour), now)
}
func TestRepairAddsButNeverDropsBaseline(t *testing.T) {
	calls := 0
	r := readerFunc(func(_ context.Context, got Repository, _, _ time.Time, c string) (Page, error) {
		if got != repo {
			t.Fatal("wrong identity")
		}
		calls++
		if c == "" {
			return Page{Jobs: []Job{{1, "completed", []string{"linux"}}, {3, "queued", []string{"LINUX"}}}, Next: "page2"}, nil
		}
		return Page{Jobs: []Job{{4, "queued", []string{"linux"}}}, Terminal: true}, nil
	})
	got, err := observe(r, []Baseline{{repo.GarmID, Job{1, "queued", []string{"linux"}}}, {repo.GarmID, Job{2, "queued", []string{"linux"}}}})
	if err != nil || !got.EnumerationComplete || len(got.Items) != 4 || calls != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if !got.Items[0].Conflict || got.Items[0].Job.Status != "queued" || !got.Items[1].MissingFromReader || got.Items[2].Job.Labels[0] != "linux" {
		t.Fatalf("lost uncertainty: %+v", got)
	}
}
func TestRepairRejectsPartialOrMalformedEnumeration(t *testing.T) {
	cases := map[string]readerFunc{
		"rate limit": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
			return Page{}, errors.New("rate limit")
		},
		"partial": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) { return Page{}, nil },
		"contradiction": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
			return Page{Next: "x", Terminal: true}, nil
		},
		"loop": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
			return Page{Next: "x"}, nil
		},
		"duplicate": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
			return Page{Jobs: []Job{{1, "queued", nil}, {1, "queued", nil}}, Terminal: true}, nil
		},
		"unknown status": func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
			return Page{Jobs: []Job{{1, "waiting", nil}}, Terminal: true}, nil
		},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := observe(r, nil)
			if err != ErrObservation || got.Items != nil || got.EnumerationComplete {
				t.Fatalf("partial result %+v %v", got, err)
			}
		})
	}
}
func TestRepairLabelsConflictAndIdentityIsolation(t *testing.T) {
	r := readerFunc(func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
		return Page{Jobs: []Job{{1, "queued", []string{"arm"}}}, Terminal: true}, nil
	})
	got, err := observe(r, []Baseline{{repo.GarmID, Job{1, "queued", []string{"x64"}}}})
	if err != nil || !got.Items[0].Conflict || got.Items[0].Job.Labels[0] != "x64" {
		t.Fatalf("%+v %v", got, err)
	}
	now := time.Now()
	other := repo
	other.GarmID = "other"
	if _, err = Observe(context.Background(), r, []Repository{repo, other}, nil, now.Add(-time.Hour), now); err != ErrObservation {
		t.Fatal("accepted duplicate identity")
	}
	if _, err = observe(r, []Baseline{{"unmapped", Job{1, "queued", nil}}}); err != ErrObservation {
		t.Fatal("accepted unmapped baseline")
	}
}
func TestRepairRejectsCancelledAndPageOverflow(t *testing.T) {
	r := readerFunc(func(context.Context, Repository, time.Time, time.Time, string) (Page, error) {
		return Page{Jobs: make([]Job, 101), Terminal: true}, nil
	})
	if _, err := observe(r, nil); err != ErrObservation {
		t.Fatal("accepted large page")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now()
	if _, err := Observe(ctx, r, []Repository{repo}, nil, now.Add(-time.Hour), now); err != ErrObservation {
		t.Fatal("accepted cancelled context")
	}
}
