package cihealthsource

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

const (
	maxCycleRepositories = 4
	maxDiscoveryPages    = 2 // per repository and query
	maxCycleAttempts     = 25
	maxCycleJobs         = 250
	maxCycleSteps        = 1000
	maxCycleHTTPReads    = 160
	maxRecentWindow      = 31 * 24 * time.Hour
	cycleDeadline        = 45 * time.Second
)

var ErrCycleObservation = errors.New("CI collection unavailable")

type CycleState uint8

const (
	CycleUnavailable CycleState = iota
	CycleObserved
	CyclePartial
)

// Cycle is a bounded, non-atomic collection of positive observations. Observed
// means configured scans ended without a detected failure or local truncation;
// it does not mean complete history. Unavailable results carry no facts: a
// publisher must withdraw old observations on failure, not append later rows.
type Cycle struct {
	state        CycleState
	observedAt   time.Time
	sealedAt     time.Time // local monotonic clock for proof expiry, never history time
	facts        []cihealth.AttemptFacts
	source       cycleSourceInstance
	runLinks     map[cihealth.AttemptKey]demandrepair.WorkflowRunLink
	continuation *activeContinuationCell
}

func (c Cycle) State() CycleState     { return c.state }
func (c Cycle) ObservedAt() time.Time { return c.observedAt }
func (c Cycle) Facts() []cihealth.AttemptFacts {
	if c.state == CycleUnavailable {
		return nil
	}
	out := make([]cihealth.AttemptFacts, 0, len(c.facts))
	for _, fact := range c.facts {
		out = append(out, cloneFacts(fact))
	}
	return out
}

type runCandidate struct {
	runID, attempt, workflow int64
}

type attemptLocator struct{ runID, attempt int64 }

type repoScan struct {
	repo           demandrepair.CIRepository
	active, recent []runCandidate
}

// Source comprises the existing scoped read capabilities, not the write
// service or a token provider.
type Source interface {
	AttemptSource
	Repositories() []demandrepair.CIRepository
	ListCIRuns(context.Context, string, demandrepair.CIRunQuery) (demandrepair.CIRunPage, error)
}

type cycleReader struct {
	source  Source
	budget  readBudget
	latest  time.Time
	partial bool
}

// ReadCycle executes one pass for an explicitly selected subset of the source's
// already enrolled repository locators. All active categories are scanned
// independently of age; the recent window catches terminal runs. No retries,
// automatic scheduling, persistence, or snapshot publication happen here.
func ReadCycle(ctx context.Context, source Source, enrolledGarmRepos []string, from, to time.Time) (Cycle, error) {
	return readCycle(ctx, source, enrolledGarmRepos, from, to, nil)
}

