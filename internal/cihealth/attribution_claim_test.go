package cihealth

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

var attributionFixtureStart = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// attributionFixtureClaim builds a fully proven claim under an exact
// enrolled repository scope. The origin is a fixture-only stand-in for the
// independently verified original provider allocation evidence that no
// documented source can yet produce.
func attributionFixtureClaim() (JobAttributionClaim, time.Time) {
	now := attributionFixtureStart.Add(30 * time.Second)
	job := JobKey{Attempt: AttemptKey{Source: "fixture", RepositoryID: 1, WorkflowID: 2, RunID: 3, Attempt: 4}, JobID: 5}
	row := StockRowEvidence{
		SourceIdentity: strings.Repeat("2", 64), ControllerID: "22222222-2222-4222-8222-222222222222",
		RepositoryID: "66666666-6666-4666-8666-666666666666", PoolID: "77777777-7777-4777-8777-777777777777",
		RowID: "88888888-8888-4888-8888-888888888888", ProviderName: "incus",
		OwnershipFingerprint: strings.Repeat("9", 64), AgentID: 9007199254740993, ObservedAt: now.Add(-time.Second),
	}
	incarnation := StockRunnerIncarnation{
		PeerFingerprint: strings.Repeat("3", 64), Member: "member-a", Project: "reviewed-project",
		InstanceName: "runner-one", UUID: "33333333-3333-4333-8333-333333333333",
		Generation: "44444444-4444-4444-8444-444444444444", BootID: "55555555-5555-4555-8555-555555555555",
		ProcessStartTicks: 123, StartedAt: attributionFixtureStart.Add(-time.Minute),
	}
	execution := StockJobExecutionObservation{
		Job: job, RunnerAgentID: row.AgentID, StartedAt: attributionFixtureStart, ObservedAt: now, State: StockJobObservedExecuting,
	}
	claim := JobAttributionClaim{
		Job: job, Enrolled: row.scopeFor(job.Attempt.Source, job.Attempt.RepositoryID), Execution: execution, Row: row, Incarnation: incarnation,
		Origin: VerifiedProviderOrigin{
			Row: row, Incarnation: incarnation, InstanceCreatedAt: incarnation.StartedAt.Add(-time.Second),
			AllocationObservedAt: incarnation.StartedAt,
		},
	}
	return claim, now
}

func TestVerifyJobAttributionAcceptsCoherentProvenClaim(t *testing.T) {
	claim, now := attributionFixtureClaim()
	got, err := VerifyJobAttribution(fixtureJoinMatrix(), claim, now)
	if err != nil {
		t.Fatalf("coherent proven claim rejected: %v", err)
	}
	if got.Job != claim.Job || got.AgentID != claim.Row.AgentID || got.SourceID != claim.Row.SourceIdentity ||
		got.PoolID != claim.Row.PoolID || got.RowID != claim.Row.RowID || got.Incarnation != claim.Incarnation ||
		got.Enrolled != claim.Enrolled ||
		!got.StartedAt.Equal(claim.Execution.StartedAt) || got.ExecutionEnds {
		t.Fatalf("verified attribution does not retain the proven facts: %+v", got)
	}
}

func TestVerifyJobAttributionRejectsClaimWithoutOrigin(t *testing.T) {
	claim, now := attributionFixtureClaim()
	claim.Origin = VerifiedProviderOrigin{}
	if _, err := VerifyJobAttribution(fixtureJoinMatrix(), claim, now); err != ErrAttributionUnsupported {
		t.Fatalf("claim without origin must fail closed: %v", err)
	}
}

