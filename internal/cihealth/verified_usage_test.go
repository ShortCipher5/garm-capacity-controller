package cihealth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// usageFixtureSet builds the bounded sample set matching attributionFixtureClaim:
// an initial binding before job start, a closing binding at the cutoff, and
// three in-window samples with known CPU/RAM counters and no disk counters.
func usageFixtureSet(claim JobAttributionClaim, now time.Time, maxGap time.Duration) VerifiedUsageSampleSet {
	scope := JobResourceScope{
		Job: claim.Job, RunnerIncarnation: strings.Repeat("1", 64), StartedAt: claim.Execution.StartedAt,
		EndAt: claim.Execution.StartedAt.Add(6 * time.Second), AsOf: now, MaxGap: maxGap,
	}
	binding := StockJobResourceBinding{
		Job: claim.Job, RunnerIncarnation: scope.RunnerIncarnation, RunnerAgentID: claim.Row.AgentID,
		StockSourceID: claim.Row.SourceIdentity, StockInstanceUUID: claim.Row.RowID, StockPoolUUID: claim.Row.PoolID,
		Runtime: claim.Incarnation, State: StockRunnerObservedRunning,
	}
	initial := binding
	initial.ObservedAt = scope.StartedAt.Add(-time.Second)
	closing := binding
	closing.ObservedAt = scope.EndAt
	samples := make([]JobResourceSample, 0, 3)
	for i, at := range []time.Duration{0, 2 * time.Second, 6 * time.Second} {
		sample := JobResourceSample{Job: claim.Job, RunnerIncarnation: scope.RunnerIncarnation, At: scope.StartedAt.Add(at)}
		sample.CPUTimeNanos = knownResource([]uint64{10e9, 11e9, 16e9}[i])
		sample.RAMBytes = knownResource([]uint64{10, 30, 50}[i])
		samples = append(samples, sample)
	}
	execution := claim.Execution
	return VerifiedUsageSampleSet{Scope: scope, Initial: &initial, Closing: &closing, Execution: &execution, Samples: samples}
}

func TestBuildVerifiedJobUsageProducesExpectedNumbersForProvenWindow(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
	if record.State != VerifiedUsageVerified || record.Reason != UsageOmissionNone {
		t.Fatalf("proven window not verified: %+v", record)
	}
	if record.Job != claim.Job || !record.AsOf.Equal(now.UTC()) {
		t.Fatalf("record identity drifted: %+v", record)
	}
	if record.Measured == nil {
		t.Fatal("verified record must carry the measured summary")
	}
	full := ResourceCoverage{Intervals: 2, Duration: 6 * time.Second, Complete: true}
	got := *record.Measured
	if got.Window != 6*time.Second || got.Samples != 3 || got.Sampling != (ResourceSamplingCoverage{}) ||
		got.CPU == nil || *got.CPU != (JobCPURates{1000, 1250, full}) ||
		got.RAMMean == nil || *got.RAMMean != (JobRAMAverage{33, full}) ||
		got.RAM == nil || *got.RAM != (ResourceGaugeSummary{3, 50, 50, set.Scope.EndAt}) {
		t.Fatalf("unexpected measured summary: %+v", got)
	}
	// Disk was not independently bound: it stays separate and unmeasured,
	// never zero and never derived from host free disk.
	if got.DiskRead != nil || got.DiskWrite != nil || got.DiskUsed != nil {
		t.Fatalf("unbound disk counters must stay absent: %+v", got)
	}
}

func TestBuildVerifiedJobUsageReportsPartialCoverageAccurately(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, 3*time.Second) // the 4s cadence gap loses the last interval
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
	if record.State != VerifiedUsagePartial || record.Measured == nil {
		t.Fatalf("incomplete sampling must be partial with numbers: %+v", record)
	}
	got := record.Measured
	if got.CPU == nil || got.CPU.Coverage.Complete || got.CPU.Coverage.Duration != 2*time.Second ||
		got.CPU.AverageMilliCPU != 500 || got.CPU.PeakIntervalMilliCPU != 500 {
		t.Fatalf("partial CPU coverage not preserved: %+v", got.CPU)
	}
	if got.Sampling.CadenceGaps != 1 || got.Sampling.CadenceGapDuration != 4*time.Second {
		t.Fatalf("sampling holes not preserved: %+v", got.Sampling)
	}
	// RAM pairs share the lost interval, so its coverage matches the CPU hole.
	if got.RAMMean == nil || got.RAMMean.Coverage.Duration != 2*time.Second || got.RAMMean.Coverage.Complete ||
		got.RAMMean.Bytes != 20 {
		t.Fatalf("partial RAM coverage not preserved: %+v", got.RAMMean)
	}
}

