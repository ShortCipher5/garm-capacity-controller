package demandrepair

// This file exposes execution metadata for CI Health only. None of its results
// constitute queue authority, verified runner lineage or a capacity decision.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CIPerPage         = 50
	CIMaxPages        = 20 // GitHub caps filtered run search at 1,000 results.
	ciMaxSteps        = 256
	ciAttemptLifetime = 2 * time.Minute
	ciMaxLookback     = 31 * 24 * time.Hour
	ciMaxClockSkew    = time.Minute
)

// CIRepository is an explicit operator-enrolled subset of the existing
// authenticated read allowlist. GarmID is a locator, not proof of GitHub repo ID.
type CIRepository struct {
	Repository
	GitHubID int64
}

type CIHealthSource struct {
	client *GitHubClient
	byGarm map[string]CIRepository
	scope  []CIRepository
	id     string
}

func NewCIHealthSource(c *GitHubClient, repos []CIRepository) (*CIHealthSource, error) {
	if c == nil || len(repos) == 0 || len(repos) > len(c.scope) {
		return nil, ErrObservation
	}
	s := &CIHealthSource{client: c, byGarm: make(map[string]CIRepository, len(repos)), scope: append([]CIRepository(nil), repos...)}
	var seen = make(map[int64]bool, len(repos))
	for _, r := range s.scope {
		known, ok := c.scope[r.GarmID]
		_, repeated := s.byGarm[r.GarmID]
		if !ok || known != r.Repository || r.GitHubID <= 0 || repeated || seen[r.GitHubID] {
			return nil, ErrObservation
		}
		u, e := url.Parse("https://" + r.Host)
		if e != nil || u.User != nil || u.Hostname() != r.Host || u.Port() != "" || u.Path != "" || u.Fragment != "" || u.RawQuery != "" {
			return nil, ErrObservation
		}
		seen[r.GitHubID] = true
		s.byGarm[r.GarmID] = r
	}
	sort.Slice(s.scope, func(i, j int) bool { return s.scope[i].GitHubID < s.scope[j].GitHubID })
	digest := sha256.Sum256([]byte(c.base + "\x00" + s.scope[0].Host))
	s.id = hex.EncodeToString(digest[:])
	return s, nil
}

// Repositories returns an immutable-scope copy in numeric repo ID order.
func (s *CIHealthSource) Repositories() []CIRepository {
	if s == nil {
		return nil
	}
	return append([]CIRepository(nil), s.scope...)
}
func (s *CIHealthSource) SourceID() string {
	if s == nil {
		return ""
	}
	return s.id
}

type CIRunQuery struct {
	// Empty status requires a bounded run-creation window. Both ends are
	// inclusive at UTC second precision, as sent in the GitHub query.
	// Active statuses intentionally disallow a window so old, redispatched
	// attempts are visible.
	Status   string
	From, To time.Time
	Page     int
}

type CIRunPage struct {
	Runs       []CIRun
	ObservedAt time.Time
	// TotalCount is reported for this page's query, not a stable snapshot.
	TotalCount int
	// NextPage zero means the observed result ended, not complete historical
	// coverage. A capped or moving page may yield ErrObservation instead.
	NextPage int
}

type CIRun struct {
	WorkflowID, RunID, Attempt int64
	Name, Status, Conclusion   string
	StartedAt                  *time.Time // run_started_at from GitHub, not original created_at
	SourceURL                  string
}

// CIAttempt is source-minted and short-lived. A caller may copy or inspect
// an attempt but cannot change its request scope or mint a different attempt.
type CIAttempt struct {
	source     *CIHealthSource
	repository CIRepository
	run        CIRun
	at         time.Time
}

func (a CIAttempt) Run() CIRun {
	out := a.run
	if out.StartedAt != nil {
		t := *out.StartedAt
		out.StartedAt = &t
	}
	return out
}
func (a CIAttempt) RepositoryID() int64   { return a.repository.GitHubID }
func (a CIAttempt) ObservedAt() time.Time { return a.at }

type CIJobPage struct {
	Jobs       []CIJob
	ObservedAt time.Time
	NextPage   int
	// TotalCount is reported for this page's query, not a stable snapshot.
	TotalCount int
}

type CIJob struct {
	ID, RunID, RunAttempt    int64
	Name, Status, Conclusion string
	StartedAt, CompletedAt   *time.Time
	RunnerID                 int64
	RunnerName, SourceURL    string
	Steps                    []CIStep
}

type CIStep struct {
	Number                   int64
	Name, Status, Conclusion string
	StartedAt, CompletedAt   *time.Time
}