func TestVerifyJobAttributionRejectsMismatchedAndMalformedClaims(t *testing.T) {
	cases := map[string]func(*JobAttributionClaim){
		"cross runner agent": func(c *JobAttributionClaim) { c.Execution.RunnerAgentID++ },
		"cross job":          func(c *JobAttributionClaim) { c.Execution.Job.JobID++ },
		"cross attempt":      func(c *JobAttributionClaim) { c.Execution.Job.Attempt.Attempt++ },
		"cross repository":   func(c *JobAttributionClaim) { c.Execution.Job.Attempt.RepositoryID++ },
		"coherent other GitHub repository": func(c *JobAttributionClaim) {
			c.Job.Attempt.RepositoryID++
			c.Execution.Job = c.Job
		},
		"coherent other GitHub source": func(c *JobAttributionClaim) {
			c.Job.Attempt.Source = "other_source"
			c.Execution.Job = c.Job
		},
		"missing GitHub enrollment mapping": func(c *JobAttributionClaim) {
			c.Enrolled.GitHubSource, c.Enrolled.GitHubRepositoryID = "", 0
		},
		"zero enrolled scope": func(c *JobAttributionClaim) {
			c.Enrolled = EnrolledRowScope{}
		},
		"enrolled scope mismatch": func(c *JobAttributionClaim) {
			// Fields still match the row; only the approved enrollment
			// differs. Matching fields cannot substitute for enrollment.
			c.Enrolled.PoolID = "77777777-7777-4777-8777-777777777778"
		},
		"row sighting predates job start": func(c *JobAttributionClaim) {
			c.Row.ObservedAt = c.Execution.StartedAt.Add(-time.Nanosecond)
			c.Origin.Row = c.Row
		},
		"reused instance in origin": func(c *JobAttributionClaim) {
			c.Origin.Incarnation.UUID = "33333333-3333-4333-8333-333333333334"
		},
		"origin for another row": func(c *JobAttributionClaim) {
			c.Origin.Row.RowID = "88888888-8888-4888-8888-888888888889"
		},
		"origin observed before creation": func(c *JobAttributionClaim) {
			c.Origin.AllocationObservedAt = c.Origin.InstanceCreatedAt.Add(-time.Nanosecond)
		},
		"zero origin row":   func(c *JobAttributionClaim) { c.Origin.Row = StockRowEvidence{} },
		"bad source digest": func(c *JobAttributionClaim) { c.Row.SourceIdentity = "not-a-digest" },
		"bad pool uuid":     func(c *JobAttributionClaim) { c.Row.PoolID = "pool" },
		"missing agent":     func(c *JobAttributionClaim) { c.Row.AgentID = 0 },
		"future row sighting": func(c *JobAttributionClaim) {
			c.Row.ObservedAt = c.Row.ObservedAt.Add(time.Hour)
			c.Origin.Row = c.Row
		},
		"future execution sighting": func(c *JobAttributionClaim) { c.Execution.ObservedAt = c.Execution.ObservedAt.Add(time.Hour) },
		"ended without end": func(c *JobAttributionClaim) {
			c.Execution.State = StockJobObservedEnded
		},
		"executing with reported end": func(c *JobAttributionClaim) {
			c.Execution.EndedAt = c.Execution.ObservedAt
		},
		"unrecognized execution state": func(c *JobAttributionClaim) { c.Execution.State = StockJobExecutionState(255) },
		"runtime starts after job": func(c *JobAttributionClaim) {
			c.Incarnation.StartedAt = c.Execution.StartedAt.Add(time.Nanosecond)
			c.Origin.Incarnation = c.Incarnation
		},
		"unknown execution state": func(c *JobAttributionClaim) { c.Execution.State = StockJobExecutionUnknown },
		"bad peer fingerprint":    func(c *JobAttributionClaim) { c.Incarnation.PeerFingerprint = "xyz" },
		"incarnation never started": func(c *JobAttributionClaim) {
			c.Incarnation.StartedAt = time.Time{}
			c.Origin.Incarnation = c.Incarnation
		},
		"instance created after process start": func(c *JobAttributionClaim) {
			c.Origin.InstanceCreatedAt = c.Incarnation.StartedAt.Add(time.Nanosecond)
		},
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			claim, now := attributionFixtureClaim()
			alter(&claim)
			if _, err := VerifyJobAttribution(fixtureJoinMatrix(), claim, now); err != ErrAttributionUnsupported {
				t.Fatalf("unsafe claim must fail closed: %v", err)
			}
		})
	}
}