func TestBuildVerifiedJobUsageNeverAttributesAcrossUnsafeEvidence(t *testing.T) {
	cases := map[string]struct {
		alter func(*JobAttributionClaim, *VerifiedUsageSampleSet)
		want  UsageOmissionReason
	}{
		"absent origin": {func(c *JobAttributionClaim, _ *VerifiedUsageSampleSet) {
			c.Origin = VerifiedProviderOrigin{}
		}, UsageOmissionJoinUnsupported},
		"cross runner": {func(c *JobAttributionClaim, _ *VerifiedUsageSampleSet) {
			c.Row.AgentID++
			c.Origin.Row = c.Row
		}, UsageOmissionEvidenceMismatch},
		"reused instance at closing bracket": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Closing.Runtime.BootID = "55555555-5555-4555-8555-555555555556"
		}, UsageOmissionEvidenceMismatch},
		"reset counters stitched": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Samples[2].CPUTimeNanos = knownResource(1)
		}, UsageOmissionCounterDiscontinuity},
		"stale future sighting": {func(c *JobAttributionClaim, _ *VerifiedUsageSampleSet) {
			c.Row.ObservedAt = c.Row.ObservedAt.Add(time.Hour)
			c.Origin.Row = c.Row
		}, UsageOmissionStaleEvidence},
		"unbracketed start": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Initial.ObservedAt = s.Scope.StartedAt.Add(time.Second)
		}, UsageOmissionUnbracketed},
		"closing before cutoff": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Closing.ObservedAt = s.Scope.EndAt.Add(-time.Second)
		}, UsageOmissionUnbracketed},
		"sample outside window": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Samples[0].At = s.Scope.StartedAt.Add(-time.Nanosecond)
		}, UsageOmissionUnbracketed},
		"sample from another incarnation": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Samples[1].RunnerIncarnation = strings.Repeat("7", 64)
		}, UsageOmissionUnbracketed},
		"execution ends before cutoff": {func(c *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			c.Execution.State, c.Execution.EndedAt = StockJobObservedEnded, s.Scope.StartedAt.Add(3*time.Second)
			s.Execution = &c.Execution
		}, UsageOmissionUnbracketed},
		"arrival timestamp alone cannot cover cutoff": {func(c *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			// A response that merely arrived mid-window, without positive
			// execution evidence covering the cutoff, proves nothing about it.
			c.Execution.ObservedAt = s.Scope.StartedAt.Add(3 * time.Second)
			s.Execution = &c.Execution
		}, UsageOmissionUnbracketed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			claim, now := attributionFixtureClaim()
			set := usageFixtureSet(claim, now, MaxJobResourceGap)
			tc.alter(&claim, &set)
			record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
			if record.State != VerifiedUsageUnavailable || record.Reason != tc.want {
				t.Fatalf("unsafe attribution produced %s/%s, want unavailable/%s: %+v", record.State, record.Reason, tc.want, record)
			}
			if record.Measured != nil {
				t.Fatalf("unsafe attribution must never carry numbers: %+v", record.Measured)
			}
		})
	}
}

func TestBuildVerifiedJobUsageKeepsShortUncapturedJobUnavailableNotZero(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	set.Samples = nil
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
	if record.State != VerifiedUsageUnavailable || record.Reason != UsageOmissionJobTooShort || record.Measured != nil {
		t.Fatalf("uncaptured short job must be unavailable, not zero: %+v", record)
	}
}

func TestBuildVerifiedJobUsageSeparatesIndependentlyBoundDisk(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	for i := range set.Samples {
		set.Samples[i].DiskReadBytes = knownResource([]uint64{5, 15, 55}[i])
	}
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
	if record.State != VerifiedUsageVerified || record.Measured == nil || record.Measured.DiskRead == nil ||
		record.Measured.DiskRead.Bytes != 50 || !record.Measured.DiskRead.Coverage.Complete {
		t.Fatalf("independently bound disk usage must be preserved: %+v", record.Measured)
	}
	wire := record.Wire()
	if wire.Measured == nil || wire.Measured.Disk.State != VerifiedUsageVerified || wire.Measured.Disk.ReadBytes != "50" {
		t.Fatalf("disk wire projection must stay separate and positive: %+v", wire.Measured.Disk)
	}
}

