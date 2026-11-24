package cihealth

import (
	"strconv"
	"time"
)

// VerifiedUsageState classifies one exact attempt/job's resource usage
// evidence. Unavailable and Stale records never carry measured numbers;
// Partial records carry only what was actually measured with its coverage;
// Verified records fully cover the requested window for the measured metrics.
type VerifiedUsageState string

const (
	VerifiedUsageUnavailable VerifiedUsageState = "unavailable"
	VerifiedUsageStale       VerifiedUsageState = "stale"
	VerifiedUsagePartial     VerifiedUsageState = "partial"
	VerifiedUsageVerified    VerifiedUsageState = "verified"
)

// UsageOmissionReason is the machine-readable reason measured usage is absent
// or incomplete. It is an omission reason, never a zero measurement.
type UsageOmissionReason string

const (
	UsageOmissionNone                  UsageOmissionReason = ""
	UsageOmissionJoinUnsupported       UsageOmissionReason = "provider_origin_join_unsupported"
	UsageOmissionEvidenceMismatch      UsageOmissionReason = "attribution_evidence_mismatch"
	UsageOmissionStaleEvidence         UsageOmissionReason = "stale_evidence"
	UsageOmissionUnbracketed           UsageOmissionReason = "unbracketed_samples"
	UsageOmissionCounterDiscontinuity  UsageOmissionReason = "counter_discontinuity"
	UsageOmissionJobTooShort           UsageOmissionReason = "job_too_short_uncaptured"
	UsageOmissionOverlap               UsageOmissionReason = "overlapping_jobs"
	UsageOmissionDiskUnmeasured        UsageOmissionReason = "disk_not_independently_measured"
	UsageOmissionDiskUnsupported       UsageOmissionReason = "disk_unsupported_by_sources"
	UsageOmissionCollectionUnavailable UsageOmissionReason = "collection_unavailable"
)

// VerifiedJobUsageRecord is the exported per-attempt/job verified-usage
// contract. The persistence plan consumes these records durably; the CI
// Health UI renders their wire projection; sizing cohorts consume only
// Verified or Partial records. Measured is non-nil only for Verified and
// Partial states, and it preserves the existing reducer's coverage, sample
// counts and omission behavior exactly. Host free disk is not job usage:
// disk metrics appear only when independently bound samples supplied them.
// AsOf is the source observation time these facts rest on (the requested
// window's AsOf, or the latest claim observation for rejected records),
// never the render or evaluation time.
type VerifiedJobUsageRecord struct {
	Job      JobKey
	State    VerifiedUsageState
	Reason   UsageOmissionReason
	AsOf     time.Time
	Measured *JobResourceSummary
}

// VerifiedUsageSampleSet is the bounded sampling evidence offered for one
// exact job window: the caller's requested scope, the initial binding taken
// before job start, the closing binding at the cutoff or independently
// verified stop, separate execution evidence, and the in-window samples.
type VerifiedUsageSampleSet struct {
	Scope     JobResourceScope
	Initial   *StockJobResourceBinding
	Closing   *StockJobResourceBinding
	Execution *StockJobExecutionObservation
	Samples   []JobResourceSample
}

