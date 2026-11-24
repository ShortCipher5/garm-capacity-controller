package adminview

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// CISizingSnapshot is an optional view publisher for a verified advisory
// sizing producer. Request handling reads a bounded pre-encoded snapshot
// from memory only; nothing here collects resources, computes the model or
// writes settings. A zero-value instance is not a configured source, and
// until a verified producer publishes, the route serves the unavailable
// state. No live producer is wired: recommendations stay disabled until
// verified job resources and durable exact history exist.
type CISizingSnapshot struct {
	scopes     []ciViewScope
	clock      func() time.Time
	mu         sync.Mutex
	latest     time.Time
	checked    time.Time
	generation *ciSizingGeneration
	body       []byte
	stopped    bool
}

type ciSizingGeneration struct{ reserved byte }

func NewCISizingSnapshot(approved []CIHealthRepository) (*CISizingSnapshot, error) {
	if len(approved) == 0 || len(approved) > maxCIScopes {
		return nil, ErrConfiguration
	}
	scopes := make([]ciViewScope, 0, len(approved))
	seen := make(map[ciViewScope]bool, len(approved))
	for _, repo := range approved {
		scope := ciViewScope{repo.Source, repo.RepositoryID}
		if !ciHealthSourceLabel(scope.source) || scope.repository <= 0 || seen[scope] {
			return nil, ErrConfiguration
		}
		scopes = append(scopes, scope)
		seen[scope] = true
	}
	return &CISizingSnapshot{scopes: scopes, clock: time.Now, generation: &ciSizingGeneration{}}, nil
}

// Publish replaces the visible advisory computed and reviewed by a trusted
// producer. The publication time must equal the advisory review time and the
// advisory must belong to an approved startup scope; versions must match the
// pinned policy and advisory derivations. On a failed publication the caller
// must Withdraw.
func (s *CISizingSnapshot) Publish(observedAt time.Time, advisory cihealth.JobSizingAdvisory) error {
	if s == nil || len(s.scopes) == 0 {
		return ErrConfiguration
	}
	at := observedAt.UTC()
	lease := s.begin(at)
	if lease == nil {
		return errCIProjection
	}
	if advisory.Version != cihealth.JobSizingAdvisoryVersion || advisory.PolicyVersion != cihealth.JobSizingCohortPolicyVersion ||
		!advisory.Window.AsOf.Equal(at) || !s.withinScopes(advisory.Scope) || !validCISizingAdvisory(advisory) {
		return errCIProjection
	}
	wire, err := json.Marshal(struct {
		State  string       `json:"state"`
		At     time.Time    `json:"observed_at"`
		Sizing ciSizingView `json:"sizing"`
	}{State: "observed", At: at, Sizing: ciSizingViewFor(advisory)})
	if err != nil || len(wire) > maxCIResponseBytes {
		return errCIProjection
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validAtLocked(at) || lease != s.generation {
		return errCIProjection
	}
	s.latest, s.body, s.generation = at, wire, &ciSizingGeneration{}
	return nil
}

func (s *CISizingSnapshot) withinScopes(scope cihealth.TrendScope) bool {
	for _, approved := range s.scopes {
		if approved.source == scope.Source && approved.repository == scope.RepositoryID {
			return true
		}
	}
	return false
}

// validCISizingAdvisory keeps the wire contract closed to producers that
// bypass the pinned advisory derivation.
func validCISizingAdvisory(a cihealth.JobSizingAdvisory) bool {
	outcomes := map[string]bool{
		cihealth.JobSizingAdvisoryUnreviewed: true, cihealth.JobSizingAdvisoryIncreaseAdvised: true,
		cihealth.JobSizingAdvisoryUnchanged: true, cihealth.JobSizingAdvisoryNeedsManualReview: true,
		cihealth.JobSizingAdvisoryNeedsRiskOrOutcomeReview: true, cihealth.JobSizingAdvisoryCohortIncomplete: true,
		cihealth.JobSizingAdvisoryCoverageUnavailable: true, cihealth.JobSizingAdvisoryInsufficientHistory: true,
		cihealth.JobSizingAdvisoryStaleHistory: true,
	}
	if a.Coverage > cihealth.TrendCoverageComplete || !outcomes[a.Outcome] || !outcomes[a.CPU.State] || !outcomes[a.RAM.State] {
		return false
	}
	fit := map[string]bool{cihealth.JobSizingHostFitUnknown: true, cihealth.JobSizingHostFitFits: true, cihealth.JobSizingHostFitDoesNotFit: true}
	if !fit[a.Host.Fit] {
		return false
	}
	disk := map[string]bool{cihealth.JobSizingDiskUnavailable: true, cihealth.JobSizingDiskObserved: true}
	if !disk[a.Disk.State] {
		return false
	}
	if a.Uncertainty != "" && a.Uncertainty != cihealth.JobSizingUncertaintySampledPeakBound {
		return false
	}
	warnings := map[string]bool{
		cihealth.JobSizingWarningSustainedCPUPressure: true, cihealth.JobSizingWarningSustainedRAMPressure: true,
		cihealth.JobSizingWarningInsufficientSpareCapacity: true,
	}
	for _, warning := range a.Warnings {
		if !warnings[warning] {
			return false
		}
	}
	return true
}

func (s *CISizingSnapshot) begin(at time.Time) *ciSizingGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validAtLocked(at) {
		return nil
	}
	return s.generation
}