func readCycle(ctx context.Context, source Source, enrolledGarmRepos []string, from, to time.Time, hint *ActiveContinuation) (Cycle, error) {
	if ctx == nil || ctx.Err() != nil || source == nil || len(enrolledGarmRepos) == 0 || len(enrolledGarmRepos) > maxCycleRepositories ||
		from.IsZero() || !from.Before(to) || to.After(time.Now()) || to.Sub(from) > maxRecentWindow ||
		!from.UTC().Truncate(time.Second).Before(to.UTC().Truncate(time.Second)) {
		return Cycle{}, ErrCycleObservation
	}
	repos, ok := selectRepos(source, enrolledGarmRepos)
	if !ok {
		return Cycle{}, ErrCycleObservation
	}
	continuation := hint.consume(source, repos)
	ctx, cancel := context.WithTimeout(ctx, cycleDeadline)
	defer cancel()
	r := cycleReader{source: source, budget: readBudget{maxCycleHTTPReads}}
	scans := make([]repoScan, 0, len(repos))
	for _, repo := range repos {
		scan, err := r.discover(ctx, repo, from, to)
		if err != nil {
			return Cycle{}, ErrCycleObservation
		}
		scans = append(scans, scan)
	}

	// Prefer active observations before recent history, interleaving the
	// sorted repository scopes so the first repo cannot fill the view alone.
	out := Cycle{state: CycleObserved, source: captureCycleSource(source)}
	type usage struct {
		jobs, steps, attempted int
		budget                 readBudget
	}
	perRepo := make([]usage, len(scans))
	for i := range perRepo {
		perRepo[i].budget.remaining = r.budget.remaining / len(scans)
	}
	readCandidate := func(scan repoScan, candidate runCandidate, used *usage) error {
		used.attempted++
		got, err := readAttempt(ctx, source, out.source, scan.repo.GarmID, candidate.runID, candidate.attempt, &used.budget,
			maxCycleJobs/len(scans)-used.jobs, maxCycleSteps/len(scans)-used.steps)
		if errors.Is(err, errReadLimit) {
			// Never publish a partial attempt. Retain only independently
			// read complete attempts and continue within the cycle budget.
			r.partial = true
			return nil
		}
		key := got.facts.Key
		if err != nil || key.Source != source.SourceID() || key.RepositoryID != scan.repo.GitHubID ||
			key.RunID != candidate.runID || key.Attempt != candidate.attempt || key.WorkflowID != candidate.workflow ||
			got.facts.AsOf.Before(r.latest) {
			return ErrCycleObservation
		}
		r.latest = got.facts.AsOf
		out.facts = append(out.facts, got.facts)
		if got.runLink.Available() {
			if out.runLinks == nil {
				out.runLinks = make(map[cihealth.AttemptKey]demandrepair.WorkflowRunLink)
			}
			out.runLinks[key] = got.runLink
		}
		used.jobs += len(got.facts.Jobs)
		for _, j := range got.facts.Jobs {
			used.steps += len(j.Steps)
		}
		return nil
	}
	for _, active := range []bool{true, false} {
		for i := 0; ; i++ {
			found := false
			for index, scan := range scans {
				candidates := scan.recent
				if active {
					candidates = scan.active
				}
				if i >= len(candidates) {
					continue
				}
				found = true
				used := &perRepo[index]
				if used.attempted == maxCycleAttempts/len(scans) || used.budget.remaining < 2 {
					r.partial = true
					continue
				}
				if err := readCandidate(scan, candidates[i], used); err != nil {
					return Cycle{}, err
				}
			}
			if !found {
				break
			}
		}
	}
	normalFacts := len(out.facts)
	// Never displace a normal candidate or spend another repository's budget.
	// A newly found rerun is independent and never completes a prior attempt.
	if !r.partial && continuation.current(source) {
		for i, scan := range scans {
			used := &perRepo[i]
			if r.partial || !continuation.current(source) {
				break
			}
			candidate, ok, err := continuation.candidate(scan)
			if err != nil {
				return Cycle{}, err
			}
			if !ok || used.attempted >= maxCycleAttempts/len(scans) || used.budget.remaining < 2 ||
				used.jobs >= maxCycleJobs/len(scans) || used.steps >= maxCycleSteps/len(scans) {
				continue
			}
			if err := readCandidate(scan, candidate, used); err != nil {
				return Cycle{}, err
			}
		}
	}
	if ctx.Err() != nil || r.latest.IsZero() {
		return Cycle{}, ErrCycleObservation
	}
	out.observedAt = r.latest
	out.sealedAt = time.Now()
	if r.partial {
		out.state = CyclePartial
	}
	out.continuation = mintActiveContinuation(out, repos, normalFacts)
	return out, nil
}

func selectRepos(source Source, requested []string) ([]demandrepair.CIRepository, bool) {
	want := make(map[string]bool, len(requested))
	for _, id := range requested {
		if id == "" || want[id] {
			return nil, false
		}
		want[id] = true
	}
	out := make([]demandrepair.CIRepository, 0, len(want))
	for _, repo := range source.Repositories() {
		if want[repo.GarmID] {
			out = append(out, repo)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GitHubID < out[j].GitHubID })
	return out, len(out) == len(want)
}