// BuildVerifiedJobUsage validates the assembled attribution claim and the
// bounded sample set, then reduces the samples with the existing bound-job
// gate. Measured usage is returned only when the capability matrix documents
// a source that proves the provider-origin join AND the claim carries
// positively verified origin evidence under the exact enrolled repository
// scope AND both sampling brackets validate against that same verified GARM
// row. The documented stock-source matrix proves none of these, so it stays
// unavailable even when every supplied field happens to match. Independently
// proven assignment and runtime with positive coverage produce the reducer's
// exact numbers under Verified or Partial; every unsupported, mismatched,
// stale, unbracketed or discontinuous input produces an explicit Unavailable
// or Stale record with a reason and no numbers.
func BuildVerifiedJobUsage(matrix AttributionCapabilityMatrix, claim JobAttributionClaim, set VerifiedUsageSampleSet, now time.Time) VerifiedJobUsageRecord {
	record := VerifiedJobUsageRecord{Job: claim.Job, State: VerifiedUsageUnavailable, AsOf: latestClaimObservation(claim, now)}
	if now.IsZero() {
		record.Reason = UsageOmissionEvidenceMismatch
		return record
	}
	if !matrix.Supports(FactProviderOriginJoin) {
		// Capability, not field coherence, decides: the documented matrix has
		// no source that proves the provider-origin join, so no measured
		// usage may be returned no matter how well the evidence matches.
		record.Reason = UsageOmissionJoinUnsupported
		return record
	}
	attribution, err := VerifyJobAttribution(matrix, claim, now)
	if err != nil {
		record.Reason = classifyAttributionFailure(claim, now)
		return record
	}
	if verdict := classifySampleSet(attribution, set, now); verdict.reason != UsageOmissionNone {
		record.State, record.Reason = verdict.state, verdict.reason
		return record
	}
	// From here the record's freshness rests on the source observation time
	// the caller bound to the requested window, never on evaluation time.
	record.AsOf = set.Scope.AsOf.UTC()
	summary, err := SummarizeBoundStockJobResources(set.Scope, set.Initial, set.Closing, set.Execution, set.Samples)
	if err != nil {
		record.Reason = UsageOmissionUnbracketed
		return record
	}
	if summary.Samples == 0 || (summary.CPU == nil && summary.RAM == nil && summary.RAMMean == nil &&
		summary.DiskRead == nil && summary.DiskWrite == nil && summary.DiskUsed == nil) {
		// Attribution is proven, but no counter covered the job window. A
		// short uncaptured job stays unavailable; it is not shown as zero.
		record.Reason = UsageOmissionJobTooShort
		return record
	}
	record.Measured = &summary
	record.State = VerifiedUsagePartial
	if summary.CPU != nil && summary.CPU.Coverage.Complete && summary.RAMMean != nil && summary.RAMMean.Coverage.Complete {
		record.State = VerifiedUsageVerified
	}
	return record
}

// latestClaimObservation picks the freshest source observation time the
// rejected claim actually carries, so an unavailable record still reports
// when its evidence was seen. Observations after now are dropped; a claim
// with no valid observation falls back to the evaluation time.
func latestClaimObservation(claim JobAttributionClaim, now time.Time) time.Time {
	latest := time.Time{}
	for _, at := range [...]time.Time{claim.Row.ObservedAt, claim.Execution.ObservedAt, claim.Origin.AllocationObservedAt} {
		if validTime(at.UTC()) && !at.UTC().After(now.UTC()) && at.UTC().After(latest) {
			latest = at.UTC()
		}
	}
	if latest.IsZero() {
		return now.UTC()
	}
	return latest
}

type omission struct {
	state  VerifiedUsageState
	reason UsageOmissionReason
}

func classifyAttributionFailure(claim JobAttributionClaim, now time.Time) UsageOmissionReason {
	if claim.Origin == (VerifiedProviderOrigin{}) {
		// No documented source can supply the provider-origin join; a name,
		// mutable provider label, timing, or separate TLS on both reads
		// cannot replace the missing origin.
		return UsageOmissionJoinUnsupported
	}
	// Distinguish expired observations from contradiction: well-formed facts
	// observed outside the bounded lifetime report stale, not mismatched.
	if wellFormedStockRowEvidence(claim.Row) && claim.Execution.State != StockJobExecutionUnknown &&
		validTime(claim.Execution.StartedAt.UTC()) &&
		(!validTime(claim.Execution.ObservedAt.UTC()) || claim.Execution.ObservedAt.UTC().After(now.UTC()) ||
			claim.Row.ObservedAt.UTC().After(now.UTC()) ||
			claim.Row.ObservedAt.UTC().Before(claim.Execution.StartedAt.UTC())) {
		return UsageOmissionStaleEvidence
	}
	return UsageOmissionEvidenceMismatch
}

