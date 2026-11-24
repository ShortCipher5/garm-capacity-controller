package cihealth

import "context"

// historyMetric identifies the exact timing field used by ProjectRetainedAttempt.
// The enclosing map already separates the full source/repo/workflow/run/attempt.
type historyMetric struct {
	job, step int64
	field     int
}

// compactHistoryMeasurements keeps the latest status row and the first
// positive witness of each timing field in newest-first order. Superseded
// samples do not tie storage use to polling frequency. Hold whole versions,
// including identities temporarily missing from the latest row, so a later
// reappearance can still recover the exact observed measurement. This is not
// an arbitrary point-in-time archive.
func compactHistoryMeasurements(ctx context.Context, sorted []AttemptFacts) ([]AttemptFacts, bool, error) {
	type group struct {
		records int
		fields  map[historyMetric]bool
	}
	groups := make(map[AttemptKey]*group)
	out := make([]AttemptFacts, 0, len(sorted))
	total, multiple := 0, false
	for _, row := range sorted {
		if !historyActive(ctx) {
			return nil, false, ErrHistory
		}
		groupState, seen := groups[row.Key]
		if !seen {
			groupState = &group{fields: make(map[historyMetric]bool)}
			groups[row.Key] = groupState
		}
		timing, err := Measure(row)
		if err != nil {
			return nil, false, ErrHistory
		}
		keep := !seen
		remember := func(job, step int64, spans ...Span) {
			for field, span := range spans {
				key := historyMetric{job, step, field}
				if span.Basis != Unavailable && !groupState.fields[key] {
					groupState.fields[key] = true
					keep = true
				}
			}
		}
		remember(0, 0, timing.Wall, timing.Active)
		for _, job := range timing.Jobs {
			remember(job.Key.JobID, 0, job.Queue, job.ActiveQueue, job.Execution, job.Active)
			for _, step := range job.Steps {
				remember(job.Key.JobID, step.Key.Number, step.Duration, step.Active)
			}
		}
		if keep {
			count := historyRecordCount(row)
			groupState.records += count
			total += count
			if groupState.records > historyMaxPageRecords || total > historyMaxRecords {
				return nil, false, ErrHistory
			}
			out = append(out, row)
			multiple = multiple || seen
		}
	}
	return out, multiple, nil
}