func (r *cycleReader) discover(ctx context.Context, repo demandrepair.CIRepository, from, to time.Time) (repoScan, error) {
	out := repoScan{repo: repo}
	seenAttempts := make(map[attemptLocator]bool)
	seenRunning := make(map[attemptLocator]bool)
	var running, otherActive []runCandidate
	workflows := make(map[int64]int64)
discovery:
	for _, status := range []string{"queued", "in_progress", "requested", "waiting", "pending", ""} {
		q := demandrepair.CIRunQuery{Status: status}
		if status == "" {
			q.From, q.To = from, to
		}
		seenQuery := make(map[int64]bool)
		reportedTotal := -1
		for page := 1; page <= maxDiscoveryPages; page++ {
			// ListCIRuns can perform one additional repo-pin confirmation
			// after an empty page. Charge the worst case before touching it.
			if !r.budget.take(2) {
				r.partial = true
				break discovery
			}
			q.Page = page
			q.PreviousTotal = max(0, reportedTotal)
			rows, err := r.source.ListCIRuns(ctx, repo.GarmID, q)
			if err != nil || rows.ObservedAt.IsZero() || rows.ObservedAt.Before(r.latest) || !validDiscoveryPage(page, reportedTotal, rows) {
				return repoScan{}, ErrCycleObservation
			}
			for _, row := range rows.Runs {
				known, seenRun := workflows[row.RunID]
				if row.RunID <= 0 || row.Attempt <= 0 || row.WorkflowID <= 0 || seenQuery[row.RunID] || (seenRun && known != row.WorkflowID) {
					return repoScan{}, ErrCycleObservation
				}
				seenQuery[row.RunID] = true
			}
			r.latest = rows.ObservedAt
			if reportedTotal >= 0 && rows.TotalCount != reportedTotal {
				// The source and identities were valid, but the page no
				// longer belongs to a stable reported result size. Keep
				// only the earlier candidate prefix, read its exact details,
				// and continue independent queries. Never persist this cycle.
				r.partial = true
				break
			}
			reportedTotal = rows.TotalCount // Equal totals cannot prove atomic pagination.
			for _, row := range rows.Runs {
				workflows[row.RunID] = row.WorkflowID
				id := attemptLocator{row.RunID, row.Attempt}
				candidate := runCandidate{row.RunID, row.Attempt, row.WorkflowID}
				if status == "in_progress" {
					// A later positive running observation promotes the exact
					// attempt even when it was already listed as queued.
					if !seenRunning[id] {
						running = append(running, candidate)
						seenRunning[id] = true
					}
				} else if !seenAttempts[id] {
					if status == "" {
						out.recent = append(out.recent, candidate)
					} else {
						otherActive = append(otherActive, candidate)
					}
				}
				seenAttempts[id] = true
			}
			if rows.NextPage == 0 {
				break
			}
			if page == maxDiscoveryPages {
				r.partial = true
			}
		}
	}
	// Interleave both active groups, giving the first available detail slot
	// to running work without letting a full running group hide the queue.
	// Repository interleaving and all attempt/read limits still apply.
	nonRunning := otherActive[:0]
	for _, candidate := range otherActive {
		if !seenRunning[attemptLocator{candidate.runID, candidate.attempt}] {
			nonRunning = append(nonRunning, candidate)
		}
	}
	for i := 0; i < max(len(running), len(nonRunning)); i++ {
		if i < len(running) {
			out.active = append(out.active, running[i])
		}
		if i < len(nonRunning) {
			out.active = append(out.active, nonRunning[i])
		}
	}
	return out, nil
}

// Check the bounded typed page envelope before interpreting changed totals as
// ordinary discovery movement. The original source also verifies the
// repository identity and query membership of every row.
func validDiscoveryPage(page, previousTotal int, rows demandrepair.CIRunPage) bool {
	total, count := rows.TotalCount, len(rows.Runs)
	first := (page - 1) * demandrepair.CIPerPage
	if page > 1 && count == 0 && total >= 0 && total <= first {
		// A preceding page proved there was a next page under the old
		// total; an exhausted tail can only transition to partial.
		return previousTotal > first && previousTotal < demandrepair.CIPerPage*demandrepair.CIMaxPages && rows.NextPage == 0
	}
	if total == 0 {
		return page == 1 && count == 0 && rows.NextPage == 0
	}
	if total <= first || total >= demandrepair.CIPerPage*demandrepair.CIMaxPages ||
		count != min(demandrepair.CIPerPage, total-first) {
		return false
	}
	next := 0
	if first+count < total {
		next = page + 1
	}
	return rows.NextPage == next
}