// classifySampleSet checks the bounded sampling evidence against the verified
// attribution before the reducer runs, so unsafe attributions are labelled
// instead of silently reduced.
func classifySampleSet(a VerifiedAttribution, set VerifiedUsageSampleSet, now time.Time) omission {
	scope := set.Scope
	if scope.Job != a.Job || !validKey(scope.Job.Attempt) || scope.Job.JobID <= 0 ||
		!validTime(scope.StartedAt.UTC()) || !validTime(scope.EndAt.UTC()) || !validTime(scope.AsOf.UTC()) ||
		!scope.EndAt.UTC().After(scope.StartedAt.UTC()) || scope.EndAt.UTC().After(scope.AsOf.UTC()) ||
		scope.AsOf.UTC().After(now.UTC()) || !scope.StartedAt.UTC().Equal(a.StartedAt) ||
		scope.EndAt.UTC().Sub(scope.StartedAt.UTC()) > MaxJobResourceWindow || scope.MaxGap <= 0 ||
		scope.MaxGap > MaxJobResourceGap || len(set.Samples) > MaxJobResourceSamples {
		return omission{VerifiedUsageUnavailable, UsageOmissionUnbracketed}
	}
	initial, closing := set.Initial, set.Closing
	if initial == nil || closing == nil || set.Execution == nil {
		return omission{VerifiedUsageUnavailable, UsageOmissionUnbracketed}
	}
	if !validStockJobResourceBinding(initial) || !validStockJobResourceBinding(closing) {
		return omission{VerifiedUsageUnavailable, UsageOmissionUnbracketed}
	}
	// Both sampling brackets must validate against the same verified GARM
	// row: a reused instance, another runner, another pool/attempt, or a
	// bracket naming a different row is never stitched on.
	for _, binding := range [...]StockJobResourceBinding{*initial, *closing} {
		if binding.Job != a.Job || binding.RunnerAgentID != a.AgentID || binding.StockSourceID != a.SourceID ||
			binding.StockPoolUUID != a.PoolID || binding.StockInstanceUUID != a.RowID ||
			binding.Runtime != a.Incarnation ||
			(scope.RunnerIncarnation != "" && binding.RunnerIncarnation != scope.RunnerIncarnation) {
			return omission{VerifiedUsageUnavailable, UsageOmissionEvidenceMismatch}
		}
	}
	if initial.RunnerIncarnation != closing.RunnerIncarnation {
		return omission{VerifiedUsageUnavailable, UsageOmissionEvidenceMismatch}
	}
	if scope.RunnerIncarnation == "" {
		scope.RunnerIncarnation = initial.RunnerIncarnation
		set.Scope = scope
	}
	if reason := ValidateCollectionWindow(set, now); reason != UsageOmissionNone {
		return omission{VerifiedUsageUnavailable, reason}
	}
	return omission{VerifiedUsageVerified, UsageOmissionNone}
}

// ValidateCollectionWindow enforces the bounded read-only collector rules on
// one exact job window: the initial binding must positively bracket the job
// start; the closing binding must bracket the cutoff or an independently
// verified stop; samples must stay inside the window; cumulative counters
// must never decrease (no stitching across runner reuse, reboot or counter
// resets); and a response arrival timestamp alone never proves the job was
// still executing, so separate execution evidence must cover the cutoff.
func ValidateCollectionWindow(set VerifiedUsageSampleSet, now time.Time) UsageOmissionReason {
	scope, initial, closing, execution := set.Scope, set.Initial, set.Closing, set.Execution
	if initial == nil || closing == nil || execution == nil || !validStockJobResourceBinding(initial) ||
		!validStockJobResourceBinding(closing) {
		return UsageOmissionUnbracketed
	}
	start, end := scope.StartedAt.UTC(), scope.EndAt.UTC()
	// Bind before job start, within the sampling gap.
	if initial.State != StockRunnerObservedRunning || initial.ObservedAt.UTC().After(start) ||
		start.Sub(initial.ObservedAt.UTC()) > scope.MaxGap {
		return UsageOmissionUnbracketed
	}
	// Close at the cutoff or an independently verified stop, within the gap.
	if closing.ObservedAt.UTC().Before(end) || closing.ObservedAt.UTC().Sub(end) > scope.MaxGap ||
		closing.ObservedAt.UTC().After(scope.AsOf.UTC()) {
		return UsageOmissionUnbracketed
	}
	// Never span job boundaries or stitch across replacement or reboot: the
	// exact runtime identity must be identical at both brackets.
	if initial.Runtime != closing.Runtime || initial.StockInstanceUUID != closing.StockInstanceUUID ||
		initial.StockSourceID != closing.StockSourceID || initial.StockPoolUUID != closing.StockPoolUUID ||
		initial.RunnerAgentID != closing.RunnerAgentID {
		return UsageOmissionEvidenceMismatch
	}
	// Positive historical completion does not prove counter coverage, and an
	// arrival timestamp alone does not prove the job was still executing.
	if !stockJobExecutionCovers(scope.Job, initial.RunnerAgentID, start, end, scope.AsOf.UTC(), execution) {
		return UsageOmissionUnbracketed
	}
	for i, sample := range set.Samples {
		at := sample.At.UTC()
		if sample.Job != scope.Job || sample.RunnerIncarnation != scope.RunnerIncarnation ||
			!validTime(at) || at.Before(start) || at.After(end) {
			return UsageOmissionUnbracketed
		}
		if closing.State == StockRunnerObservedStopped && !at.Before(closing.TeardownAt.UTC()) {
			return UsageOmissionUnbracketed
		}
		if i > 0 && countersDecreased(set.Samples[i-1], sample) {
			return UsageOmissionCounterDiscontinuity
		}
	}
	return UsageOmissionNone
}

