package cihealth

import "time"

type StockRunnerObservationState uint8

const (
	StockRunnerObservationUnknown StockRunnerObservationState = iota
	StockRunnerObservedRunning
	StockRunnerObservedStopped
)

// StockJobResourceBinding is supplied by an independently authenticated
// job-to-runner and Incus observation source. It cannot be constructed
// authoritatively from a stock provider's name/ProviderID, instance tags,
// repository labels, samples, or counter continuity. No repository runtime
// currently produces the required authoritative initial stock binding.
type StockJobResourceBinding struct {
	Job               JobKey
	RunnerIncarnation string // opaque source-provided SHA-256 identity, never the instance name
	RunnerAgentID     int64  // exact GitHub registration ID, not a label
	StockSourceID     string // exact authenticated GARM source identity
	StockInstanceUUID string
	StockPoolUUID     string
	Runtime           StockRunnerIncarnation
	ObservedAt        time.Time
	State             StockRunnerObservationState
	// TeardownAt must be positive stopped evidence for the exact runtime,
	// not the first 404, matching name, or an inferred completion.
	TeardownAt time.Time
}

type StockRunnerIncarnation struct {
	PeerFingerprint   string
	Member, Project   string
	InstanceName      string
	UUID, Generation  string
	BootID            string
	ProcessStartTicks uint64
	StartedAt         time.Time
}

type StockJobExecutionState uint8

const (
	StockJobExecutionUnknown StockJobExecutionState = iota
	StockJobObservedExecuting
	StockJobObservedEnded
)

// StockJobExecutionObservation is separate exact GitHub job-window evidence
// from an already authenticated job observer. It is never inferred from the
// Incus process lifetime, stock runner state, or counter continuity.
type StockJobExecutionObservation struct {
	Job                            JobKey
	StartedAt, EndedAt, ObservedAt time.Time
	State                          StockJobExecutionState
}

// SummarizeBoundStockJobResources is a pure consistency check before the
// existing sample reducer. It neither authenticates the supplied evidence
// nor turns it into a reusable proof, start permission or release authority.
// Initial positively brackets the start; closing positively brackets the
// last requested sample window. Separate execution evidence must establish
// that the exact job ran through that cutoff. Any replacement, refusal or
// post-job interval invalidates the result, even with rising counters.
func SummarizeBoundStockJobResources(scope JobResourceScope, initial, closing *StockJobResourceBinding, job *StockJobExecutionObservation, samples []JobResourceSample) (JobResourceSummary, error) {
	fail := func() (JobResourceSummary, error) { return JobResourceSummary{}, ErrJobResourceFacts }
	if initial == nil || closing == nil || len(samples) > MaxJobResourceSamples ||
		!validStockJobResourceBinding(initial) || !validStockJobResourceBinding(closing) {
		return fail()
	}
	a, b := *initial, *closing
	start, end, asOf := scope.StartedAt.UTC(), scope.EndAt.UTC(), scope.AsOf.UTC()
	if !stockJobExecutionCovers(scope.Job, start, end, asOf, job) {
		return fail()
	}
	a.Runtime.StartedAt, b.Runtime.StartedAt = a.Runtime.StartedAt.UTC(), b.Runtime.StartedAt.UTC()
	a.ObservedAt, b.ObservedAt = a.ObservedAt.UTC(), b.ObservedAt.UTC()
	if a.Job != scope.Job || b.Job != scope.Job || a.RunnerIncarnation != scope.RunnerIncarnation ||
		b.RunnerIncarnation != scope.RunnerIncarnation || a.RunnerAgentID != b.RunnerAgentID ||
		a.StockSourceID != b.StockSourceID || a.StockInstanceUUID != b.StockInstanceUUID || a.StockPoolUUID != b.StockPoolUUID ||
		a.Runtime != b.Runtime ||
		a.State != StockRunnerObservedRunning || a.ObservedAt.After(start) || start.Sub(a.ObservedAt) > MaxJobResourceGap ||
		b.ObservedAt.Before(end) || b.ObservedAt.After(asOf) || b.ObservedAt.Sub(end) > MaxJobResourceGap ||
		!b.ObservedAt.After(a.ObservedAt) {
		return fail()
	}
	if b.State == StockRunnerObservedStopped {
		stop := b.TeardownAt.UTC()
		if !stop.After(start) || end.After(stop) {
			return fail()
		}
		// An active workload observation must occur while this exact
		// incarnation is alive. An independently completed job can be read
		// back after teardown, but its actual end cannot follow teardown.
		if job.State == StockJobObservedExecuting && !job.ObservedAt.UTC().Before(stop) ||
			job.State == StockJobObservedEnded && job.EndedAt.UTC().After(stop) {
			return fail()
		}
		for _, s := range samples {
			if !s.At.UTC().Before(stop) {
				return fail()
			}
		}
	}
	return SummarizeJobResources(scope, samples)
}

func stockJobExecutionCovers(key JobKey, start, end, asOf time.Time, job *StockJobExecutionObservation) bool {
	if job == nil {
		return false
	}
	started, observed, ended := job.StartedAt.UTC(), job.ObservedAt.UTC(), job.EndedAt.UTC()
	if job.Job != key || !validTime(started) || !started.Equal(start) || !validTime(observed) ||
		observed.Before(end) || observed.After(asOf) || observed.Sub(end) > MaxJobResourceGap {
		return false
	}
	switch job.State {
	case StockJobObservedExecuting:
		return ended.IsZero()
	case StockJobObservedEnded:
		return validTime(ended) && ended.After(started) && !ended.Before(end) && !ended.After(observed)
	default:
		return false
	}
}

func validStockJobResourceBinding(b *StockJobResourceBinding) bool {
	r := b.Runtime
	if !validKey(b.Job.Attempt) || b.Job.JobID <= 0 || b.RunnerAgentID <= 0 ||
		!stockJobDigest(b.RunnerIncarnation) || !stockJobDigest(b.StockSourceID) ||
		!stockJobUUID(b.StockInstanceUUID) || !stockJobUUID(b.StockPoolUUID) ||
		!stockJobDigest(r.PeerFingerprint) || !stockJobLocator(r.Member) || !stockJobLocator(r.Project) || !stockJobLocator(r.InstanceName) ||
		!stockJobUUID(r.UUID) || !stockJobUUID(r.Generation) || !stockJobUUID(r.BootID) || r.ProcessStartTicks == 0 ||
		!validTime(r.StartedAt.UTC()) || !validTime(b.ObservedAt.UTC()) ||
		r.StartedAt.UTC().After(b.ObservedAt.UTC()) {
		return false
	}
	switch b.State {
	case StockRunnerObservedRunning:
		return b.TeardownAt.IsZero()
	case StockRunnerObservedStopped:
		stop := b.TeardownAt.UTC()
		return validTime(stop) && stop.After(r.StartedAt.UTC()) && !stop.After(b.ObservedAt.UTC())
	default:
		return false
	}
}

func stockJobLocator(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_' || c == '.') {
			continue
		}
		return false
	}
	return true
}

func stockJobDigest(s string) bool { return len(s) == 64 && stockJobLowerHex(s) }
func stockJobUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	return stockJobLowerHex(s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:])
}

func stockJobLowerHex(s string) bool {
	nonzero := false
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
		nonzero = nonzero || c != '0'
	}
	return nonzero
}
