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
	record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, now)
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
		got.RAM == nil || *got.RAM != (ResourceGaugeSummary{3, 50, 50, set.Scope.EndAt, set.Scope.EndAt}) {
		t.Fatalf("unexpected measured summary: %+v", got)
	}
	// Disk was not independently bound: it stays separate and unmeasured,
	// never zero and never derived from host free disk.
	if got.DiskRead != nil || got.DiskWrite != nil || got.DiskUsed != nil {
		t.Fatalf("unbound disk counters must stay absent: %+v", got)
	}
}

func TestVerifiedUsageUsesSupportingObservationsNotEvaluationOrDeclaredTime(t *testing.T) {
	claim, observed := attributionFixtureClaim()
	set := usageFixtureSet(claim, observed, MaxJobResourceGap)
	evaluate := observed.Add(time.Second)
	set.Scope.AsOf = evaluate
	record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, evaluate)
	if record.State != VerifiedUsageVerified || record.Measured == nil ||
		!record.AsOf.Equal(observed) || !record.Measured.Scope.AsOf.Equal(observed) {
		t.Fatalf("expected measurements with the actual source observation, got %+v", record)
	}
}

func TestVerifiedUsageCannotRefreshExpiredClosingObservation(t *testing.T) {
	claim, observed := attributionFixtureClaim()
	set := usageFixtureSet(claim, observed, MaxJobResourceGap)
	expires := set.Closing.ObservedAt.Add(MaxJobResourceGap)
	// Both attribution observations are still recent and the caller can set a
	// new cutoff for evaluation, but the old closing fact has not changed.
	validAt := expires.Add(-time.Nanosecond)
	set.Scope.AsOf = validAt
	if record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, validAt); record.State != VerifiedUsageVerified {
		t.Fatalf("fresh just before expiry: %+v", record)
	}
	set.Scope.AsOf = expires
	record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, expires)
	if record.State != VerifiedUsageStale || record.Reason != UsageOmissionStaleEvidence ||
		record.Measured != nil || !record.AsOf.Equal(observed) {
		t.Fatalf("expired evidence must not produce measurements or a fresh timestamp: %+v", record)
	}
	if decision := DecideCollection(fixtureJoinMatrix(), claim, set, fixtureHooks(), expires); decision.Collect ||
		decision.Reason != UsageOmissionStaleEvidence {
		t.Fatalf("expired evidence must not enable collection: %+v", decision)
	}
}

func TestVerifiedUsageWithNoSourceTimestampDoesNotInventOne(t *testing.T) {
	now := attributionFixtureStart
	claim := JobAttributionClaim{
		Row: StockRowEvidence{ObservedAt: now.Add(time.Second)}, // future does not count
	}
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, VerifiedUsageSampleSet{}, now)
	wire, err := json.Marshal(record.Wire(DocumentedMatrix()))
	if err != nil || !record.AsOf.IsZero() || !strings.Contains(string(wire), `"as_of":null`) ||
		strings.Contains(string(wire), `"measured"`) {
		t.Fatalf("unknown observation must stay unknown: %s %v", wire, err)
	}
}

func TestBuildVerifiedJobUsageReportsPartialCoverageAccurately(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, 3*time.Second) // the 4s cadence gap loses the last interval
	record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, now)
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
		"stale row sighting before job start": {func(c *JobAttributionClaim, _ *VerifiedUsageSampleSet) {
			c.Row.ObservedAt = c.Execution.StartedAt.Add(-time.Nanosecond)
			c.Origin.Row = c.Row
		}, UsageOmissionStaleEvidence},
		"bracket names another garm row": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Initial.StockInstanceUUID = "88888888-8888-4888-8888-888888888889"
		}, UsageOmissionEvidenceMismatch},
		"bracket runner identity differs": {func(_ *JobAttributionClaim, s *VerifiedUsageSampleSet) {
			s.Initial.RunnerAgentID++
		}, UsageOmissionEvidenceMismatch},
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
			record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, now)
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
	record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, now)
	if record.State != VerifiedUsageUnavailable || record.Reason != UsageOmissionJobTooShort || record.Measured != nil {
		t.Fatalf("uncaptured short job must be unavailable, not zero: %+v", record)
	}
}

