package cihealthsource

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const (
	completedUsageTimeout     = 20 * time.Second
	completedUsageSampleLimit = 120
)

// A locator for an independently enrolled row and one exact GitHub page.
// A page number only reduces read work. It cannot identify a job or runner.
type jobUsageTarget struct {
	job   cihealth.JobKey
	page  int
	scope garmapi.ManagedRunningRegistrationScope
	rowID string
}

// Only a reviewed source that positively proves the original allocation,
// exclusive execution through the entire window, and the counter epoch can
// implement this private boundary. Each read returns an individually bound
// point from that captured window. Count and order must describe the same
// immutable capture, including every intervening point. Reads must honor cancellation.
// Stock GARM and named Incus state cannot implement this contract.
type exclusiveJobCounterSource interface {
	cihealth.ReadOnlySampleProducer
	openExclusive(context.Context, cihealth.StockJobExecutionObservation, cihealth.StockRowEvidence) (exclusiveCounterWindow, cihealth.UsageOmissionReason)
}

type exclusiveCounterWindow struct {
	origin           cihealth.VerifiedProviderOrigin
	initial, closing cihealth.StockJobResourceBinding
	// Independently confirmed exclusive job and interval on this runtime.
	execution cihealth.StockJobExecutionObservation
	count     int
	maxGap    time.Duration
	read      func(context.Context, int) (cihealth.JobResourceSample, cihealth.StockRunnerIncarnation, error)
	current   func() bool // local proof, no I/O
}

type jobRegistration struct {
	execution  cihealth.StockJobExecutionObservation
	row        cihealth.StockRowEvidence
	enrollment cihealth.EnrolledRowScope
	current    func() bool // reopens the same two opaque receipts locally
}

// An owner would bind one independently enrolled source/repository/pool
// to these readers. No production source is registered and no command or
// scheduler constructs a collector. One owner can collect one job at a time.
type completedJobCollector struct {
	history  *CycleHistory
	store    *cihealth.UsageAttachmentStore
	scope    garmapi.ManagedRunningRegistrationScope
	garm     *garmapi.DemandClient
	counters exclusiveJobCounterSource
	busy     atomic.Bool
}