func TestDecideCollectionFailsClosedOnDocumentedMatrix(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	decision := DecideCollection(DocumentedMatrix(), claim, set, now)
	if decision.Collect || decision.Reason != UsageOmissionJoinUnsupported {
		t.Fatalf("documented matrix must not authorize live collection: %+v", decision)
	}
}

func TestDecideCollectionCollectsOnlyCoherentFixtureEvidence(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	if decision := DecideCollection(fixtureJoinMatrix(), claim, set, now); !decision.Collect || decision.Reason != UsageOmissionNone {
		t.Fatalf("coherent fixture evidence must be collectable: %+v", decision)
	}
	for name, alter := range map[string]func(*JobAttributionClaim, *VerifiedUsageSampleSet){
		"join still needs coherent evidence": func(c *JobAttributionClaim, _ *VerifiedUsageSampleSet) {
			c.Row.AgentID = -1
			c.Origin.Row = c.Row
		},
		"unbracketed window": func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Initial.ObservedAt = s.Scope.StartedAt.Add(time.Second)
		},
		"reset counters": func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Samples[2].DiskWriteBytes = knownResource(1)
			s.Samples[1].DiskWriteBytes = knownResource(2)
		},
	} {
		t.Run(name, func(t *testing.T) {
			claim, now := attributionFixtureClaim()
			set := usageFixtureSet(claim, now, MaxJobResourceGap)
			alter(&claim, &set)
			if decision := DecideCollection(fixtureJoinMatrix(), claim, set, now); decision.Collect {
				t.Fatalf("unsafe window must not be collectable: %+v", decision)
			}
		})
	}
	// Even with join support, a claim without origin stays unsupported.
	claim, now = attributionFixtureClaim()
	claim.Origin = VerifiedProviderOrigin{}
	if decision := DecideCollection(fixtureJoinMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), now); decision.Collect || decision.Reason != UsageOmissionJoinUnsupported {
		t.Fatalf("origin-free claim must stay unsupported: %+v", decision)
	}
}

func TestVerifiedUsageWireProjection(t *testing.T) {
	claim, now := attributionFixtureClaim()
	verified := BuildVerifiedJobUsage(DocumentedMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), now)
	raw, err := json.Marshal(verified.Wire())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state"] != "verified" || decoded["as_of"] == nil {
		t.Fatalf("wire must project state and as_of: %s", raw)
	}
	if _, ok := decoded["reason"]; ok {
		t.Fatalf("verified wire must not carry a reason: %s", raw)
	}
	measured, ok := decoded["measured"].(map[string]any)
	if !ok {
		t.Fatalf("verified wire must carry measured numbers: %s", raw)
	}
	if measured["samples"] != float64(3) || measured["window_ns"] != "6000000000" {
		t.Fatalf("measured sample facts changed: %s", raw)
	}
	cpu, ok := measured["cpu"].(map[string]any)
	if !ok || cpu["average_millicpu"] != "1000" || cpu["peak_interval_millicpu"] != "1250" ||
		cpu["covered_ns"] != "6000000000" || cpu["complete"] != true {
		t.Fatalf("cpu projection must preserve rates and coverage: %s", raw)
	}
	ram, ok := measured["ram"].(map[string]any)
	if !ok || ram["mean_bytes"] != "33" || ram["sample_peak_bytes"] != "50" || ram["gauge_samples"] != float64(3) {
		t.Fatalf("ram projection must preserve mean and sampled peak: %s", raw)
	}
	disk, ok := measured["disk"].(map[string]any)
	if !ok || disk["state"] != "unavailable" || disk["reason"] != "disk_not_independently_measured" {
		t.Fatalf("disk projection must be explicit, not zero: %s", raw)
	}

	// Unavailable records project no numbers at all; stale records likewise.
	claim.Origin = VerifiedProviderOrigin{}
	unavailable := BuildVerifiedJobUsage(DocumentedMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), now)
	raw, err = json.Marshal(unavailable.Wire())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "measured") {
		t.Fatalf("unavailable wire must not carry measured numbers: %s", raw)
	}
}
