package demandrepair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
)

// JobLifecycle is positive GitHub job identity/state, not an acceptance fence,
// exhaustive runner history, physical completion or retirement permission.
type JobLifecycle struct {
	Repository               Repository      `json:"repository"`
	SourceIdentity           string          `json:"source_identity"`
	Runner                   stockjit.Record `json:"runner"`
	JobID, RunID, RunAttempt int64
	RunnerID                 int64
	RunnerName               string
	Status, Conclusion       string
	StartedAt                time.Time
	CompletedAt              *time.Time
	ObservedAt               time.Time
}
type JobLifecycleObservation struct {
	record   JobLifecycle
	verified bool
}

func (o JobLifecycleObservation) Valid() bool { return o.verified }
func (o JobLifecycleObservation) Record() JobLifecycle {
	if !o.verified {
		return JobLifecycle{}
	}
	r := o.record
	if r.CompletedAt != nil {
		v := *r.CompletedAt
		r.CompletedAt = &v
	}
	return r
}

type lifecycleResponse struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	RunAttempt  int64      `json:"run_attempt"`
	RunnerID    int64      `json:"runner_id"`
	RunnerName  string     `json:"runner_name"`
	URL         string     `json:"url"`
	RunURL      string     `json:"run_url"`
	Status      string     `json:"status"`
	Conclusion  *string    `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// Reject duplicate/case-aliased authority fields while permitting unrelated API
// additions. No parser ambiguity is admitted into the opaque observation.
func (v *lifecycleResponse) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return ErrObservation
	}
	fields := map[string]json.RawMessage{}
	known := map[string]bool{"id": true, "run_id": true, "run_attempt": true, "runner_id": true, "runner_name": true, "url": true, "run_url": true, "status": true, "conclusion": true, "started_at": true, "completed_at": true}
	for d.More() {
		tok, e = d.Token()
		k, ok := tok.(string)
		if e != nil || !ok {
			return ErrObservation
		}
		lower := strings.ToLower(k)
		if _, ok = fields[k]; ok || known[lower] && lower != k {
			return ErrObservation
		}
		var val json.RawMessage
		if d.Decode(&val) != nil {
			return ErrObservation
		}
		fields[k] = val
	}
	if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
		return ErrObservation
	}
	for k := range known {
		if _, ok := fields[k]; !ok {
			return ErrObservation
		}
	}
	type plain lifecycleResponse
	var p plain
	if json.Unmarshal(raw, &p) != nil {
		return ErrObservation
	}
	*v = lifecycleResponse(p)
	return nil
}

// ObserveJobLifecycle performs one scoped positive read by exact job ID. Missing
// jobs, missing runner identity and queued/unassigned jobs remain unavailable.
// The authentic stock JIT identity must have been obtained independently; a raw
// runner ID supplied by a caller cannot mint an observation.
func (c *GitHubClient) ObserveJobLifecycle(ctx context.Context, repo Repository, jobID int64, runner stockjit.Observation) (JobLifecycleObservation, error) {
	if !runner.Valid() {
		return JobLifecycleObservation{}, ErrObservation
	}
	identity := runner.Record()
	if identity.GitHubURL != "https://"+repo.Host+"/"+repo.Owner+"/"+repo.Name {
		return JobLifecycleObservation{}, ErrObservation
	}
	o, e := c.ObserveExactJob(ctx, repo, jobID)
	if e != nil || o.record.RunnerID != identity.RunnerID || o.record.RunnerName != identity.Name {
		return JobLifecycleObservation{}, ErrObservation
	}
	o.record.Runner = identity
	return o, nil
}

// ObserveExactJob reports the actual GitHub-assigned runner without claiming its
// relationship to native infrastructure. The ledger must join this positive
// observation to its independently authenticated immutable runner binding.
func (c *GitHubClient) ObserveExactJob(ctx context.Context, repo Repository, jobID int64) (JobLifecycleObservation, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || jobID <= 0 {
		return JobLifecycleObservation{}, ErrObservation
	}
	if configured, ok := c.scope[repo.GarmID]; !ok || configured != repo {
		return JobLifecycleObservation{}, ErrObservation
	}
	path := "/repos/" + repo.Owner + "/" + repo.Name + "/actions/jobs/" + strconv.FormatInt(jobID, 10)
	var result lifecycleResponse
	if c.get(ctx, path, url.Values{}, &result) != nil {
		return JobLifecycleObservation{}, ErrObservation
	}
	now := time.Now().UTC()
	if result.ID != jobID || result.RunID <= 0 || result.RunAttempt <= 0 || result.RunnerID <= 0 || result.RunnerName == "" || len(result.RunnerName) > 128 || result.URL != c.base+path || result.RunURL != c.base+"/repos/"+repo.Owner+"/"+repo.Name+"/actions/runs/"+strconv.FormatInt(result.RunID, 10) || result.StartedAt == nil || result.StartedAt.IsZero() || result.StartedAt.After(now) {
		return JobLifecycleObservation{}, ErrObservation
	}
	conclusion := ""
	switch result.Status {
	case "in_progress":
		if result.Conclusion != nil || result.CompletedAt != nil {
			return JobLifecycleObservation{}, ErrObservation
		}
	case "completed":
		if result.Conclusion == nil || result.CompletedAt == nil || result.CompletedAt.Before(*result.StartedAt) || result.CompletedAt.After(now) {
			return JobLifecycleObservation{}, ErrObservation
		}
		conclusion = *result.Conclusion
		switch conclusion {
		case "success", "failure", "neutral", "cancelled", "skipped", "timed_out", "action_required", "stale":
		default:
			return JobLifecycleObservation{}, ErrObservation
		}
	default:
		return JobLifecycleObservation{}, ErrObservation
	}
	sum := sha256.Sum256([]byte(c.base))
	return JobLifecycleObservation{verified: true, record: JobLifecycle{Repository: repo, SourceIdentity: hex.EncodeToString(sum[:]), RunnerID: result.RunnerID, RunnerName: result.RunnerName, JobID: jobID, RunID: result.RunID, RunAttempt: result.RunAttempt, Status: result.Status, Conclusion: conclusion, StartedAt: *result.StartedAt, CompletedAt: result.CompletedAt, ObservedAt: now}}, nil
}
