package adminview

import (
	"errors"
	"strconv"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const (
	maxCIScopes   = 100
	maxCIAttempts = 25
	maxCIJobs     = 250
	maxCISteps    = 1000
)

var errCIProjection = errors.New("invalid or oversized CI health view")

// ciViewScope comes only from the server's already authorized startup scope.
// Neither the source label nor numeric repository ID attests authorization by itself.
type ciViewScope struct {
	source     string
	repository int64
}

type ciTimingView struct {
	Attempts []ciAttemptView `json:"attempts"`
}

type ciAttemptView struct {
	Source     string              `json:"source"`
	RepoID     string              `json:"repository_id"`
	Workflow   string              `json:"workflow_id"`
	Run        string              `json:"run_id"`
	Attempt    string              `json:"attempt"`
	AsOf       string              `json:"observed_at"`
	Status     string              `json:"status"`
	Conclusion cihealth.Conclusion `json:"conclusion,omitempty"`
	Wall       *ciSpanView         `json:"wall,omitempty"`
	Elapsed    *ciSpanView         `json:"elapsed,omitempty"`
	Jobs       []ciJobView         `json:"jobs"`
}

type ciJobView struct {
	ID          string              `json:"id"`
	Status      string              `json:"status"`
	Conclusion  cihealth.Conclusion `json:"conclusion,omitempty"`
	Queue       *ciSpanView         `json:"queue,omitempty"`
	ActiveQueue *ciSpanView         `json:"waiting,omitempty"`
	Execution   *ciSpanView         `json:"execution,omitempty"`
	Elapsed     *ciSpanView         `json:"elapsed,omitempty"`
	Steps       []ciStepView        `json:"steps"`
}

type ciStepView struct {
	Number     string              `json:"number"`
	Status     string              `json:"status"`
	Conclusion cihealth.Conclusion `json:"conclusion,omitempty"`
	Execution  *ciSpanView         `json:"execution,omitempty"`
	Elapsed    *ciSpanView         `json:"elapsed,omitempty"`
}

type ciSpanView struct {
	Basis       string `json:"basis"`
	Nanoseconds string `json:"nanoseconds"`
}

// projectCIHealthTiming creates only the read-only owner-view projection from a
// bounded selection of separately authorized facts. It performs no I/O, does
// not authorize or claim to cover the whole input source, and cannot write
// to GitHub, GARM, or the allocator. No routes call it by default.
func projectCIHealthTiming(scopes []ciViewScope, facts []cihealth.AttemptFacts) (ciTimingView, error) {
	fail := func() (ciTimingView, error) { return ciTimingView{}, errCIProjection }
	if len(scopes) > maxCIScopes || len(facts) > maxCIAttempts {
		return fail()
	}
	allowed := make(map[ciViewScope]bool, len(scopes))
	for _, scope := range scopes {
		if scope.repository <= 0 || len(scope.source) == 0 || len(scope.source) > 64 || allowed[scope] {
			return fail()
		}
		allowed[scope] = true
	}
	out := ciTimingView{Attempts: make([]ciAttemptView, 0, len(facts))}
	seen := make(map[cihealth.AttemptKey]bool, len(facts))
	jobCount, stepCount := 0, 0
	for _, a := range facts {
		k := a.Key
		if !allowed[ciViewScope{k.Source, k.RepositoryID}] || seen[k] || len(a.Jobs) > maxCIJobs-jobCount {
			return fail()
		}
		seen[k] = true
		jobCount += len(a.Jobs)
		for _, j := range a.Jobs {
			if len(j.Steps) > maxCISteps-stepCount {
				return fail()
			}
			stepCount += len(j.Steps)
		}
		timing, err := cihealth.Measure(a)
		if err != nil {
			return fail()
		}
		row := ciAttemptView{
			Source: k.Source, RepoID: ciInt(k.RepositoryID), Workflow: ciInt(k.WorkflowID), Run: ciInt(k.RunID), Attempt: ciInt(k.Attempt),
			AsOf: timing.AsOf.UTC().Format(time.RFC3339Nano), Status: ciPhase(timing.Phase), Conclusion: timing.Conclusion, Wall: ciSpan(timing.Wall), Elapsed: ciSpan(timing.Active),
			Jobs: make([]ciJobView, 0, len(timing.Jobs)),
		}
		for _, j := range timing.Jobs {
			job := ciJobView{
				ID: ciInt(j.Key.JobID), Status: ciPhase(j.Phase), Conclusion: j.Conclusion, Queue: ciSpan(j.Queue), ActiveQueue: ciSpan(j.ActiveQueue),
				Execution: ciSpan(j.Execution), Elapsed: ciSpan(j.Active), Steps: make([]ciStepView, 0, len(j.Steps)),
			}
			for _, s := range j.Steps {
				job.Steps = append(job.Steps, ciStepView{Number: ciInt(s.Key.Number), Status: ciPhase(s.Phase), Conclusion: s.Conclusion, Execution: ciSpan(s.Duration), Elapsed: ciSpan(s.Active)})
			}
			row.Jobs = append(row.Jobs, job)
		}
		out.Attempts = append(out.Attempts, row)
	}
	return out, nil
}

func ciInt(n int64) string { return strconv.FormatInt(n, 10) }

func ciPhase(p cihealth.Phase) string {
	switch p {
	case cihealth.Queued:
		return "queued"
	case cihealth.Running:
		return "running"
	case cihealth.Completed:
		return "completed"
	case cihealth.Skipped:
		return "skipped"
	default:
		return "unknown"
	}
}

func ciSpan(s cihealth.Span) *ciSpanView {
	switch s.Basis {
	case cihealth.ObservedEndpoints:
		return &ciSpanView{Basis: "observed_endpoints", Nanoseconds: ciInt(int64(s.Duration))}
	case cihealth.ObservedLowerBound:
		return &ciSpanView{Basis: "observed_lower_bound", Nanoseconds: ciInt(int64(s.Duration))}
	default:
		return nil
	}
}