func (c *completedJobCollector) collect(ctx context.Context, cursor cihealth.HistoryCursor, job cihealth.JobKey, rowID string, pageNumber int) cihealth.UsageOmissionReason {
	matrix := cihealth.DocumentedMatrix()
	if !matrix.Supports(cihealth.FactProviderOriginJoin) || !matrix.Supports(cihealth.FactProcessEpoch) {
		return cihealth.UsageOmissionJoinUnsupported
	}
	if c == nil || c.garm == nil {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	if _, qualified := qualifiedMetrics(matrix, c.counters); !qualified || !c.busy.CompareAndSwap(false, true) {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	defer c.busy.Store(false)
	h, k := c.history, job.Attempt
	target := jobUsageTarget{job: job, rowID: rowID, page: pageNumber, scope: c.scope}
	if !h.allows(cihealth.HistoryScope{Source: k.Source, RepositoryID: k.RepositoryID, WorkflowID: k.WorkflowID}) ||
		h.owner.observer == nil || ctx == nil || ctx.Err() != nil ||
		target.page < 1 || target.page > demandrepair.CIMaxPages {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	deadline, cancel := context.WithTimeout(ctx, completedUsageTimeout)
	defer cancel()
	readCtx, release, err := h.owner.observer.BindLocalLifetime(deadline)
	if err != nil {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	defer release()
	window, ok := c.store.BeginCompletedUsage(readCtx, h.history, cursor, target.job)
	if !ok {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	active := func() bool { return deadline.Err() == nil && window.Current(readCtx) }
	// Verify the numeric repository's configured GARM mapping before either
	// remote read, even if another enrolled repo could return a similar row.
	if !active() || !target.enrolledBy(h.owner.observer) {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	start, end := window.Endpoints()
	attempt, assignment, ok := target.readAssignment(readCtx, h.owner.observer, start, end, active)
	if !ok {
		return cihealth.UsageOmissionUnbracketed
	}
	report, err := c.garm.ObserveRunningRegistration(readCtx, target.scope, target.rowID)
	reg, ok := target.registration(attempt, assignment, report)
	if err != nil || !ok {
		return cihealth.UsageOmissionEvidenceMismatch
	}
	current := reg.current
	reg.current = func() bool { return deadline.Err() == nil && current() }
	return collectCounterWindow(readCtx, matrix, reg, c.counters, active, window.Attach, time.Now)
}

func (t jobUsageTarget) readAssignment(ctx context.Context, source AttemptSource, start, end time.Time, active func() bool) (demandrepair.CIAttempt, demandrepair.CIRunnerAssignmentReceipt, bool) {
	k := t.job.Attempt
	attempt, err := source.ReadCIAttempt(ctx, t.scope.RepositoryID, k.RunID, k.Attempt)
	if err != nil || !active() || attempt.RepositoryID() != k.RepositoryID || attempt.Run().WorkflowID != k.WorkflowID {
		return demandrepair.CIAttempt{}, demandrepair.CIRunnerAssignmentReceipt{}, false
	}
	page, err := source.ListCIAttemptJobs(ctx, attempt, t.page)
	assignment, ok := page.RunnerAssignment(attempt, t.job.JobID)
	x, ended := assignment.CompletedExecution(attempt)
	if err != nil || !ok || !ended || x.Job != t.job || !x.StartedAt.Equal(start) || !x.EndedAt.Equal(end) || !active() {
		return demandrepair.CIAttempt{}, demandrepair.CIRunnerAssignmentReceipt{}, false
	}
	return attempt, assignment, true
}

func (t jobUsageTarget) enrolledBy(source Source) bool {
	if source.SourceID() != t.job.Attempt.Source {
		return false
	}
	for _, repo := range source.Repositories() {
		if repo.GitHubID == t.job.Attempt.RepositoryID && repo.GarmID == t.scope.RepositoryID {
			return true
		}
	}
	return false
}

func (t jobUsageTarget) registration(a demandrepair.CIAttempt, receipt demandrepair.CIRunnerAssignmentReceipt, report garmapi.ManagedRunningRegistrationReport) (jobRegistration, bool) {
	execution, ended := receipt.CompletedExecution(a)
	agent, at, registered := report.ReportedAgentID(t.scope, t.rowID)
	if !ended || execution.Job != t.job || !registered || agent != execution.RunnerAgentID {
		return jobRegistration{}, false
	}
	s := t.scope
	return jobRegistration{
		execution: execution,
		row: cihealth.StockRowEvidence{
			SourceIdentity: s.SourceIdentity, ControllerID: s.ControllerID, RepositoryID: s.RepositoryID, PoolID: s.PoolID,
			RowID: t.rowID, ProviderName: s.ProviderName, OwnershipFingerprint: s.OwnershipFingerprint, AgentID: agent, ObservedAt: at,
		},
		enrollment: cihealth.EnrolledRowScope{
			GitHubSource: t.job.Attempt.Source, GitHubRepositoryID: t.job.Attempt.RepositoryID,
			SourceIdentity: s.SourceIdentity, ControllerID: s.ControllerID, RepositoryID: s.RepositoryID, PoolID: s.PoolID,
			ProviderName: s.ProviderName, OwnershipFingerprint: s.OwnershipFingerprint,
		},
		current: func() bool {
			got, ok := receipt.CompletedExecution(a)
			id, observed, valid := report.ReportedAgentID(s, t.rowID)
			return ok && got == execution && valid && id == agent && observed.Equal(at)
		},
	}, true
}

type collectorMetrics struct {
	cpu, ram, readBytes, writeBytes, usedBytes bool
}

// Qualification applies to the producer that will actually be called. Facts
// independently proved by some unrelated reader cannot authorize this one.
func qualifiedMetrics(matrix cihealth.AttributionCapabilityMatrix, source exclusiveJobCounterSource) (collectorMetrics, bool) {
	if source == nil {
		return collectorMetrics{}, false
	}
	class := source.EvidenceClass()
	proves := func(fact cihealth.AttributionFact) bool { return matrix.ClassProves(class, fact) }
	if !proves(cihealth.FactProviderOriginJoin) || !proves(cihealth.FactProcessEpoch) || !proves(cihealth.FactExclusiveJobWindow) {
		return collectorMetrics{}, false
	}
	disk := proves(cihealth.FactDiskCounters)
	m := collectorMetrics{cpu: proves(cihealth.FactCPUCounters), ram: proves(cihealth.FactRAMCounters),
		readBytes: disk && proves(cihealth.FactDiskReadBytes), writeBytes: disk && proves(cihealth.FactDiskWriteBytes), usedBytes: disk && proves(cihealth.FactDiskUsedBytes)}
	return m, m != (collectorMetrics{})
}

// Only qualified quantities enter the reducer and the sidecar input digest.
func (m collectorMetrics) restrict(sample *cihealth.JobResourceSample) {
	if !m.cpu {
		sample.CPUTimeNanos = cihealth.ResourceValue{}
	}
	if !m.ram {
		sample.RAMBytes = cihealth.ResourceValue{}
	}
	if !m.readBytes {
		sample.DiskReadBytes = cihealth.ResourceValue{}
	}
	if !m.writeBytes {
		sample.DiskWriteBytes = cihealth.ResourceValue{}
	}
	if !m.usedBytes {
		sample.DiskUsedBytes = cihealth.ResourceValue{}
	}
}

type attachUsage func(context.Context, cihealth.JobAttributionClaim, cihealth.VerifiedUsageSampleSet, func() bool) error

func collectCounterWindow(ctx context.Context, matrix cihealth.AttributionCapabilityMatrix, reg jobRegistration, counters exclusiveJobCounterSource,
	retained func() bool, attach attachUsage, now func() time.Time) cihealth.UsageOmissionReason {
	active := func() bool { return ctx.Err() == nil && reg.current() && retained() }
	if !matrix.Supports(cihealth.FactProviderOriginJoin) || !matrix.Supports(cihealth.FactProcessEpoch) {
		return cihealth.UsageOmissionJoinUnsupported
	}
	metrics, qualified := qualifiedMetrics(matrix, counters)
	if !qualified || !active() {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	w, reason := counters.openExclusive(ctx, reg.execution, reg.row)
	if reason != cihealth.UsageOmissionNone {
		return reason
	}
	if w.count < 0 || w.count > completedUsageSampleLimit || w.read == nil || w.current == nil ||
		!active() || !w.current() {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	job := reg.execution.Job
	claim := cihealth.JobAttributionClaim{Job: job, Enrolled: reg.enrollment, Execution: reg.execution, Row: reg.row,
		Incarnation: w.initial.Runtime, Origin: w.origin}
	// The independent provider-side observation must prove exclusive
	// assignment for this exact completed interval, including any gap
	// between samples. Absence from a listing is never such an observation.
	x := w.execution
	if reg.execution.State != cihealth.StockJobObservedEnded || x.State != cihealth.StockJobObservedEnded ||
		x.Job != job || x.RunnerAgentID != reg.execution.RunnerAgentID ||
		!x.StartedAt.Equal(reg.execution.StartedAt) || !x.EndedAt.Equal(reg.execution.EndedAt) {
		return cihealth.UsageOmissionOverlap
	}
	asOf := reg.execution.ObservedAt
	for _, at := range [...]time.Time{reg.row.ObservedAt, x.ObservedAt, w.closing.ObservedAt} {
		if at.After(asOf) {
			asOf = at
		}
	}
	window := cihealth.VerifiedUsageSampleSet{
		Scope: cihealth.JobResourceScope{Job: job, RunnerIncarnation: w.initial.RunnerIncarnation, StartedAt: x.StartedAt,
			EndAt: x.EndedAt, AsOf: asOf, MaxGap: w.maxGap},
		Initial: &w.initial, Closing: &w.closing, Execution: &w.execution,
	}
	// Check origin, epoch, job brackets and expiry before fetching points.
	if reason := cihealth.CheckJobUsageWindow(matrix, claim, window, now()); reason != cihealth.UsageOmissionNone {
		return reason
	}
	set := window
	set.Samples = make([]cihealth.JobResourceSample, 0, w.count)
	for i := range w.count {
		if !active() || !w.current() {
			return cihealth.UsageOmissionCollectionUnavailable
		}
		// Each read needs the original independent observations to still
		// be fresh. Keep the window sample-free for this fixed-cost check.
		if reason := cihealth.CheckJobUsageWindow(matrix, claim, window, now()); reason != cihealth.UsageOmissionNone {
			return reason
		}
		sample, runtime, err := w.read(ctx, i)
		if err != nil {
			return cihealth.UsageOmissionCollectionUnavailable
		}
		if runtime != claim.Incarnation || sample.Job != job || sample.RunnerIncarnation != set.Scope.RunnerIncarnation {
			return cihealth.UsageOmissionEvidenceMismatch
		}
		if sample.At.Before(x.StartedAt) || sample.At.After(x.EndedAt) ||
			i > 0 && !sample.At.After(set.Samples[i-1].At) {
			return cihealth.UsageOmissionUnbracketed
		}
		metrics.restrict(&sample)
		set.Samples = append(set.Samples, sample)
	}
	result := cihealth.BuildVerifiedJobUsage(matrix, claim, set, now())
	if result.Measured == nil {
		return result.Reason
	}
	current := func() bool { return ctx.Err() == nil && reg.current() && w.current() }
	if !retained() || !current() || attach(ctx, claim, set, current) != nil {
		return cihealth.UsageOmissionCollectionUnavailable
	}
	return cihealth.UsageOmissionNone
}
