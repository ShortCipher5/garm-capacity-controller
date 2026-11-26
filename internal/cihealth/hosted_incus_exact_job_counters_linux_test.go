package cihealth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type hostedIncusEpoch struct {
	Project    string    `json:"project"`
	Name       string    `json:"name"`
	UUID       string    `json:"uuid"`
	Generation string    `json:"generation"`
	BootID     string    `json:"boot_id"`
	PID        uint64    `json:"pid"`
	StartTicks uint64    `json:"start_ticks"`
	StartedAt  time.Time `json:"started_at"`
}
type hostedIncusCounter struct {
	hostedIncusEpoch
	ObservedAt time.Time `json:"observed_at"`
	CPUNS      uint64    `json:"cpu_ns"`
	RAMBytes   uint64    `json:"ram_bytes"`
}

func decodeHostedIncusCounters(t *testing.T, raw []byte) [2]hostedIncusCounter {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var rows []hostedIncusCounter
	if err := d.Decode(&rows); err != nil || len(rows) != 2 || d.Decode(new(any)) != io.EOF {
		t.Fatal("expected exactly two disposable guest counter readings")
	}
	b, a := rows[0], rows[1]
	if b.hostedIncusEpoch != a.hostedIncusEpoch || !strings.HasPrefix(b.Name, "garm-ci-") ||
		!stockJobLocator(b.Project) || !stockJobLocator(b.Name) || !stockJobUUID(b.UUID) ||
		!stockJobUUID(b.Generation) || !stockJobUUID(b.BootID) || b.PID <= 1 ||
		b.PID > 1<<31-1 || b.StartTicks == 0 || !validTime(b.StartedAt) ||
		!validTime(b.ObservedAt) || !validTime(a.ObservedAt) || b.StartedAt.After(b.ObservedAt) ||
		!a.ObservedAt.After(b.ObservedAt) || a.ObservedAt.Sub(b.ObservedAt) > 60*time.Second ||
		b.CPUNS == 0 || a.CPUNS <= b.CPUNS || b.RAMBytes == 0 || a.RAMBytes == 0 ||
		a.CPUNS >= 1<<63 || a.CPUNS-b.CPUNS > 32*uint64(a.ObservedAt.Sub(b.ObservedAt)) || b.RAMBytes >= 1<<63 || a.RAMBytes >= 1<<63 {
		t.Fatal("guest epoch or real counter interval invalid")
	}
	return [2]hostedIncusCounter{b, a}
}

// The hosted collector samples one actual disposable guest. GitHub assignment,
// GARM row, provider-origin receipt, and peer/member are explicitly synthetic.
// This is an adapter and exact-identity consistency check, not evidence of a
// supported live provider-origin join.
func TestHostedIncusExactJobCounters(t *testing.T) {
	path := os.Getenv("GCC_HOSTED_INCUS_JOB_COUNTERS")
	if path == "" {
		t.Skip("requires the disposable hosted Incus fixture")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("cannot open disposable guest counters")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		t.Fatal("disposable guest counters unreadable or too large")
	}
	assertHostedIncusJobReduction(t, decodeHostedIncusCounters(t, raw))
}

func assertHostedIncusJobReduction(t *testing.T, rows [2]hostedIncusCounter) {
	t.Helper()
	before, after := rows[0], rows[1]
	begin, end := before.ObservedAt, after.ObservedAt
	claim, _ := attributionFixtureClaim()
	claim.Job.JobID = 9007199254741011
	claim.Execution.Job = claim.Job
	claim.Execution.StartedAt, claim.Execution.ObservedAt = begin, end
	claim.Row.ObservedAt = end
	r := &claim.Incarnation
	r.Project, r.InstanceName, r.UUID, r.Generation, r.BootID, r.ProcessStartTicks, r.StartedAt =
		before.Project, before.Name, before.UUID, before.Generation, before.BootID, before.StartTicks, before.StartedAt
	claim.Origin = VerifiedProviderOrigin{Row: claim.Row, Incarnation: *r, InstanceCreatedAt: r.StartedAt, AllocationObservedAt: r.StartedAt}

	epoch, err := json.Marshal(before.hostedIncusEpoch)
	if err != nil {
		t.Fatal("cannot encode sampled guest epoch")
	}
	digest := sha256.Sum256(epoch)
	incarnation := hex.EncodeToString(digest[:])
	start := StockJobResourceBinding{
		Job: claim.Job, RunnerIncarnation: incarnation, RunnerAgentID: claim.Row.AgentID,
		StockSourceID: claim.Row.SourceIdentity, StockInstanceUUID: claim.Row.RowID, StockPoolUUID: claim.Row.PoolID,
		Runtime: *r, ObservedAt: begin, State: StockRunnerObservedRunning,
	}
	stop := start
	stop.ObservedAt = end
	set := VerifiedUsageSampleSet{
		Scope:   JobResourceScope{Job: claim.Job, RunnerIncarnation: incarnation, StartedAt: begin, EndAt: end, AsOf: end, MaxGap: time.Minute},
		Initial: &start, Closing: &stop, Execution: &claim.Execution,
	}
	for _, row := range rows {
		set.Samples = append(set.Samples, JobResourceSample{
			Job: claim.Job, RunnerIncarnation: incarnation, At: row.ObservedAt,
			CPUTimeNanos: knownResource(row.CPUNS), RAMBytes: knownResource(row.RAMBytes),
		})
	}
	got := BuildVerifiedJobUsage(fixtureJoinMatrix(), claim, set, end)
	wantCPU := (after.CPUNS - before.CPUNS) * 1000 / uint64(end.Sub(begin))
	wantRAM := (before.RAMBytes + after.RAMBytes) / 2
	m := got.Measured
	if got.State != VerifiedUsageVerified || got.Job != claim.Job || m == nil ||
		m.CPU == nil || m.CPU.AverageMilliCPU != wantCPU || m.CPU.PeakIntervalMilliCPU != wantCPU || !m.CPU.Coverage.Complete ||
		m.RAM == nil || m.RAM.SampleMax != max(before.RAMBytes, after.RAMBytes) ||
		m.RAM.Latest != after.RAMBytes || m.RAMMean == nil || m.RAMMean.Bytes != wantRAM || !m.RAMMean.Coverage.Complete ||
		m.DiskRead != nil || m.DiskWrite != nil || m.DiskUsed != nil {
		t.Fatal("real CPU and RAM did not reduce exactly under the synthetic job model")
	}
	assertRefusal := func(matrix AttributionCapabilityMatrix, reason UsageOmissionReason) {
		t.Helper()
		x := BuildVerifiedJobUsage(matrix, claim, set, end)
		if x.State != VerifiedUsageUnavailable || x.Reason != reason || x.Measured != nil {
			t.Fatalf("expected unmeasured usage: state %v, reason %v", x.State, x.Reason)
		}
	}
	assertRefusal(DocumentedMatrix(), UsageOmissionJoinUnsupported)
	set.Samples[1].Job.JobID++ // adjacent exact synthetic job
	assertRefusal(fixtureJoinMatrix(), UsageOmissionUnbracketed)
	set.Samples[1].Job = claim.Job
	stop.Runtime.ProcessStartTicks++ // same name and increasing counters, different epoch
	assertRefusal(fixtureJoinMatrix(), UsageOmissionEvidenceMismatch)
}