func TestBuildVerifiedJobUsageKeepsDocumentedMatrixUnavailableEvenWhenFieldsMatch(t *testing.T) {
	// Every field is coherent and would verify claim-level consistency, but
	// the documented matrix still has no source that proves the join. The
	// measured numbers must not come back on field coherence alone.
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	if _, err := VerifyJobAttribution(fixtureJoinMatrix(), claim, now); err != nil {
		t.Fatalf("fixture fields must be claim-coherent for this regression: %v", err)
	}
	if _, err := VerifyJobAttribution(DocumentedMatrix(), claim, now); err != ErrAttributionUnsupported {
		t.Fatalf("the documented matrix must deny a direct verified attribution: %v", err)
	}
	record := BuildVerifiedJobUsage(DocumentedMatrix(), claim, set, now)
	if record.State != VerifiedUsageUnavailable || record.Reason != UsageOmissionJoinUnsupported || record.Measured != nil {
		t.Fatalf("documented matrix must stay unavailable even when fields match: %+v", record)
	}
	if decision := DecideCollection(DocumentedMatrix(), claim, set, fixtureHooks(), now); decision.Collect {
		t.Fatalf("documented matrix must not authorize collection with hooks connected: %+v", decision)
	}
}

func TestBuildVerifiedJobUsageSeparatesIndependentlyBoundDisk(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	for i := range set.Samples {
		set.Samples[i].DiskReadBytes = knownResource([]uint64{5, 15, 55}[i])
	}
	record := BuildVerifiedJobUsage(fixtureFullMatrix(), claim, set, now)
	if record.State != VerifiedUsageVerified || record.Measured == nil || record.Measured.DiskRead == nil ||
		record.Measured.DiskRead.Bytes != 50 || !record.Measured.DiskRead.Coverage.Complete {
		t.Fatalf("independently bound disk usage must be preserved: %+v", record.Measured)
	}
	wire := record.Wire(fixtureFullMatrix())
	if wire.Measured == nil || wire.Measured.Disk.State != VerifiedUsageVerified || wire.Measured.Disk.Version != DiskWireVersion ||
		wire.Measured.Disk.Read == nil || wire.Measured.Disk.Read.Bytes != "50" ||
		wire.Measured.Disk.Read.CoveredNanoseconds != "6000000000" || !wire.Measured.Disk.Read.Complete ||
		wire.Measured.Disk.Write != nil || wire.Measured.Disk.Used != nil {
		t.Fatalf("disk wire projection must stay separate and positive: %+v", wire.Measured.Disk)
	}
	// No documented source supports per-instance disk counters: the same
	// record projected under the stock matrix hides them with a reason
	// instead of showing unsupported numbers.
	stockWire := record.Wire(DocumentedMatrix())
	if stockWire.Measured == nil || stockWire.Measured.Disk.State != VerifiedUsageUnavailable ||
		stockWire.Measured.Disk.Reason != UsageOmissionDiskUnsupported || stockWire.Measured.Disk.Read != nil {
		t.Fatalf("disk must hide without a documented source, not show zeros: %+v", stockWire.Measured.Disk)
	}
}