func (s *CISizingSnapshot) validAtLocked(at time.Time) bool {
	now, stable := s.wallLocked()
	return stable && !s.stopped && !at.IsZero() && at.After(s.latest) && !at.After(now) && now.Before(at.Add(ciDisplayLifetime))
}

func (s *CISizingSnapshot) wallLocked() (time.Time, bool) {
	now := s.clock().UTC()
	if now.Before(s.checked) {
		s.fenceLocked()
		return now, false
	}
	s.checked = now
	return now, true
}

// Withdraw clears the current result and fences every in-flight publication.
// A later publication cannot resurrect data at or before the withdrawal.
func (s *CISizingSnapshot) Withdraw() {
	if s == nil || s.clock == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.wallLocked()
	s.fenceLocked()
}

// Stop permanently fences publications before the owned producer closes.
func (s *CISizingSnapshot) Stop() {
	if s == nil || s.clock == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.fenceLocked()
}

func (s *CISizingSnapshot) fenceLocked() {
	s.body, s.generation = nil, &ciSizingGeneration{}
	if s.checked.After(s.latest) {
		s.latest = s.checked
	}
}

func (s *CISizingSnapshot) read() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	now, stable := s.wallLocked()
	if !stable || now.Before(s.latest) || !now.Before(s.latest.Add(ciDisplayLifetime)) {
		s.body = nil
	}
	return s.body
}

// SetCISizingSnapshot is a one-time explicit startup attachment. This adds no
// route without a configured source and does not initiate I/O or enable
// recommendations.
func (h *Handler) SetCISizingSnapshot(s *CISizingSnapshot) error {
	if s == nil || len(s.scopes) == 0 || !h.ciSizing.CompareAndSwap(nil, s) {
		return ErrConfiguration
	}
	return nil
}

func (h *Handler) serveCISizing(w http.ResponseWriter, r *http.Request, source *CISizingSnapshot) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body := source.read()
	if body == nil {
		_, _ = w.Write([]byte("{\"state\":\"unavailable\",\"sizing\":null}\n"))
		return
	}
	_, _ = w.Write(body)
}

type ciSizingScopeView struct {
	Source     string `json:"source"`
	Repository string `json:"repository_id"`
	Workflow   string `json:"workflow_id"`
}

type ciSizingIdentityView struct {
	Matrix   string `json:"matrix"`
	Profile  string `json:"profile"`
	Revision string `json:"revision"`
}

type ciSizingWindowView struct {
	From                   time.Time `json:"from"`
	AsOf                   time.Time `json:"as_of"`
	LatestCompletionMaxAge string    `json:"latest_completion_max_age_ns"`
}

type ciSizingFreshnessView struct {
	LatestPrimaryCompletion *time.Time `json:"latest_primary_completion"`
	Age                     string     `json:"age_ns"`
	Fresh                   bool       `json:"fresh"`
}

type ciSizingSamplesView struct {
	Eligible        int `json:"eligible"`
	Primary         int `json:"primary"`
	Retried         int `json:"retried"`
	Successful      int `json:"successful"`
	AtRisk          int `json:"at_risk"`
	Unknown         int `json:"unknown"`
	Unsuccessful    int `json:"unsuccessful"`
	Throttled       int `json:"throttled"`
	OOM             int `json:"oom"`
	DiskFull        int `json:"disk_full"`
	ExternalBuilder int `json:"external_builder"`
	QualifyingCPU   int `json:"qualifying_cpu"`
	QualifyingRAM   int `json:"qualifying_ram"`
}