func countersDecreased(previous, latest JobResourceSample) bool {
	for _, pair := range [...][2]ResourceValue{
		{previous.CPUTimeNanos, latest.CPUTimeNanos},
		{previous.DiskReadBytes, latest.DiskReadBytes},
		{previous.DiskWriteBytes, latest.DiskWriteBytes},
	} {
		if pair[0].Known && pair[1].Known && pair[1].Value < pair[0].Value {
			return true
		}
	}
	return false
}

// ReadOnlySampleProducer is the hook a qualified read-only collection source
// implements before any live collection can be enabled. The matrix must
// document its evidence class as proving the provider-origin join; no
// repository runtime currently provides an implementation.
type ReadOnlySampleProducer interface {
	EvidenceClass() AttributionEvidenceClass
}

// ExactRetainedHistory is the hook that durably retains collected samples
// against an exact, monotonically advancing revision before any live
// collection can be enabled. No repository runtime currently provides an
// implementation.
type ExactRetainedHistory interface {
	RetainedRevision() int64
}

// CollectionHooks carries the connections DecideCollection requires before
// enabling collection. Either hook nil keeps every collection unavailable.
type CollectionHooks struct {
	Producer ReadOnlySampleProducer
	History  ExactRetainedHistory
}

// CollectionDecision is the bounded read-only collector verdict for one
// candidate job window.
type CollectionDecision struct {
	Collect bool
	Reason  UsageOmissionReason
}

// DecideCollection decides whether a bounded read-only collection may run
// for one candidate window. It fails closed at every layer: the documented
// matrix must prove the provider-origin join, the claim must carry
// positively verified origin evidence under the exact enrolled repository
// scope, both sampling brackets must match the same verified GARM row, and
// a supported read-only producer plus an exact retained-history hook must
// both be connected. Without them, collection stays unavailable and the
// explicit unavailable status is preserved. A name, mutable provider label,
// timing, or separate TLS on both reads cannot authorize collection.
func DecideCollection(matrix AttributionCapabilityMatrix, claim JobAttributionClaim, set VerifiedUsageSampleSet, hooks CollectionHooks, now time.Time) CollectionDecision {
	if !matrix.Supports(FactProviderOriginJoin) {
		return CollectionDecision{false, UsageOmissionJoinUnsupported}
	}
	if hooks.Producer == nil || hooks.History == nil || hooks.History.RetainedRevision() < 0 ||
		!matrix.ClassProves(hooks.Producer.EvidenceClass(), FactProviderOriginJoin) {
		return CollectionDecision{false, UsageOmissionCollectionUnavailable}
	}
	attribution, err := VerifyJobAttribution(matrix, claim, now)
	if err != nil {
		return CollectionDecision{false, classifyAttributionFailure(claim, now)}
	}
	if verdict := classifySampleSet(attribution, set, now); verdict != (omission{VerifiedUsageVerified, UsageOmissionNone}) {
		return CollectionDecision{false, verdict.reason}
	}
	return CollectionDecision{true, UsageOmissionNone}
}

// Wire is the read-only UI projection of one verified-usage record. Quantity
// fields are decimal strings so an eventual web adapter never narrows them
// into JavaScript Number values.
type Wire struct {
	State    VerifiedUsageState  `json:"state"`
	Reason   UsageOmissionReason `json:"reason,omitempty"`
	AsOf     time.Time           `json:"as_of"`
	Measured *MeasuredWire       `json:"measured,omitempty"`
}

type MeasuredWire struct {
	Samples int         `json:"samples"`
	Window  string      `json:"window_ns"`
	CPU     *MetricWire `json:"cpu,omitempty"`
	RAM     *MetricWire `json:"ram,omitempty"`
	Disk    DiskWire    `json:"disk"`
}

