package cihealth

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func workflowResourceFixture(t *testing.T) WorkflowResourceInput {
	shape := JobSizingShape{CPUMilli: 2000, RAMBytes: 4 << 30}
	c := advisoryCohortFixture(t, 21, shape, 500, 1<<30, false, 9*24*time.Hour, 9*time.Second)
	key := WorkflowResourceKey{Scope: c.Scope, DefinitionDigest: strings.Repeat("a", 64), JobDefinition: "build-1", Runner: sizingTestIdentity}
	in := WorkflowResourceInput{Key: key, Shape: shape, Window: sizingReviewWindowFixture(c), Coverage: TrendCoverageComplete, Reviewed: true, Capabilities: fixtureJoinMatrix()}
	in.Capabilities.Sources = append(in.Capabilities.Sources, AttributionCapability{Class: "fixture_exclusive", Proves: []AttributionFact{FactExclusiveJobWindow}})
	for _, s := range c.Samples {
		m := s.Resources
		in.Rows = append(in.Rows, WorkflowResourceRow{Key: key, Shape: shape, Completion: s.Completion, Outcome: s.Outcome, RiskKnown: true, Usage: VerifiedJobUsageRecord{Job: m.Scope.Job, AsOf: m.Scope.AsOf, State: VerifiedUsagePartial, Measured: &m}})
	}
	return in
}
func TestWorkflowResourceHistoryExactJobUpDownAndOrdering(t *testing.T) {
	in := workflowResourceFixture(t)
	r := in.Rows[len(in.Rows)-1].Usage.Measured
	r.CPU.AverageMilliCPU, r.CPU.PeakIntervalMilliCPU = 900, 910
	r.RAM.Latest, r.RAM.SampleMax, r.RAMMean.Bytes = 1<<28, 1<<28, 1<<28
	retry := in.Rows[0]
	mr := *retry.Usage.Measured
	mr.Scope.Job.Attempt.Attempt = 2
	mr.Scope.Job.JobID = 9999
	mr.Scope.RunnerIncarnation = "retry-distinct"
	retry.Usage.Measured = &mr
	retry.Usage.Job = mr.Scope.Job
	retry.Completion.Job = mr.Scope.Job
	in.Rows = append(in.Rows, retry)
	ad, h, err := ReviewWorkflowResourceHistory(in)
	if err != nil || h.Baseline != 20 || h.Primary != 21 || h.Retries != 1 || h.CurrentRun != "21" || h.CurrentJob != "120" || h.CPUAverage == nil || h.CPUAverage.Direction != "increase" || h.CPUPeak.Median != "500" || h.RAMAverage.Direction != "decrease" || h.RAMPeak.Current != "268435456" || h.DiskPeak != nil || ad.RAM.Suggested != nil {
		t.Fatalf("unexpected: %+v %+v %v", h, ad, err)
	}
	slices.Reverse(in.Rows)
	_, again, err := ReviewWorkflowResourceHistory(in)
	if err != nil || !reflect.DeepEqual(h, again) {
		t.Fatal("order-dependent baseline", again, err)
	}
}
func TestWorkflowResourceHistorySuppressesWeakEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mut    func(*WorkflowResourceInput)
		reject bool
	}{
		{"changed profile", func(i *WorkflowResourceInput) { i.Rows[0].Key.Runner.Profile = "different" }, true},
		{"changed revision", func(i *WorkflowResourceInput) { i.Rows[0].Key.DefinitionDigest = strings.Repeat("b", 64) }, true},
		{"changed matrix", func(i *WorkflowResourceInput) { i.Rows[0].Key.Runner.Matrix = "other" }, true},
		{"changed job", func(i *WorkflowResourceInput) { i.Rows[0].Key.JobDefinition = "other" }, true},
		{"wrong exact job", func(i *WorkflowResourceInput) { i.Rows[0].Usage.Job.JobID = 9999 }, true},
		{"duplicate", func(i *WorkflowResourceInput) { i.Rows = append(i.Rows, i.Rows[0]) }, true},
		{"partial enumeration", func(i *WorkflowResourceInput) { i.Coverage = TrendCoveragePartial }, false},
		{"missing record", func(i *WorkflowResourceInput) { i.Rows[0].Usage.Measured = nil }, false},
		{"unqualified source", func(i *WorkflowResourceInput) { i.Capabilities = DocumentedMatrix() }, false},
		{"stale", func(i *WorkflowResourceInput) { i.Window.AsOf = i.Window.AsOf.Add(time.Hour) }, false},
		{"thin prior history", func(i *WorkflowResourceInput) { i.Rows = i.Rows[1:] }, false},
		{"oom", func(i *WorkflowResourceInput) { i.Rows[3].Risks = JobSizingOOM }, false},
		{"unreviewed", func(i *WorkflowResourceInput) { i.Reviewed = false }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := workflowResourceFixture(t)
			tt.mut(&in)
			_, h, err := ReviewWorkflowResourceHistory(in)
			if (err != nil) != tt.reject || h.Baseline != 0 || h.CPUPeak != nil || h.RAMPeak != nil {
				t.Fatal(h, err)
			}
		})
	}
}
func TestWorkflowResourceHistoryMissingIsNotZero(t *testing.T) {
	in := workflowResourceFixture(t)
	in.Rows[0].Usage.Measured.CPU = nil
	latest := in.Rows[20].Usage.Measured
	latest.RAM.Latest, latest.RAM.SampleMax, latest.RAMMean.Bytes = 0, 0, 0
	_, h, err := ReviewWorkflowResourceHistory(in)
	if err != nil || h.CPUPeak != nil || h.CPUAverage != nil || h.RAMPeak == nil || h.RAMPeak.Current != "0" || h.RAMPeak.Direction != "decrease" {
		t.Fatal(h, err)
	}
}
func TestWorkflowResourceSignalRequiresAbsoluteRelativeAndHistoricalBand(t *testing.T) {
	tests := []struct {
		baseline   []uint64
		cur, floor uint64
		want       string
	}{
		{[]uint64{0, 0, 0}, 0, 50, "within_baseline"},
		{[]uint64{0, 0, 0}, 49, 50, "within_baseline"},
		{[]uint64{0, 0, 0}, 50, 50, "increase"},
		{[]uint64{1000, 1000, 1000}, 1249, 50, "within_baseline"},
		{[]uint64{10, 1000, 1600}, 1300, 50, "within_baseline"},
		{[]uint64{math.MaxUint64 - 1, math.MaxUint64}, math.MaxUint64, 1, "within_baseline"},
	}
	for _, tt := range tests {
		if c := compareResourceValues(tt.baseline, tt.cur, tt.floor); c.Direction != tt.want {
			t.Fatal(c, tt)
		}
	}
}