type ciSizingObservedView struct {
	CPUPeakMilli *string `json:"cpu_peak_milli"`
	RAMPeakBytes *string `json:"ram_peak_bytes"`
	CPUSaturated int     `json:"cpu_saturated"`
	RAMSaturated int     `json:"ram_saturated"`
}

type ciSizingShapeView struct {
	CPUMilli string `json:"cpu_milli"`
	RAMBytes string `json:"ram_bytes"`
}

type ciSizingDimensionView struct {
	State     string  `json:"state"`
	Suggested *string `json:"suggested"`
	Reason    string  `json:"reason"`
}

type ciSizingHostView struct {
	Known             bool    `json:"known"`
	Host              string  `json:"host"`
	CPUBudgetMilli    *string `json:"cpu_budget_milli"`
	CPUAllocatedMilli *string `json:"cpu_allocated_milli"`
	CPUHeadroomMilli  *string `json:"cpu_headroom_milli"`
	RAMBudgetBytes    *string `json:"ram_budget_bytes"`
	RAMAllocatedBytes *string `json:"ram_allocated_bytes"`
	RAMHeadroomBytes  *string `json:"ram_headroom_bytes"`
	Fit               string  `json:"fit"`
}

type ciSizingDiskView struct {
	State     string  `json:"state"`
	PeakBytes *string `json:"peak_bytes"`
	Samples   int     `json:"samples"`
	Qualified int     `json:"qualified"`
}

type ciSizingRejectionView struct {
	Source     string `json:"source"`
	Repository string `json:"repository_id"`
	Workflow   string `json:"workflow_id"`
	Run        string `json:"run_id"`
	Attempt    string `json:"attempt"`
	Job        string `json:"job_id"`
	Reason     string `json:"reason"`
}

// ciSizingView is the deterministic owner-view projection of one advisory.
// All exact numeric values are decimal strings; missing facts are null, not
// zero. Rejection records only ever carry in-scope identifiers; omitted
// candidates appear as aggregate counts.
type ciSizingView struct {
	PolicyVersion   string                  `json:"policy_version"`
	AdvisoryVersion string                  `json:"advisory_version"`
	Outcome         string                  `json:"outcome"`
	Scope           ciSizingScopeView       `json:"scope"`
	Identity        ciSizingIdentityView    `json:"identity"`
	Window          ciSizingWindowView      `json:"window"`
	Freshness       ciSizingFreshnessView   `json:"freshness"`
	Coverage        string                  `json:"coverage"`
	Samples         ciSizingSamplesView     `json:"samples"`
	Observed        ciSizingObservedView    `json:"observed"`
	Installed       ciSizingShapeView       `json:"installed"`
	CPU             ciSizingDimensionView   `json:"cpu"`
	RAM             ciSizingDimensionView   `json:"ram"`
	Uncertainty     string                  `json:"uncertainty"`
	Host            ciSizingHostView        `json:"host"`
	Warnings        []string                `json:"warnings"`
	Disk            ciSizingDiskView        `json:"disk"`
	Rejected        []ciSizingRejectionView `json:"rejected"`
	Incomparable    int                     `json:"incomparable"`
	Unverifiable    int                     `json:"unverifiable"`
	Foreign         int                     `json:"foreign"`
}