// TestVerifiedUsageWireProjectsDiskCountersAndGaugeSeparately covers the
// versioned disk wire shape: read and write carry independent totals and
// coverage, a measured zero stays a measured zero, and the used-space gauge
// is a sampled peak with its own sample count and explicit peak time, never
// an interval coverage.
func TestVerifiedUsageWireProjectsDiskCountersAndGaugeSeparately(t *testing.T) {
	claim, now := attributionFixtureClaim()
	matrix := fixtureFullMatrix()
	build := func(bind func(samples []JobResourceSample)) Wire {
		set := usageFixtureSet(claim, now, MaxJobResourceGap)
		bind(set.Samples)
		record := BuildVerifiedJobUsage(matrix, claim, set, now)
		if record.State != VerifiedUsageVerified || record.Measured == nil {
			t.Fatalf("coherent disk evidence must verify: %+v", record)
		}
		wire := record.Wire(matrix)
		if wire.Measured == nil || wire.Measured.Disk.State != VerifiedUsageVerified || wire.Measured.Disk.Version != DiskWireVersion {
			t.Fatalf("measured disk must carry the versioned shape: %+v", wire.Measured)
		}
		return wire
	}
	start, end := claim.Execution.StartedAt.UTC(), claim.Execution.StartedAt.UTC().Add(6*time.Second)

	// Read only: the read counter carries its own full coverage; write and
	// the gauge stay absent rather than inheriting anything.
	readOnly := build(func(samples []JobResourceSample) {
		for i := range samples {
			samples[i].DiskReadBytes = knownResource([]uint64{5, 15, 55}[i])
		}
	}).Measured.Disk
	if readOnly.Read == nil || readOnly.Read.Bytes != "50" || readOnly.Read.CoveredNanoseconds != "6000000000" || !readOnly.Read.Complete ||
		readOnly.Write != nil || readOnly.Used != nil {
		t.Fatalf("read-only disk projection wrong: %+v", readOnly)
	}

	// Write only with a shorter covered window: coverage belongs to the
	// write counter alone and is honestly incomplete.
	writeOnly := build(func(samples []JobResourceSample) {
		samples[1].DiskWriteBytes = knownResource(6)
		samples[2].DiskWriteBytes = knownResource(9)
	}).Measured.Disk
	if writeOnly.Write == nil || writeOnly.Write.Bytes != "3" || writeOnly.Write.CoveredNanoseconds != "4000000000" ||
		writeOnly.Write.Complete || writeOnly.Read != nil || writeOnly.Used != nil {
		t.Fatalf("write-only disk projection wrong: %+v", writeOnly)
	}

	// A counter that was measured and did not move is a measured zero with
	// full coverage, not a missing metric.
	zeroWrite := build(func(samples []JobResourceSample) {
		for i := range samples {
			samples[i].DiskWriteBytes = knownResource(7)
		}
	}).Measured.Disk
	if zeroWrite.Write == nil || zeroWrite.Write.Bytes != "0" || !zeroWrite.Write.Complete || zeroWrite.Read != nil {
		t.Fatalf("measured zero write must be preserved: %+v", zeroWrite)
	}

	// Gauge only: a sampled peak with its own sample count, the explicitly
	// tracked time the peak was reached and the last sample time. It never
	// implies interval coverage and never borrows counter semantics.
	gaugeOnly := build(func(samples []JobResourceSample) {
		for i := range samples {
			samples[i].DiskUsedBytes = knownResource([]uint64{100, 9, 20}[i])
		}
	}).Measured.Disk
	if gaugeOnly.Used == nil || gaugeOnly.Used.SamplePeakBytes != "100" || gaugeOnly.Used.GaugeSamples != 3 ||
		!gaugeOnly.Used.PeakAt.Equal(start) ||
		!gaugeOnly.Used.LatestSampleAt.Equal(end) ||
		gaugeOnly.Read != nil || gaugeOnly.Write != nil {
		t.Fatalf("gauge-only disk projection wrong: %+v", gaugeOnly)
	}

	// Differing windows: read covers the whole job while write covers only
	// the tail; neither counter borrows the other's coverage.
	differing := build(func(samples []JobResourceSample) {
		for i := range samples {
			samples[i].DiskReadBytes = knownResource([]uint64{5, 15, 55}[i])
		}
		samples[1].DiskWriteBytes = knownResource(6)
		samples[2].DiskWriteBytes = knownResource(9)
	}).Measured.Disk
	if differing.Read == nil || differing.Read.CoveredNanoseconds != "6000000000" || !differing.Read.Complete ||
		differing.Write == nil || differing.Write.CoveredNanoseconds != "4000000000" || differing.Write.Complete {
		t.Fatalf("differing read/write windows must stay independent: %+v", differing)
	}

	// The wire JSON uses the versioned field names and omits absent metrics.
	raw, err := json.Marshal(gaugeOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"version":"disk-measured/v2"`, `"used":{"sample_peak_bytes":"100","gauge_samples":3,"peak_at":"`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("disk wire JSON missing %s: %s", fragment, raw)
		}
	}
	for _, fragment := range []string{`"read"`, `"write"`, "covered_ns"} {
		if strings.Contains(string(raw), fragment) {
			t.Fatalf("gauge-only disk wire must omit %s: %s", fragment, raw)
		}
	}
}

type fixtureProducer struct{ class AttributionEvidenceClass }