func TestVerifyJobAttributionRequiresDocumentedIdentitySources(t *testing.T) {
	claim, now := attributionFixtureClaim()
	// A matrix missing the identity facts cannot verify anything, even with a
	// fully proven claim: capability, not coherence, gates verification.
	empty := AttributionCapabilityMatrix{}
	if _, err := VerifyJobAttribution(empty, claim, now); err != ErrAttributionUnsupported {
		t.Fatalf("capability-free matrix must fail closed: %v", err)
	}
	joinOnly := AttributionCapabilityMatrix{Sources: []AttributionCapability{{Class: "fixture_verified_origin", Proves: []AttributionFact{FactProviderOriginJoin}}}}
	if _, err := VerifyJobAttribution(joinOnly, claim, now); err != ErrAttributionUnsupported {
		t.Fatalf("join evidence alone cannot replace identity facts: %v", err)
	}
}

// Even an otherwise verified original allocation does not establish the boot
// identity of the process whose counters cover the job window.
func TestJobMetricsNeedIndependentProcessEpoch(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	matrix := DocumentedMatrix()
	matrix.Sources = append(matrix.Sources, AttributionCapability{
		Class: "fixture_allocation_only", Proves: []AttributionFact{FactProviderOriginJoin},
	})
	if _, err := VerifyJobAttribution(matrix, claim, now); err != ErrAttributionUnsupported {
		t.Fatalf("allocation plus matching PID/start must leave the process epoch unproven: %v", err)
	}
	record := BuildVerifiedJobUsage(matrix, claim, set, now)
	if record.State != VerifiedUsageUnavailable || record.Measured != nil || record.Wire(matrix).Measured != nil {
		t.Fatalf("unproven process epoch yielded measurements: %+v", record)
	}
	decision := DecideCollection(matrix, claim, set, CollectionHooks{
		Producer: fixtureProducer{class: "fixture_allocation_only"}, History: fixtureHistory{revision: 1},
	}, now)
	if decision.Collect {
		t.Fatal("unproven process epoch authorized collection")
	}
}

func TestJobMetricsRespectBothExecutionReceipts(t *testing.T) {
	for _, tc := range []struct {
		name                                               string
		claimEnd, sampleEnd, claimObserved, sampleObserved time.Duration
		want                                               UsageOmissionReason
	}{
		{"cutoff before later independent completion", 8 * time.Second, 0, 20 * time.Second, 6 * time.Second, UsageOmissionNone},
		{"later sample confirms completion", 0, 8 * time.Second, 7 * time.Second, 20 * time.Second, UsageOmissionNone},
		{"matching terminal receipts", 8 * time.Second, 8 * time.Second, 12 * time.Second, 13 * time.Second, UsageOmissionNone},
		{"claim ended before sampled cutoff", 5 * time.Second, 0, 10 * time.Second, 6 * time.Second, UsageOmissionUnbracketed},
		{"terminal receipts disagree", 8 * time.Second, 9 * time.Second, 20 * time.Second, 20 * time.Second, UsageOmissionEvidenceMismatch},
		{"sample still executing after claim completion", 8 * time.Second, 0, 20 * time.Second, 9 * time.Second, UsageOmissionEvidenceMismatch},
		{"claim still executing after sample completion", 0, 8 * time.Second, 9 * time.Second, 20 * time.Second, UsageOmissionEvidenceMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim, now := attributionFixtureClaim()
			set := usageFixtureSet(claim, now, MaxJobResourceGap)
			start := set.Scope.StartedAt
			claim.Execution.ObservedAt = start.Add(tc.claimObserved)
			set.Execution.ObservedAt = start.Add(tc.sampleObserved)
			if tc.claimEnd > 0 {
				claim.Execution.State = StockJobObservedEnded
				claim.Execution.EndedAt = start.Add(tc.claimEnd)
			}
			if tc.sampleEnd > 0 {
				set.Execution.State = StockJobObservedEnded
				set.Execution.EndedAt = start.Add(tc.sampleEnd)
			}
			record := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, now)
			decision := DecideCollection(fixtureJoinMatrix(), claim, set, fixtureHooks(), now)
			if tc.want == UsageOmissionNone {
				if record.Measured == nil || record.State != VerifiedUsageVerified || !decision.Collect {
					t.Fatalf("consistent receipts withheld: %+v %+v", record, decision)
				}
			} else if record.Reason != tc.want || record.Measured != nil || decision.Collect || decision.Reason != tc.want {
				t.Fatalf("contradictory receipts yielded %+v, %+v", record, decision)
			}
		})
	}
}

