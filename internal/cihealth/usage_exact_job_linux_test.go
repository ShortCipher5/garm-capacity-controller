package cihealth

import (
	"errors"
	"testing"
	"time"
)

// This exercises the production history/sidecar/measurement code with
// coherent synthetic source proofs. These jobs can share a display label:
// the source projection removes labels before either store sees them.
// Stock GARM still cannot supply the original provider-allocation proof.
func TestExactJobUsageSurvivesSiblingNameReuseAndStoreReopen(t *testing.T) {
	a, observed := attributionFixtureClaim()
	_, usageRoot := snapshotRoot(t)
	_, historyRoot := snapshotRoot(t)
	f := &attachmentFixture{at: observed, in: UsageAttachmentInput{
		Enabled: true, Root: usageRoot, History: snapshotOpenMode(t, historyRoot, true, true),
	}}
	f.open(t, true)
	a.Job.JobID = 9007199254740992
	a.Execution.Job = a.Job
	b := a
	b.Job.JobID++
	b.Execution.Job = b.Job
	b.Row.RowID = "88888888-8888-4888-8888-888888888889"
	b.Row.AgentID++
	b.Execution.RunnerAgentID = b.Row.AgentID
	// Retain the displayed instance name, but give the other allocation an
	// independently identified runtime. Neither name is an identity.
	b.Incarnation.UUID = "33333333-3333-4333-8333-333333333334"
	b.Incarnation.Generation = "44444444-4444-4444-8444-444444444445"
	b.Incarnation.BootID = "55555555-5555-4555-8555-555555555556"
	b.Incarnation.ProcessStartTicks++
	b.Origin.Row, b.Origin.Incarnation = b.Row, b.Incarnation

	at := f.clock()
	first := usageFixtureSet(a, at, MaxJobResourceGap)
	second := usageFixtureSet(b, at, 3*time.Second)
	second.Scope.RunnerIncarnation = "2222222222222222222222222222222222222222222222222222222222222222"
	second.Initial.RunnerIncarnation, second.Closing.RunnerIncarnation = second.Scope.RunnerIncarnation, second.Scope.RunnerIncarnation
	for i := range second.Samples {
		second.Samples[i].RunnerIncarnation = second.Scope.RunnerIncarnation
		second.Samples[i].CPUTimeNanos = knownResource([]uint64{20e9, 24e9, 40e9}[i])
		second.Samples[i].RAMBytes = knownResource([]uint64{100, 140, 180}[i])
	}

	ctx := t.Context()
	h := f.in.History
	scope := historyScope(a.Job.Attempt)
	cursor, err := h.Cursor(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	row := snapAttempt(scope, a.Job.Attempt.RunID, a.Job.Attempt.Attempt, at)
	clock := Clock{Phase: Completed, StartedAt: first.Scope.StartedAt, CompletedAt: first.Scope.EndAt, ObservedAt: at}
	row.Jobs = []JobFacts{{Key: b.Job, Clock: clock}, {Key: a.Job, Clock: clock}}
	if err := h.Apply(ctx, scope, cursor, HistoryCursor{Revision: cursor.Revision + 1}, []AttemptFacts{row}, at); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetEnrolled(ctx, usageScopeOf(a.Job), true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Attach(ctx, a, first); err != nil {
		t.Fatalf("attach intended job: %v", err)
	}
	assertAbsent := func(key JobKey) {
		t.Helper()
		got, found, err := f.store.Read(ctx, key)
		if err != nil || found || got.Evidence.Measured != nil {
			t.Fatalf("sibling without evidence inherited a measurement: %+v, %v, %v", got, found, err)
		}
	}
	assertAbsent(b.Job)

	for _, tc := range []struct {
		name string
		edit func(*VerifiedUsageSampleSet)
	}{
		{"sibling samples", func(s *VerifiedUsageSampleSet) { s.Samples[1] = first.Samples[1] }},
		{"prior sample incarnation", func(s *VerifiedUsageSampleSet) {
			s.Samples[1].RunnerIncarnation = first.Scope.RunnerIncarnation
		}},
		{"changed closing process epoch", func(s *VerifiedUsageSampleSet) { s.Closing.Runtime.ProcessStartTicks++ }},
		{"reused closing runtime name", func(s *VerifiedUsageSampleSet) { s.Closing.Runtime = a.Incarnation }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := second
			closing := *second.Closing
			bad.Closing = &closing
			bad.Samples = append([]JobResourceSample(nil), second.Samples...)
			tc.edit(&bad)
			if result := BuildVerifiedJobUsage(fixtureFullMatrix(), b, bad, at); result.State != VerifiedUsageUnavailable || result.Measured != nil {
				t.Fatalf("invalid identity published a measurement: %+v", result)
			}
			if err := f.store.Attach(ctx, b, bad); !errors.Is(err, ErrUsageAttachment) {
				t.Fatalf("invalid identity accepted by sidecar: %v", err)
			}
			assertAbsent(b.Job)
		})
	}
	if err := f.store.Attach(ctx, b, second); err != nil {
		t.Fatalf("attach sibling's own partial measurement: %v", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, false)

	assertMetrics := func(key JobKey, state VerifiedUsageState, cpu, mean, peak, covered string, complete bool) UsageAttachment {
		t.Helper()
		got, found, err := f.store.Read(ctx, key)
		if err != nil || !found || got.Job != key || got.Evidence.State != state || got.Evidence.Measured == nil {
			t.Fatalf("missing or wrong exact-job attachment: %+v, %v, %v", got, found, err)
		}
		m := got.Evidence.Measured
		if m.CPU == nil || m.RAM == nil || m.CPU.AverageMilliCPU != cpu || m.RAM.MeanBytes != mean ||
			m.RAM.SamplePeakBytes != peak || m.CPU.CoveredNanoseconds != covered || m.RAM.CoveredNanoseconds != covered ||
			m.CPU.Complete != complete || m.RAM.Complete != complete {
			t.Fatalf("wrong CPU/RAM values or coverage for %v: CPU=%+v RAM=%+v", key, m.CPU, m.RAM)
		}
		return got
	}
	gotA := assertMetrics(a.Job, VerifiedUsageVerified, "1000", "33", "50", "6000000000", true)
	gotB := assertMetrics(b.Job, VerifiedUsagePartial, "2000", "120", "180", "2000000000", false)
	if gotA.RunnerDigest == gotB.RunnerDigest || gotA.InputDigest == gotB.InputDigest {
		t.Fatal("independent evidence collapsed")
	}

	// The same coherent data must stay unavailable under the documented
	// real-stock capability matrix, even though the synthetic matrix accepts it.
	for _, in := range []struct {
		claim JobAttributionClaim
		set   VerifiedUsageSampleSet
	}{{a, first}, {b, second}} {
		got := BuildVerifiedJobUsage(DocumentedMatrix(), in.claim, in.set, at)
		if got.State != VerifiedUsageUnavailable || got.Reason != UsageOmissionJoinUnsupported || got.Measured != nil {
			t.Fatalf("fixture coherence promoted an unsupported stock join: %+v", got)
		}
	}
}