func TestVerifiedUsageWireOmitsInvalidDiskMeasurements(t *testing.T) {
	claim, now := attributionFixtureClaim()
	matrix := fixtureFullMatrix()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	for i := range set.Samples {
		set.Samples[i].DiskReadBytes = knownResource(uint64(i))
		set.Samples[i].DiskUsedBytes = knownResource(uint64(100 - i))
	}
	record := BuildVerifiedJobUsage(matrix, claim, set, now)
	if record.Measured == nil || record.Measured.DiskRead == nil || record.Measured.DiskUsed == nil {
		t.Fatal("positive fixture did not yield disk measurements")
	}
	record.Measured.DiskRead.Coverage = ResourceCoverage{}
	record.Measured.DiskUsed.PeakAt = record.Measured.DiskUsed.LatestAt.Add(time.Nanosecond)
	wire := record.Wire(matrix)
	if disk := wire.Measured.Disk; disk.State != VerifiedUsageUnavailable || disk.Read != nil || disk.Used != nil {
		t.Fatalf("invalid counter and gauge were projected: %+v", disk)
	}
}

func (p fixtureProducer) EvidenceClass() AttributionEvidenceClass { return p.class }

type fixtureHistory struct{ revision int64 }

func (h fixtureHistory) RetainedRevision() int64 { return h.revision }

func fixtureHooks() CollectionHooks {
	return CollectionHooks{
		Producer: fixtureProducer{class: "fixture_verified_origin"},
		History:  fixtureHistory{revision: 1},
	}
}

func TestDecideCollectionFailsClosedOnDocumentedMatrix(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	decision := DecideCollection(DocumentedMatrix(), claim, set, fixtureHooks(), now)
	if decision.Collect || decision.Reason != UsageOmissionJoinUnsupported {
		t.Fatalf("documented matrix must not authorize live collection: %+v", decision)
	}
}

func TestDecideCollectionRequiresSupportedProducerAndRetainedHistory(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	matrix := fixtureJoinMatrix()
	for name, hooks := range map[string]CollectionHooks{
		"no hooks":                   {},
		"producer only":              {Producer: fixtureHooks().Producer},
		"history only":               {History: fixtureHooks().History},
		"unsupported producer class": {Producer: fixtureProducer{class: EvidenceIncusRuntimeHealth}, History: fixtureHooks().History},
		"stale retained revision":    {Producer: fixtureHooks().Producer, History: fixtureHistory{revision: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			decision := DecideCollection(matrix, claim, set, hooks, now)
			if decision.Collect || decision.Reason != UsageOmissionCollectionUnavailable {
				t.Fatalf("collection without supported hooks must stay unavailable: %+v", decision)
			}
		})
	}
	if decision := DecideCollection(matrix, claim, set, fixtureHooks(), now); !decision.Collect || decision.Reason != UsageOmissionNone {
		t.Fatalf("supported hooks plus coherent evidence must collect: %+v", decision)
	}
}

func TestDecideCollectionCollectsOnlyCoherentFixtureEvidence(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	if decision := DecideCollection(fixtureJoinMatrix(), claim, set, fixtureHooks(), now); !decision.Collect || decision.Reason != UsageOmissionNone {
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
			if decision := DecideCollection(fixtureJoinMatrix(), claim, set, fixtureHooks(), now); decision.Collect {
				t.Fatalf("unsafe window must not be collectable: %+v", decision)
			}
		})
	}
	// Even with join support, a claim without origin stays unsupported.
	claim, now = attributionFixtureClaim()
	claim.Origin = VerifiedProviderOrigin{}
	if decision := DecideCollection(fixtureJoinMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), fixtureHooks(), now); decision.Collect || decision.Reason != UsageOmissionJoinUnsupported {
		t.Fatalf("origin-free claim must stay unsupported: %+v", decision)
	}
}

func TestVerifiedUsageWireProjection(t *testing.T) {
	claim, now := attributionFixtureClaim()
	verified := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), now)
	raw, err := json.Marshal(verified.Wire(fixtureJoinMatrix()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state"] != "verified" || decoded["as_of"] != "2026-01-02T03:04:35Z" {
		t.Fatalf("wire must project state and the source observation as_of: %s", raw)
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
	unavailable := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, usageFixtureSet(claim, now, MaxJobResourceGap), now)
	raw, err = json.Marshal(unavailable.Wire(fixtureJoinMatrix()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "measured") {
		t.Fatalf("unavailable wire must not carry measured numbers: %s", raw)
	}
}