func TestJobUsageGatesEachMetricOnItsSourceCapability(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	for i := range set.Samples {
		set.Samples[i].DiskReadBytes = knownResource(uint64(i))
	}
	unrestricted := BuildVerifiedJobUsage(fixtureFullMatrix(), claim, set, now)
	for _, tc := range []struct {
		name     string
		cpu, ram bool
	}{
		{"CPU only", true, false}, {"RAM only", false, true}, {"no qualified metrics", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := fixtureJoinMatrix()
			for i := range matrix.Sources {
				source := &matrix.Sources[i]
				source.Proves = slices.DeleteFunc(source.Proves, func(f AttributionFact) bool {
					return (!tc.cpu && f == FactCPUCounters) || (!tc.ram && f == FactRAMCounters)
				})
			}
			record := BuildVerifiedJobUsage(matrix, claim, set, now)
			for _, wire := range []Wire{record.Wire(matrix), unrestricted.Wire(matrix)} {
				if !tc.cpu && !tc.ram {
					if record.Measured != nil || wire.Measured != nil || wire.State != VerifiedUsageUnavailable || wire.Reason != UsageOmissionDiskUnsupported {
						t.Fatalf("unsupported inputs produced measurements: %+v", wire)
					}
					continue
				}
				if record.Measured == nil || (record.Measured.CPU != nil) != tc.cpu || (record.Measured.RAM != nil) != tc.ram ||
					(record.Measured.RAMMean != nil) != tc.ram || record.Measured.DiskRead != nil ||
					wire.State != VerifiedUsagePartial || wire.Measured == nil || (wire.Measured.CPU != nil) != tc.cpu || (wire.Measured.RAM != nil) != tc.ram ||
					wire.Measured.Disk.State != VerifiedUsageUnavailable || wire.Measured.Disk.Reason != UsageOmissionDiskUnsupported {
					t.Fatalf("metric projection mismatch: %+v", wire)
				}
			}
			if !set.Samples[1].RAMBytes.Known || !set.Samples[1].CPUTimeNanos.Known || !set.Samples[1].DiskReadBytes.Known {
				t.Fatal("capability filtering modified caller samples")
			}
			if !tc.cpu && !tc.ram {
				noDisk := usageFixtureSet(claim, now, MaxJobResourceGap)
				got := BuildVerifiedJobUsage(matrix, claim, noDisk, now)
				if got.State != VerifiedUsageUnavailable || got.Reason != UsageOmissionCollectionUnavailable || got.Measured != nil {
					t.Fatalf("unqualified CPU and RAM were retained: %+v", got)
				}
				if DecideCollection(matrix, claim, noDisk, fixtureHooks(), now).Collect {
					t.Fatal("empty capability set allowed collection")
				}
			}
		})
	}
}

func TestJobMetricsRAMGaugeOnlyHasNoIntervalWireFields(t *testing.T) {
	claim, now := attributionFixtureClaim()
	set := usageFixtureSet(claim, now, MaxJobResourceGap)
	set.Samples[0].RAMBytes = ResourceValue{}
	set.Samples[2].RAMBytes = ResourceValue{}
	matrix := fixtureJoinMatrix()
	wireRAM := func() map[string]json.RawMessage {
		t.Helper()
		r := BuildVerifiedJobUsage(matrix, claim, set, now).Wire(matrix)
		if r.Measured == nil || r.Measured.RAM == nil {
			t.Fatal("RAM sample missing")
		}
		raw, err := json.Marshal(r.Measured.RAM)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil {
			t.Fatal("invalid RAM wire", err)
		}
		return fields
	}
	fields := wireRAM()
	if len(fields) != 2 || string(fields["sample_peak_bytes"]) != `"30"` || string(fields["gauge_samples"]) != "1" {
		t.Fatalf("gauge advertised unsupported fields: %s", fields)
	}
	set.Samples[2].RAMBytes = knownResource(40)
	fields = wireRAM()
	if string(fields["covered_ns"]) != `"4000000000"` || string(fields["complete"]) != "false" || string(fields["mean_bytes"]) != `"35"` {
		t.Fatalf("partial mean lost its interval fields: %s", fields)
	}
}