func ciRoot(r CIRepository) string { return "/repos/" + r.Owner + "/" + r.Name }
func ciRunPath(n int64) string     { return "/actions/runs/" + strconv.FormatInt(n, 10) }
func ciAttemptPath(runID, attempt int64) string {
	return ciRunPath(runID) + "/attempts/" + strconv.FormatInt(attempt, 10)
}
func ciSourceURL(r CIRepository, subpath string) string {
	return "https://" + r.Host + "/" + r.Owner + "/" + r.Name + subpath
}
func ciPage(page int) bool { return page > 0 && page <= CIMaxPages }
func ciPageQuery(page int) url.Values {
	return url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(CIPerPage)}}
}
func (s *CIHealthSource) enrolled(ctx context.Context, garmID string) (CIRepository, bool) {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return CIRepository{}, false
	}
	r, ok := s.byGarm[garmID]
	return r, ok
}

func (s *CIHealthSource) ListCIRuns(ctx context.Context, garmID string, q CIRunQuery) (CIRunPage, error) {
	r, ok := s.enrolled(ctx, garmID)
	now := time.Now().UTC()
	if !ok || !ciPage(q.Page) {
		return CIRunPage{}, ErrObservation
	}
	params := ciPageQuery(q.Page)
	var from, to time.Time
	switch q.Status {
	case "":
		if q.From.IsZero() || !q.From.Before(q.To) || q.To.After(now) || q.To.Sub(q.From) > ciMaxLookback {
			return CIRunPage{}, ErrObservation
		}
		// GitHub's range syntax is inclusive and uses ISO 8601 with
		// optional time at second precision. Verify the actual wire window.
		from, to = q.From.UTC().Truncate(time.Second), q.To.UTC().Truncate(time.Second)
		params.Set("created", from.Format(time.RFC3339)+".."+to.Format(time.RFC3339))
	case "queued", "in_progress", "requested", "waiting", "pending":
		if !q.From.IsZero() || !q.To.IsZero() {
			return CIRunPage{}, ErrObservation
		}
		params.Set("status", q.Status)
	default:
		return CIRunPage{}, ErrObservation
	}
	var wire ciRunPageWire
	if s.client.get(ctx, ciRoot(r)+"/actions/runs", params, &wire) != nil || wire.Runs == nil {
		return CIRunPage{}, ErrObservation
	}
	next, valid := ciNextPage(q.Page, wire.Total, len(wire.Runs))
	if !valid || *wire.Total >= CIPerPage*CIMaxPages {
		// Run search may advertise a capped total. A later worker can split
		// a historical window; an active cap remains explicitly incomplete.
		return CIRunPage{}, ErrObservation
	}
	if len(wire.Runs) == 0 {
		// An empty list carries no immutable repo identity. Confirm the
		// current locator still refers to the enrolled repository before
		// surfacing a successful empty result.
		var current ciRepoWire
		if s.client.get(ctx, ciRoot(r), url.Values{}, &current) != nil || !current.matches(r) {
			return CIRunPage{}, ErrObservation
		}
	}
	out := CIRunPage{Runs: make([]CIRun, 0, len(wire.Runs)), NextPage: next, ObservedAt: time.Now().UTC(), TotalCount: *wire.Total}
	seen := make(map[int64]bool, len(wire.Runs))
	for _, w := range wire.Runs {
		run, valid := w.validate(r, out.ObservedAt)
		if !valid || seen[run.RunID] || !w.matchesQuery(q.Status, from, to, out.ObservedAt) {
			return CIRunPage{}, ErrObservation
		}
		seen[run.RunID] = true
		out.Runs = append(out.Runs, run)
	}
	return out, nil
}

func (s *CIHealthSource) ReadCIAttempt(ctx context.Context, garmID string, runID, attempt int64) (CIAttempt, error) {
	r, ok := s.enrolled(ctx, garmID)
	if !ok || runID <= 0 || attempt <= 0 {
		return CIAttempt{}, ErrObservation
	}
	var wire ciRunWire
	if s.client.get(ctx, ciRoot(r)+ciAttemptPath(runID, attempt), url.Values{}, &wire) != nil {
		return CIAttempt{}, ErrObservation
	}
	now := time.Now().UTC()
	run, valid := wire.validate(r, now)
	if !valid || run.RunID != runID || run.Attempt != attempt {
		return CIAttempt{}, ErrObservation
	}
	return CIAttempt{source: s, repository: r, run: run, at: now}, nil
}