type MetricWire struct {
	CoveredNanoseconds string `json:"covered_ns"`
	Complete           bool   `json:"complete"`
	AverageMilliCPU    string `json:"average_millicpu,omitempty"`
	PeakMilliCPU       string `json:"peak_interval_millicpu,omitempty"`
	MeanBytes          string `json:"mean_bytes,omitempty"`
	SamplePeakBytes    string `json:"sample_peak_bytes,omitempty"`
	GaugeSamples       int    `json:"gauge_samples,omitempty"`
}

type DiskWire struct {
	State      VerifiedUsageState  `json:"state"`
	Reason     UsageOmissionReason `json:"reason,omitempty"`
	ReadBytes  string              `json:"read_bytes,omitempty"`
	WriteBytes string              `json:"write_bytes,omitempty"`
	UsedBytes  string              `json:"used_sample_peak_bytes,omitempty"`
	Covered    string              `json:"covered_ns,omitempty"`
	Complete   bool                `json:"complete,omitempty"`
}

// Wire projects the record for the CI Health UI under the documented
// capability matrix. Unavailable and Stale records project no measured
// block; Partial and Verified records project exactly the reducer's
// coverage, sample counts, time-weighted RAM average, sampled peak, and CPU
// consumption rates. Disk stays separate and reports its own unmeasured
// state instead of zero; disk counters are only shown when some documented
// source supports per-instance disk evidence and samples bound them. No
// documented source does, so the stock matrix never shows disk numbers.
func (r VerifiedJobUsageRecord) Wire(matrix AttributionCapabilityMatrix) Wire {
	out := Wire{State: r.State, Reason: r.Reason, AsOf: r.AsOf.UTC()}
	if r.Measured == nil || (r.State != VerifiedUsageVerified && r.State != VerifiedUsagePartial) {
		return out
	}
	summary := *r.Measured
	measured := &MeasuredWire{Samples: summary.Samples, Window: strconv.FormatUint(uint64(summary.Window), 10)}
	if summary.CPU != nil {
		measured.CPU = &MetricWire{
			CoveredNanoseconds: strconv.FormatUint(uint64(summary.CPU.Coverage.Duration), 10),
			Complete:           summary.CPU.Coverage.Complete,
			AverageMilliCPU:    strconv.FormatUint(summary.CPU.AverageMilliCPU, 10),
			PeakMilliCPU:       strconv.FormatUint(summary.CPU.PeakIntervalMilliCPU, 10),
		}
	}
	if summary.RAM != nil || summary.RAMMean != nil {
		ram := &MetricWire{}
		if summary.RAMMean != nil {
			ram.CoveredNanoseconds = strconv.FormatUint(uint64(summary.RAMMean.Coverage.Duration), 10)
			ram.Complete = summary.RAMMean.Coverage.Complete
			ram.MeanBytes = strconv.FormatUint(summary.RAMMean.Bytes, 10)
		}
		if summary.RAM != nil {
			ram.SamplePeakBytes = strconv.FormatUint(summary.RAM.SampleMax, 10)
			ram.GaugeSamples = summary.RAM.Samples
		}
		measured.RAM = ram
	}
	measured.Disk = DiskWire{State: VerifiedUsageUnavailable, Reason: UsageOmissionDiskUnmeasured}
	if summary.DiskRead != nil || summary.DiskWrite != nil || summary.DiskUsed != nil {
		if !matrix.Supports(FactDiskCounters) {
			// Disk counters arrived without any documented source that can
			// supply them: keep them hidden with an explicit reason instead
			// of showing numbers no source supports.
			measured.Disk = DiskWire{State: VerifiedUsageUnavailable, Reason: UsageOmissionDiskUnsupported}
		} else {
			measured.Disk = DiskWire{State: VerifiedUsageVerified}
			if summary.DiskRead != nil {
				measured.Disk.ReadBytes = strconv.FormatUint(summary.DiskRead.Bytes, 10)
				measured.Disk.Covered = strconv.FormatUint(uint64(summary.DiskRead.Coverage.Duration), 10)
				measured.Disk.Complete = summary.DiskRead.Coverage.Complete
			}
			if summary.DiskWrite != nil {
				measured.Disk.WriteBytes = strconv.FormatUint(summary.DiskWrite.Bytes, 10)
			}
			if summary.DiskUsed != nil {
				measured.Disk.UsedBytes = strconv.FormatUint(summary.DiskUsed.SampleMax, 10)
			}
		}
	}
	out.Measured = measured
	return out
}
