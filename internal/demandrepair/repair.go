// Package demandrepair combines stock queue observations with explicitly scoped
// read-only job enumeration. It never writes webhooks, grants, or release evidence.
package demandrepair

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrObservation = errors.New("demand repair observation incomplete or invalid")

type Repository struct{ GarmID, Host, Owner, Name string }
type Job struct {
	ID     int64
	Status string
	Labels []string
}
type Page struct {
	Jobs []Job
	Next string
	// Terminal states that this is the last page of the requested enumeration.
	// It is not proof that the API exposed all relevant historical events.
	Terminal bool
}

// Reader must enumerate only the exact repository requested, for the exact
// interval, including all workflow runs, job pages and attempts in that interval.
// Implementations must reject partial results, hidden pagination, permission
// failures, rate limits and ambiguous identity. No implementation is supplied here.
// A real implementation must honor context cancellation and bound HTTP bodies.
type Reader interface {
	ListJobs(context.Context, Repository, time.Time, time.Time, string) (Page, error)
}
type Baseline struct {
	RepositoryID string
	Job          Job
}
type Item struct {
	RepositoryID      string
	Job               Job
	Conflict          bool
	MissingFromReader bool
	Source            string
	// PriorSource records provenance retained from an earlier poll, not current stock evidence.
	PriorSource string
}
type Result struct {
	Items                 []Item
	StartedAt, FinishedAt time.Time
	// EnumerationComplete only means the injected reader reached terminal pages.
	// It does not establish webhook coverage, atomicity or a durable checkpoint.
	EnumerationComplete bool
}

func validJob(j Job) bool {
	if j.ID <= 0 || (j.Status != "queued" && j.Status != "in_progress" && j.Status != "completed") || len(j.Labels) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, l := range j.Labels {
		if strings.TrimSpace(l) == "" || strings.ContainsAny(l, "\x00\r\n") || len(l) > 256 || seen[strings.ToLower(l)] {
			return false
		}
		seen[strings.ToLower(l)] = true
	}
	return true
}
func canonical(j Job) Job {
	j.Labels = append([]string(nil), j.Labels...)
	for i := range j.Labels {
		j.Labels[i] = strings.ToLower(j.Labels[i])
	}
	sort.Strings(j.Labels)
	return j
}
func equalLabels(a, b []string) bool   { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func key(repo string, id int64) string { return fmt.Sprintf("%s/%d", repo, id) }

// Observe adds positively observed queued jobs and retains every baseline queue
// item. Missing rows or terminal upstream jobs never erase baseline demand;
// disagreements are explicit. It returns no partial result on incomplete reads.
func Observe(ctx context.Context, r Reader, repos []Repository, baseline []Baseline, from, to time.Time) (Result, error) {
	started := time.Now().UTC()
	if ctx == nil || ctx.Err() != nil || r == nil || len(repos) == 0 || len(repos) > 100 || len(baseline) > 10000 || from.IsZero() || !from.Before(to) || to.After(started) {
		return Result{}, ErrObservation
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	mapping := map[string]Repository{}
	identities := map[string]bool{}
	for _, repo := range repos {
		for _, s := range []string{repo.GarmID, repo.Host, repo.Owner, repo.Name} {
			if s == "" || len(s) > 256 || strings.TrimSpace(s) != s || strings.ContainsAny(s, "/\\\x00\r\n") {
				return Result{}, ErrObservation
			}
		}
		identity := strings.ToLower(repo.Host + "/" + repo.Owner + "/" + repo.Name)
		if _, exists := mapping[repo.GarmID]; exists || identities[identity] {
			return Result{}, ErrObservation
		}
		mapping[repo.GarmID] = repo
		identities[identity] = true
	}
	merged := map[string]Item{}
	for _, b := range baseline {
		if _, ok := mapping[b.RepositoryID]; !ok || !validJob(b.Job) || b.Job.Status != "queued" {
			return Result{}, ErrObservation
		}
		k := key(b.RepositoryID, b.Job.ID)
		if _, exists := merged[k]; exists {
			return Result{}, ErrObservation
		}
		merged[k] = Item{RepositoryID: b.RepositoryID, Job: canonical(b.Job), Source: "stock", MissingFromReader: true}
	}
	count := 0
	pages := 0
	for _, repo := range repos {
		cursor := ""
		seenCursors := map[string]bool{}
		seenJobs := map[int64]bool{}
		for {
			if ctx.Err() != nil || pages >= 1000 {
				return Result{}, ErrObservation
			}
			pages++
			page, err := r.ListJobs(ctx, repo, from, to, cursor)
			if err != nil || ctx.Err() != nil || len(page.Jobs) > 100 || len(page.Next) > 2048 || page.Terminal != (page.Next == "") {
				return Result{}, ErrObservation
			}
			for _, j := range page.Jobs {
				count++
				if count > 10000 || !validJob(j) || seenJobs[j.ID] {
					return Result{}, ErrObservation
				}
				seenJobs[j.ID] = true
				j = canonical(j)
				k := key(repo.GarmID, j.ID)
				if existing, ok := merged[k]; ok {
					existing.MissingFromReader = false
					existing.Conflict = j.Status != "queued" || !equalLabels(existing.Job.Labels, j.Labels)
					existing.Source = "stock+reader"
					merged[k] = existing
				} else if j.Status == "queued" {
					merged[k] = Item{RepositoryID: repo.GarmID, Job: j, Source: "reader"}
				}
			}
			if page.Terminal {
				break
			}
			if seenCursors[page.Next] {
				return Result{}, ErrObservation
			}
			seenCursors[page.Next] = true
			cursor = page.Next
		}
	}
	if len(merged) > 10000 {
		return Result{}, ErrObservation
	}
	items := make([]Item, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RepositoryID != items[j].RepositoryID {
			return items[i].RepositoryID < items[j].RepositoryID
		}
		return items[i].Job.ID < items[j].Job.ID
	})
	return Result{Items: items, StartedAt: started, FinishedAt: time.Now().UTC(), EnumerationComplete: true}, nil
}