// ListCIAttemptJobs reads only the exact action-run attempt. Missing queue
// fields are not fabricated. Older supported wire shapes may omit run_attempt;
// request path plus positive run_id then supplies the attempt provenance.
func (s *CIHealthSource) ListCIAttemptJobs(ctx context.Context, a CIAttempt, page int) (CIJobPage, error) {
	r, ok := s.enrolled(ctx, a.repository.GarmID)
	if !ok || a.source != s || a.repository != r || !ciPage(page) || a.at.IsZero() || time.Since(a.at) < 0 || time.Since(a.at) >= ciAttemptLifetime {
		return CIJobPage{}, ErrObservation
	}
	var wire ciJobPageWire
	if s.client.get(ctx, ciRoot(r)+ciAttemptPath(a.run.RunID, a.run.Attempt)+"/jobs", ciPageQuery(page), &wire) != nil || wire.Jobs == nil || time.Since(a.at) >= ciAttemptLifetime {
		return CIJobPage{}, ErrObservation
	}
	next, valid := ciNextPage(page, wire.Total, len(wire.Jobs))
	if !valid {
		return CIJobPage{}, ErrObservation
	}
	out := CIJobPage{Jobs: make([]CIJob, 0, len(wire.Jobs)), NextPage: next, ObservedAt: time.Now().UTC(), TotalCount: *wire.Total}
	seen := make(map[int64]bool, len(wire.Jobs))
	for _, w := range wire.Jobs {
		job, valid := w.validate(r, a.run, out.ObservedAt)
		if !valid || seen[job.ID] {
			return CIJobPage{}, ErrObservation
		}
		seen[job.ID] = true
		out.Jobs = append(out.Jobs, job)
	}
	return out, nil
}

func ciNextPage(page int, total *int, actual int) (int, bool) {
	if total == nil || *total < 0 || *total > CIPerPage*CIMaxPages || actual > CIPerPage {
		return 0, false
	}
	first := (page - 1) * CIPerPage
	if *total == 0 && page == 1 && actual == 0 {
		return 0, true
	}
	if first >= *total || actual != min(CIPerPage, *total-first) {
		return 0, false
	}
	if first+actual < *total {
		return page + 1, true
	}
	return 0, true
}

type ciRepoWire struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

func (w ciRepoWire) matches(r CIRepository) bool {
	return w.ID == r.GitHubID && strings.EqualFold(w.FullName, r.Owner+"/"+r.Name)
}

