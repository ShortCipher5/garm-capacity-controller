package cihealth

// This file is the fixture-backed source capability contract for exact per-job
// resource attribution. It records what each documented read positively proves
// and, equally important, which joins no documented source supplies. The
// capability matrix is data, not authority: it describes the repository's own
// scoped readers and their published limits.

// AttributionEvidenceClass identifies one documented evidence source.
type AttributionEvidenceClass string

const (
	EvidenceGitHubAttemptJobs       AttributionEvidenceClass = "github_attempt_jobs"
	EvidenceGARMRunningRegistration AttributionEvidenceClass = "garm_running_registration"
	EvidenceIncusRuntimeHealth      AttributionEvidenceClass = "incus_runtime_health"
)

// AttributionFact is one positive fact a source read can document about an
// exact job attribution chain. Facts are precise: a source proves a fact only
// when its published contract positively supplies every dimension of it.
type AttributionFact string

const (
	// FactExactJobIdentity is the exact source/repository/workflow/run/attempt/
	// job binding read from one authenticated GitHub attempt-jobs page.
	FactExactJobIdentity AttributionFact = "exact_job_identity"
	// FactPositiveRunnerRegistration is the positive numeric GitHub runner
	// registration (agent ID) assigned to that exact job in the same read.
	FactPositiveRunnerRegistration AttributionFact = "positive_runner_registration"
	// FactExactStockRow is the exact authenticated GARM source/controller/
	// repository/pool/row binding for one running instance row.
	FactExactStockRow AttributionFact = "exact_stock_row"
	// FactPositiveStockRowAgent is the positive agent ID that exact row
	// reported within its short bounded observation lifetime.
	FactPositiveStockRowAgent AttributionFact = "positive_stock_row_agent"
	// FactStockProviderName is the provider name and provider ID a stock row
	// carries for its allocation. They are mutable labels chosen by stock
	// GARM; no read authenticates them against an immutable incarnation.
	FactStockProviderName AttributionFact = "stock_provider_name_unauthenticated"
	// FactIncusIncarnation is the authenticated Incus project/member/daemon
	// identity plus the immutable instance incarnation (volatile UUID,
	// generation and creation time) of the read's chosen subject.
	FactIncusIncarnation AttributionFact = "incus_incarnation"
	// FactProcessEpoch is the chosen subject's process boot/start epoch
	// (boot ID and process start ticks). Named Incus state supplies PID and
	// wall-clock start time, which cannot establish this fact.
	FactProcessEpoch AttributionFact = "process_epoch"
	// FactCPUCounters is the chosen subject's cumulative container CPU time.
	FactCPUCounters AttributionFact = "cpu_counters"
	// FactRAMCounters is the chosen subject's current container memory and
	// cgroup-lifetime high-water mark.
	FactRAMCounters AttributionFact = "ram_counters"
	// FactProviderOriginJoin is the original provider allocation binding one
	// exact stock row to one immutable Incus incarnation. NO documented
	// source proves this fact: the stock read reports a mutable name/label,
	// and an Incus read proves only its chosen subject. A name, mutable
	// provider label, timing, or separate TLS on both reads cannot supply it.
	FactProviderOriginJoin AttributionFact = "provider_origin_join"
	// FactDiskCounters is per-instance attributable disk usage/I-O. The
	// runtime health read supplies no disk counters; host free disk is not
	// job usage.
	FactDiskCounters AttributionFact = "disk_counters"
)

// AttributionCapability documents one evidence source: the exact facts its
// published read positively proves, the unauthenticated facts it merely
// carries, and the facts it can never prove on its own.
type AttributionCapability struct {
	Class                  AttributionEvidenceClass
	Proves                 []AttributionFact
	CarriesUnauthenticated []AttributionFact
	NeverProves            []AttributionFact
}

// AttributionCapabilityMatrix is the documented capability set. A fact is
// supported only when some source in the matrix proves it.
type AttributionCapabilityMatrix struct {
	Sources []AttributionCapability
}

// DocumentedMatrix returns the repository's published source capabilities.
// No documented source proves FactProviderOriginJoin, FactProcessEpoch or
// FactDiskCounters; callers must fail closed on those facts.
func DocumentedMatrix() AttributionCapabilityMatrix {
	return AttributionCapabilityMatrix{Sources: []AttributionCapability{
		{
			Class: EvidenceGitHubAttemptJobs,
			Proves: []AttributionFact{
				FactExactJobIdentity,
				FactPositiveRunnerRegistration,
			},
			NeverProves: []AttributionFact{
				FactExactStockRow,
				FactProviderOriginJoin,
				FactIncusIncarnation,
				FactProcessEpoch,
				FactCPUCounters,
				FactRAMCounters,
				FactDiskCounters,
			},
		},
		{
			Class: EvidenceGARMRunningRegistration,
			Proves: []AttributionFact{
				FactExactStockRow,
				FactPositiveStockRowAgent,
			},
			CarriesUnauthenticated: []AttributionFact{
				FactStockProviderName,
			},
			NeverProves: []AttributionFact{
				FactExactJobIdentity,
				FactPositiveRunnerRegistration,
				FactProviderOriginJoin,
				FactIncusIncarnation,
				FactProcessEpoch,
				FactCPUCounters,
				FactRAMCounters,
				FactDiskCounters,
			},
		},
		{
			Class: EvidenceIncusRuntimeHealth,
			Proves: []AttributionFact{
				FactIncusIncarnation,
				FactCPUCounters,
				FactRAMCounters,
			},
			NeverProves: []AttributionFact{
				FactProcessEpoch,
				FactExactJobIdentity,
				FactPositiveRunnerRegistration,
				FactExactStockRow,
				FactPositiveStockRowAgent,
				FactProviderOriginJoin,
				FactDiskCounters,
			},
		},
	}}
}

// Supports reports whether any documented source positively proves the fact.
func (m AttributionCapabilityMatrix) Supports(fact AttributionFact) bool {
	for _, source := range m.Sources {
		for _, proven := range source.Proves {
			if proven == fact {
				return true
			}
		}
	}
	return false
}

// ClassProves reports whether the identified source positively proves the
// fact within this matrix.
func (m AttributionCapabilityMatrix) ClassProves(class AttributionEvidenceClass, fact AttributionFact) bool {
	for _, source := range m.Sources {
		if source.Class != class {
			continue
		}
		for _, proven := range source.Proves {
			if proven == fact {
				return true
			}
		}
		return false
	}
	return false
}