func ciSizingViewFor(a cihealth.JobSizingAdvisory) ciSizingView {
	coverage := "unknown"
	switch a.Coverage {
	case cihealth.TrendCoveragePartial:
		coverage = "partial"
	case cihealth.TrendCoverageComplete:
		coverage = "complete"
	}
	out := ciSizingView{
		PolicyVersion:   a.PolicyVersion,
		AdvisoryVersion: a.Version,
		Outcome:         a.Outcome,
		Scope: ciSizingScopeView{Source: a.Scope.Source, Repository: ciInt(a.Scope.RepositoryID),
			Workflow: ciInt(a.Scope.WorkflowID)},
		Identity: ciSizingIdentityView{Matrix: a.Identity.Matrix, Profile: a.Identity.Profile, Revision: a.Identity.Revision},
		Window: ciSizingWindowView{From: a.Window.From, AsOf: a.Window.AsOf,
			LatestCompletionMaxAge: ciInt(int64(a.Window.LatestCompletionMaxAge))},
		Freshness: ciSizingFreshnessView{Age: ciInt(int64(a.Freshness.Age)), Fresh: a.Freshness.Fresh},
		Coverage:  coverage,
		Samples: ciSizingSamplesView{
			Eligible: a.Samples.Eligible, Primary: a.Samples.Primary, Retried: a.Samples.Retried, Successful: a.Samples.Successful,
			AtRisk: a.Samples.AtRisk, Unknown: a.Samples.Unknown, Unsuccessful: a.Samples.Unsuccessful,
			Throttled: a.Samples.Throttled, OOM: a.Samples.OOM, DiskFull: a.Samples.DiskFull, ExternalBuilder: a.Samples.ExternalBuilder,
			QualifyingCPU: a.Samples.QualifyingCPU, QualifyingRAM: a.Samples.QualifyingRAM,
		},
		Installed:   ciSizingShapeView{CPUMilli: ciUint(a.Installed.CPUMilli), RAMBytes: ciUint(a.Installed.RAMBytes)},
		CPU:         ciSizingDimensionView{State: a.CPU.State, Suggested: ciUintPtr(a.CPU.Suggested), Reason: a.CPU.Reason},
		RAM:         ciSizingDimensionView{State: a.RAM.State, Suggested: ciUintPtr(a.RAM.Suggested), Reason: a.RAM.Reason},
		Uncertainty: a.Uncertainty,
		Host:        ciSizingHostViewFor(a.Host),
		Warnings:    append([]string{}, a.Warnings...),
		Disk: ciSizingDiskView{State: a.Disk.State, PeakBytes: ciUintKnown(a.Disk.Peak, a.Disk.State == cihealth.JobSizingDiskObserved),
			Samples: a.Disk.Samples, Qualified: a.Disk.Qualified},
		Rejected:     make([]ciSizingRejectionView, 0, len(a.Rejected)),
		Incomparable: a.Incomparable,
		Unverifiable: a.Unverifiable,
		Foreign:      a.Foreign,
	}
	if !a.Freshness.LatestPrimaryCompletion.IsZero() {
		latest := a.Freshness.LatestPrimaryCompletion
		out.Freshness.LatestPrimaryCompletion = &latest
	}
	if a.Observed.CPUKnown {
		out.Observed.CPUPeakMilli = ciUintPtrValue(a.Observed.CPUPeakMilli)
	}
	if a.Observed.RAMKnown {
		out.Observed.RAMPeakBytes = ciUintPtrValue(a.Observed.RAMPeakBytes)
	}
	out.Observed.CPUSaturated, out.Observed.RAMSaturated = a.Observed.CPUSaturated, a.Observed.RAMSaturated
	for _, rejection := range a.Rejected {
		job := rejection.Job
		out.Rejected = append(out.Rejected, ciSizingRejectionView{
			Source: job.Attempt.Source, Repository: ciInt(job.Attempt.RepositoryID), Workflow: ciInt(job.Attempt.WorkflowID),
			Run: ciInt(job.Attempt.RunID), Attempt: ciInt(job.Attempt.Attempt), Job: ciInt(job.JobID), Reason: string(rejection.Reason),
		})
	}
	return out
}

func ciSizingHostViewFor(host cihealth.JobSizingAdvisoryHost) ciSizingHostView {
	out := ciSizingHostView{Known: host.Known, Fit: host.Fit}
	if !host.Known {
		return out
	}
	out.Host = host.Host
	out.CPUBudgetMilli = ciUintPtrValue(host.CPUBudgetMilli)
	out.CPUAllocatedMilli = ciUintPtrValue(host.CPUAllocatedMilli)
	out.CPUHeadroomMilli = ciUintPtrValue(host.CPUHeadroomMilli)
	out.RAMBudgetBytes = ciUintPtrValue(host.RAMBudgetBytes)
	out.RAMAllocatedBytes = ciUintPtrValue(host.RAMAllocatedBytes)
	out.RAMHeadroomBytes = ciUintPtrValue(host.RAMHeadroomBytes)
	return out
}

func ciUint(v uint64) string { return strconv.FormatUint(v, 10) }

func ciUintPtr(v *uint64) *string {
	if v == nil {
		return nil
	}
	return ciUintPtrValue(*v)
}

func ciUintPtrValue(v uint64) *string {
	s := ciUint(v)
	return &s
}

func ciUintKnown(v uint64, known bool) *string {
	if !known {
		return nil
	}
	return ciUintPtrValue(v)
}