type ciRunPageWire struct {
	Total *int        `json:"total_count"`
	Runs  []ciRunWire `json:"workflow_runs"`
}
type ciRunWire struct {
	ID         int64      `json:"id"`
	WorkflowID int64      `json:"workflow_id"`
	Attempt    int64      `json:"run_attempt"`
	Repo       ciRepoWire `json:"repository"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	CreatedAt  *time.Time `json:"created_at"`
	StartedAt  *time.Time `json:"run_started_at"`
}

func (w ciRunWire) matchesQuery(status string, from, to, now time.Time) bool {
	if status != "" {
		return w.Status == status
	}
	if w.CreatedAt == nil || !ciClocks(w.CreatedAt, nil, now) {
		return false
	}
	created := w.CreatedAt.UTC().Truncate(time.Second)
	return !created.Before(from) && !created.After(to)
}

func (w ciRunWire) validate(r CIRepository, now time.Time) (CIRun, bool) {
	if w.ID <= 0 || w.WorkflowID <= 0 || w.Attempt <= 0 || !w.Repo.matches(r) ||
		!ciValidState(w.Status, w.Conclusion) || !ciClocks(w.StartedAt, nil, now) {
		return CIRun{}, false
	}
	return CIRun{WorkflowID: w.WorkflowID, RunID: w.ID, Attempt: w.Attempt, Name: ciLabel(w.Name, false), Status: w.Status, Conclusion: w.Conclusion,
		StartedAt: ciUTC(w.StartedAt), SourceURL: ciSourceURL(r, ciAttemptPath(w.ID, w.Attempt))}, true
}

type ciJobPageWire struct {
	Total *int        `json:"total_count"`
	Jobs  []ciJobWire `json:"jobs"`
}
type ciJobWire struct {
	ID          int64        `json:"id"`
	RunID       int64        `json:"run_id"`
	RunAttempt  *int64       `json:"run_attempt"`
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Conclusion  string       `json:"conclusion"`
	StartedAt   *time.Time   `json:"started_at"`
	CompletedAt *time.Time   `json:"completed_at"`
	RunnerID    int64        `json:"runner_id"`
	RunnerName  string       `json:"runner_name"`
	Steps       []ciStepWire `json:"steps"`
}
type ciStepWire struct {
	Number      int64      `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (w ciJobWire) validate(r CIRepository, run CIRun, now time.Time) (CIJob, bool) {
	if w.ID <= 0 || w.RunID != run.RunID || w.RunAttempt != nil && *w.RunAttempt != run.Attempt || w.RunnerID < 0 ||
		len(w.Steps) > ciMaxSteps || !ciClocks(w.StartedAt, w.CompletedAt, now) || !ciValidState(w.Status, w.Conclusion) {
		return CIJob{}, false
	}
	out := CIJob{ID: w.ID, RunID: run.RunID, RunAttempt: run.Attempt, Name: ciLabel(w.Name, false), Status: w.Status, Conclusion: w.Conclusion,
		StartedAt: ciUTC(w.StartedAt), CompletedAt: ciUTC(w.CompletedAt), SourceURL: ciSourceURL(r, ciRunPath(run.RunID)+"/job/"+strconv.FormatInt(w.ID, 10)),
		Steps: make([]CIStep, 0, len(w.Steps))}
	if n := ciLabel(w.RunnerName, false); w.RunnerID > 0 && n != "" {
		out.RunnerID, out.RunnerName = w.RunnerID, n
	}
	seen := make(map[int64]bool, len(w.Steps))
	for _, step := range w.Steps {
		if step.Number <= 0 || seen[step.Number] || !ciValidState(step.Status, step.Conclusion) || !ciClocks(step.StartedAt, step.CompletedAt, now) {
			return CIJob{}, false
		}
		seen[step.Number] = true
		out.Steps = append(out.Steps, CIStep{Number: step.Number, Name: ciLabel(step.Name, true), Status: step.Status, Conclusion: step.Conclusion,
			StartedAt: ciUTC(step.StartedAt), CompletedAt: ciUTC(step.CompletedAt)})
	}
	sort.Slice(out.Steps, func(i, j int) bool { return out.Steps[i].Number < out.Steps[j].Number })
	return out, true
}

func ciLabel(s string, step bool) string {
	if len(s) > 256 || !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	if step && strings.HasPrefix(strings.ToLower(s), "run ") {
		// GitHub may synthesize the name by embedding the shell command.
		return "Run step (name withheld)"
	}
	return s
}
func ciValidState(status, conclusion string) bool {
	switch status {
	case "queued", "in_progress", "requested", "waiting", "pending":
		return conclusion == ""
	case "completed":
		switch conclusion {
		case "", "success", "failure", "cancelled", "neutral", "skipped", "timed_out", "action_required", "stale", "startup_failure":
			return true
		}
	case "skipped":
		return conclusion == "" || conclusion == "skipped"
	}
	return false
}
func ciClocks(started, completed *time.Time, now time.Time) bool {
	bound := now.Add(ciMaxClockSkew)
	return (started == nil || !started.IsZero() && !started.After(bound)) &&
		(completed == nil || !completed.IsZero() && !completed.After(bound)) &&
		(started == nil || completed == nil || !completed.Before(*started))
}
func ciUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// encoding/json defaults to last duplicate or case-aliased key winning; never
// allow ambiguity about an authoritative field. Unknown fields are not retained.
func ciObject(raw []byte, dst any) error {
	if !utf8.Valid(raw) {
		return ErrObservation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') {
		return ErrObservation
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		k, ok := key.(string)
		folded := ciFoldKey(k)
		if err != nil || !ok || seen[folded] || ciWireAlias(k) {
			return ErrObservation
		}
		seen[folded] = true
		var discard json.RawMessage
		if d.Decode(&discard) != nil {
			return ErrObservation
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return ErrObservation
	}
	return json.Unmarshal(raw, dst)
}

// json's field matching uses Unicode simple folding, not ToLower. Walking
// each fold cycle to its minimum rune gives duplicate detection the same
// equivalence class (including long s and the Kelvin sign).
func ciFoldKey(key string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			smallest = min(smallest, next)
		}
		return smallest
	}, key)
}

func ciWireAlias(actual string) bool {
	for _, canonical := range [...]string{
		"id", "full_name", "total_count", "workflow_runs", "workflow_id", "run_attempt", "repository",
		"name", "status", "conclusion", "created_at", "run_started_at", "jobs", "run_id", "started_at", "completed_at",
		"runner_id", "runner_name", "steps", "number",
	} {
		if actual != canonical && strings.EqualFold(actual, canonical) {
			return true
		}
	}
	return false
}
func (v *ciRepoWire) UnmarshalJSON(raw []byte) error {
	type plain ciRepoWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciRunPageWire) UnmarshalJSON(raw []byte) error {
	type plain ciRunPageWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciRunWire) UnmarshalJSON(raw []byte) error {
	type plain ciRunWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciJobPageWire) UnmarshalJSON(raw []byte) error {
	type plain ciJobPageWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciJobWire) UnmarshalJSON(raw []byte) error {
	type plain ciJobWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciStepWire) UnmarshalJSON(raw []byte) error {
	type plain ciStepWire
	return ciObject(raw, (*plain)(v))
}
