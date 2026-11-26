package adminview

import (
	"encoding/json"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
)

type ciHistoryResponse struct {
	body      []byte
	withUsage []byte
	expires   time.Time
	source    cihealthsource.WorkflowTrendsSnapshot
}

type ciHistoryView struct {
	ExpiresAt   time.Time           `json:"expires_at"`
	Coverage    string              `json:"coverage"`
	TestResults string              `json:"test_results"`
	Workflows   []ciHistoryWorkflow `json:"workflows"`
}

type ciHistoryWorkflow struct {
	Source           string                         `json:"source"`
	Repository       string                         `json:"repository_id"`
	Workflow         string                         `json:"workflow_id"`
	Previous         ciHistoryWindow                `json:"previous"`
	Current          ciHistoryWindow                `json:"current"`
	SelectionLimited cihealth.HistorySelectionLimit `json:"selection_limited,omitempty"`
}

type ciHistoryWindow struct {
	From                     time.Time       `json:"from"`
	To                       time.Time       `json:"to"`
	Initial                  ciHistoryBucket `json:"initial"`
	Retries                  ciHistoryBucket `json:"retries"`
	RecoveredInitialFailures int             `json:"recovered_initial_failures"`
}

type ciHistoryBucket struct {
	Runs     int             `json:"distinct_runs"`
	Attempts ciHistoryMetric `json:"attempts"`
	Jobs     ciHistoryMetric `json:"jobs"`
	Steps    ciHistoryMetric `json:"steps"`
}

type ciHistoryMetric struct {
	Observed  int                `json:"observed"`
	Success   int                `json:"success"`
	Failure   int                `json:"failure"`
	Other     int                `json:"other"`
	Active    int                `json:"active"`
	Unknown   int                `json:"unknown"`
	Completed int                `json:"completed"`
	Duration  *ciHistoryDuration `json:"duration,omitempty"`
}

type ciHistoryDuration struct {
	Samples int    `json:"samples"`
	Mean    string `json:"mean_ns"`
	P95     string `json:"p95_ns"`
}

func ciHistoryWindowView(w cihealth.RetainedTrendWindow) ciHistoryWindow {
	return ciHistoryWindow{From: w.From, To: w.To, Initial: ciHistoryBucketView(w.Initial), Retries: ciHistoryBucketView(w.Retries),
		RecoveredInitialFailures: w.RecoveredInitialFailures}
}

func ciHistoryBucketView(b cihealth.RetainedTrendBucket) ciHistoryBucket {
	return ciHistoryBucket{Runs: b.Runs, Attempts: ciHistoryMetricView(b.Attempts), Jobs: ciHistoryMetricView(b.Jobs), Steps: ciHistoryMetricView(b.Steps)}
}

func ciHistoryMetricView(m cihealth.RetainedTrendMetric) ciHistoryMetric {
	out := ciHistoryMetric{Observed: m.Observed, Success: m.Success, Failure: m.Failure, Other: m.Other, Active: m.Active, Unknown: m.Unknown, Completed: m.Completed}
	if d := m.Duration; d != nil {
		out.Duration = &ciHistoryDuration{Samples: d.Samples, Mean: ciInt(int64(d.Mean)), P95: ciInt(int64(d.P95))}
	}
	return out
}

// PublishRetainedTrends enriches only the already-published, exact current
// timing cycle. The source supplied an opaque permission/retention lease.
// No historical data is attached to the ordinary timing-only path.
func (s *CIHealthSnapshot) PublishRetainedTrends(source cihealthsource.WorkflowTrendsSnapshot) error {
	if s == nil || s.clock == nil {
		return ErrConfiguration
	}
	at, expires, rows, ok := source.Summary()
	if !ok || len(rows) == 0 || len(rows) > maxCIAttempts || !expires.After(at) || expires.After(at.Add(ciDisplayLifetime)) {
		return errCIProjection
	}
	view := ciHistoryView{ExpiresAt: expires, Coverage: "retained_subset", TestResults: "unavailable"}
	seen := make(map[cihealth.TrendScope]bool, len(rows))
	for _, row := range rows {
		key := row.Scope
		allowed := false
		for _, scope := range s.scopes {
			if scope.source == key.Source && scope.repository == key.RepositoryID {
				allowed = true
				break
			}
		}
		if !allowed || key.WorkflowID <= 0 || seen[key] {
			return errCIProjection
		}
		switch row.Limited {
		case "", cihealth.HistorySelectionAttemptLimit, cihealth.HistorySelectionRecordLimit:
		default:
			return errCIProjection
		}
		seen[key] = true
		view.Workflows = append(view.Workflows, ciHistoryWorkflow{
			Source: key.Source, Repository: ciInt(key.RepositoryID), Workflow: ciInt(key.WorkflowID),
			Previous: ciHistoryWindowView(row.Trends.Previous), Current: ciHistoryWindowView(row.Trends.Current), SelectionLimited: row.Limited,
		})
	}
	extra, err := json.Marshal(view)
	if err != nil {
		return errCIProjection
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now, stable := s.wallLocked()
	const key = `,"history":`
	if !stable || !s.latest.Equal(at) || now.Before(at) || !now.Before(expires) ||
		len(s.body) == 0 || s.body[len(s.body)-1] != '}' || s.history.body != nil ||
		len(s.body)+len(key)+len(extra) > maxCIResponseBytes || !source.Current() {
		return errCIProjection
	}
	for scope := range seen {
		if !s.workflows[scope] {
			return errCIProjection
		}
	}
	// The timing body was marshaled by this publisher; do not modify that
	// immutable original, which remains readable when the shorter lease ends.
	combined := make([]byte, 0, len(s.body)+len(key)+len(extra))
	combined = append(combined, s.body[:len(s.body)-1]...)
	combined = append(combined, key...)
	combined = append(combined, extra...)
	combined = append(combined, '}')
	if !source.Current() {
		return errCIProjection
	}
	s.history = ciHistoryResponse{body: combined, expires: expires, source: source}
	if usage := s.usage.body; len(usage) > 0 && len(usage)+len(key)+len(extra) <= maxCIResponseBytes {
		// Preserve history if usage is independently withdrawn later.
		s.history.withUsage = append(append([]byte(nil), usage[:len(usage)-1]...), combined[len(s.body)-1:]...)
	}
	return nil
}
