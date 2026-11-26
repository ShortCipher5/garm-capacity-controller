package cihealthsource

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

type counterFixture struct {
	now                   time.Time
	reg                   jobRegistration
	w                     exclusiveCounterWindow
	points                []cihealth.JobResourceSample
	epochs                []cihealth.StockRunnerIncarnation
	matrix                cihealth.AttributionCapabilityMatrix
	reason                cihealth.UsageOmissionReason
	result                cihealth.Wire
	opens, reads, commits int
	afterRead             func(int)
	retained              bool
}

func newCounterFixture() *counterFixture {
	start := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	end := start.Add(8 * time.Second)
	job := cihealth.JobKey{Attempt: cihealth.AttemptKey{Source: "fixture", RepositoryID: repoID, WorkflowID: workflowID, RunID: runID, Attempt: 2}, JobID: jobID}
	scope := cihealth.EnrolledRowScope{GitHubSource: job.Attempt.Source, GitHubRepositoryID: repoID, SourceIdentity: strings.Repeat("1", 64),
		ControllerID: "11111111-1111-4111-8111-111111111111", RepositoryID: "22222222-2222-4222-8222-222222222222",
		PoolID: "33333333-3333-4333-8333-333333333333", ProviderName: "fixture", OwnershipFingerprint: strings.Repeat("2", 64)}
	row := cihealth.StockRowEvidence{SourceIdentity: scope.SourceIdentity, ControllerID: scope.ControllerID, RepositoryID: scope.RepositoryID,
		PoolID: scope.PoolID, ProviderName: scope.ProviderName, OwnershipFingerprint: scope.OwnershipFingerprint,
		RowID: "44444444-4444-4444-8444-444444444444", AgentID: 9007199254741011, ObservedAt: end.Add(time.Second)}
	x := cihealth.StockJobExecutionObservation{Job: job, RunnerAgentID: row.AgentID, State: cihealth.StockJobObservedEnded,
		StartedAt: start, EndedAt: end, ObservedAt: end.Add(2 * time.Second)}
	epoch := cihealth.StockRunnerIncarnation{PeerFingerprint: strings.Repeat("3", 64), Member: "fixture", Project: "example", InstanceName: "example",
		UUID: "55555555-5555-4555-8555-555555555555", Generation: "66666666-6666-4666-8666-666666666666",
		BootID: "77777777-7777-4777-8777-777777777777", ProcessStartTicks: 42, StartedAt: start.Add(-time.Minute)}
	binding := cihealth.StockJobResourceBinding{Job: job, RunnerIncarnation: strings.Repeat("4", 64), RunnerAgentID: row.AgentID,
		StockSourceID: scope.SourceIdentity, StockPoolUUID: scope.PoolID, StockInstanceUUID: row.RowID, Runtime: epoch, ObservedAt: start, State: cihealth.StockRunnerObservedRunning}
	f := &counterFixture{now: end.Add(3 * time.Second), retained: true, reg: jobRegistration{x, row, scope, func() bool { return true }}, matrix: cihealth.DocumentedMatrix()}
	f.matrix.Sources = append(f.matrix.Sources, cihealth.AttributionCapability{Class: "fixture", Proves: []cihealth.AttributionFact{
		cihealth.FactProviderOriginJoin, cihealth.FactProcessEpoch, cihealth.FactExclusiveJobWindow, cihealth.FactDiskCounters, cihealth.FactDiskReadBytes, cihealth.FactDiskWriteBytes, cihealth.FactDiskUsedBytes, cihealth.FactCPUCounters, cihealth.FactRAMCounters}})
	f.w = exclusiveCounterWindow{origin: cihealth.VerifiedProviderOrigin{Row: row, Incarnation: epoch, InstanceCreatedAt: epoch.StartedAt, AllocationObservedAt: epoch.StartedAt},
		initial: binding, closing: binding, execution: x, count: 3, maxGap: cihealth.MaxJobResourceGap, current: func() bool { return true }}
	f.w.closing.ObservedAt = end
	point := func(v uint64) cihealth.ResourceValue { return cihealth.ResourceValue{Known: true, Value: v} }
	for i, elapsed := range []time.Duration{0, 3 * time.Second, 8 * time.Second} {
		f.epochs = append(f.epochs, epoch)
		p := cihealth.JobResourceSample{Job: job, RunnerIncarnation: binding.RunnerIncarnation, At: start.Add(elapsed),
			CPUTimeNanos: point(uint64(elapsed)), RAMBytes: point(uint64(10 * i)), DiskUsedBytes: point(uint64(50 - i))}
		if i <= 1 {
			p.DiskReadBytes = point(0)
		}
		if i >= 1 {
			p.DiskWriteBytes = point(uint64(i * 20))
		}
		f.points = append(f.points, p)
	}
	f.w.read = func(ctx context.Context, i int) (cihealth.JobResourceSample, cihealth.StockRunnerIncarnation, error) {
		f.reads++
		if f.afterRead != nil {
			f.afterRead(i)
		}
		return f.points[i], f.epochs[i], nil
	}
	return f
}

func (*counterFixture) EvidenceClass() cihealth.AttributionEvidenceClass { return "fixture" }

func (f *counterFixture) openExclusive(_ context.Context, x cihealth.StockJobExecutionObservation, row cihealth.StockRowEvidence) (exclusiveCounterWindow, cihealth.UsageOmissionReason) {
	f.opens++
	if row != f.reg.row || x != f.reg.execution {
		return exclusiveCounterWindow{}, cihealth.UsageOmissionEvidenceMismatch
	}
	return f.w, f.reason
}
func (f *counterFixture) collect(ctx context.Context) cihealth.UsageOmissionReason {
	attach := func(ctx context.Context, claim cihealth.JobAttributionClaim, set cihealth.VerifiedUsageSampleSet, current func() bool) error {
		if !current() {
			return errors.New("expired")
		}
		f.commits++
		f.result = cihealth.BuildVerifiedJobUsage(f.matrix, claim, set, f.now).Wire(f.matrix)
		return nil
	}
	return collectCounterWindow(ctx, f.matrix, f.reg, f, func() bool { return f.retained }, attach, func() time.Time { return f.now })
}

func TestCollectorReadsIndependentCoverageAndSourceTime(t *testing.T) {
	f := newCounterFixture()
	if reason := f.collect(t.Context()); reason != "" || f.opens != 1 || f.reads != 3 || f.commits != 1 {
		t.Fatalf("collection: %q, reads %d", reason, f.reads)
	}
	m := f.result.Measured
	if f.result.State != cihealth.VerifiedUsageVerified || !f.result.AsOf.Equal(f.reg.execution.ObservedAt) || m == nil || m.Samples != 3 ||
		m.CPU == nil || m.CPU.AverageMilliCPU != "1000" || m.CPU.CoveredNanoseconds != "8000000000" ||
		m.RAM == nil || m.RAM.MeanBytes != "11" || m.RAM.SamplePeakBytes != "20" || m.Disk.Read == nil || m.Disk.Read.Bytes != "0" ||
		m.Disk.Read.CoveredNanoseconds != "3000000000" || m.Disk.Write == nil || m.Disk.Write.Bytes != "20" ||
		m.Disk.Write.CoveredNanoseconds != "5000000000" || m.Disk.Read.Complete || m.Disk.Write.Complete ||
		m.Disk.Used == nil || m.Disk.Used.SamplePeakBytes != "50" || !m.Disk.Used.PeakAt.Equal(f.points[0].At) {
		t.Fatalf("wrong source time, coverage or peak: %+v", f.result)
	}

	f = newCounterFixture()
	f.w.maxGap = 4 * time.Second
	f.matrix.Sources[len(f.matrix.Sources)-1].Proves = []cihealth.AttributionFact{cihealth.FactProviderOriginJoin, cihealth.FactProcessEpoch, cihealth.FactExclusiveJobWindow, cihealth.FactCPUCounters, cihealth.FactRAMCounters}
	if reason := f.collect(t.Context()); reason != "" {
		t.Fatal(reason)
	}
	m = f.result.Measured
	if f.result.State != cihealth.VerifiedUsagePartial || m.CPU.CoveredNanoseconds != "3000000000" || m.RAM.CoveredNanoseconds != "3000000000" ||
		m.Disk.Read != nil || m.Disk.Write != nil || m.Disk.Used != nil || m.Disk.Reason != cihealth.UsageOmissionDiskUnmeasured {
		t.Fatalf("unsupported disk or lost cadence: %+v", m)
	}
}

func TestCollectorOmitsUnqualifiedMetricsAndPreservesObservedZero(t *testing.T) {
	for _, metric := range []cihealth.AttributionFact{cihealth.FactCPUCounters, cihealth.FactRAMCounters, cihealth.FactDiskReadBytes, cihealth.FactDiskWriteBytes, cihealth.FactDiskUsedBytes} {
		t.Run(string(metric), func(t *testing.T) {
			f := newCounterFixture()
			f.matrix.Sources[len(f.matrix.Sources)-1].Proves = []cihealth.AttributionFact{
				cihealth.FactProviderOriginJoin, cihealth.FactProcessEpoch, cihealth.FactExclusiveJobWindow, cihealth.FactDiskCounters, metric,
			}
			// All raw readings are present. Exposing one metric class cannot
			// release the others, even if a different reader supports them.
			for i := range f.points {
				zero := cihealth.ResourceValue{Known: true}
				f.points[i].CPUTimeNanos, f.points[i].RAMBytes = zero, zero
				f.points[i].DiskReadBytes, f.points[i].DiskWriteBytes, f.points[i].DiskUsedBytes = zero, zero, zero
			}
			if reason := f.collect(t.Context()); reason != cihealth.UsageOmissionNone || f.result.Measured == nil {
				t.Fatal("qualified zero omitted", reason)
			}
			m := f.result.Measured
			if (m.CPU != nil) != (metric == cihealth.FactCPUCounters) || (m.RAM != nil) != (metric == cihealth.FactRAMCounters) ||
				(m.Disk.Read != nil) != (metric == cihealth.FactDiskReadBytes) || (m.Disk.Write != nil) != (metric == cihealth.FactDiskWriteBytes) ||
				(m.Disk.Used != nil) != (metric == cihealth.FactDiskUsedBytes) {
				t.Fatal("unqualified metric reached the wire")
			}
			if m.CPU != nil && (m.CPU.AverageMilliCPU != "0" || m.CPU.PeakMilliCPU != "0" || !m.CPU.Complete) ||
				m.RAM != nil && (m.RAM.MeanBytes != "0" || m.RAM.SamplePeakBytes != "0" || !m.RAM.Complete) ||
				m.Disk.Read != nil && m.Disk.Read.Bytes != "0" || m.Disk.Write != nil && m.Disk.Write.Bytes != "0" || m.Disk.Used != nil && m.Disk.Used.SamplePeakBytes != "0" {
				t.Fatal("measured zero or coverage changed")
			}
		})
	}
}

func TestCollectorRefusesBeforeAttach(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*counterFixture)
		reason cihealth.UsageOmissionReason
		reads  int
	}{
		{"stock", func(f *counterFixture) { f.matrix = cihealth.DocumentedMatrix() }, cihealth.UsageOmissionJoinUnsupported, 0},
		{"unqualified producer class", func(f *counterFixture) { f.matrix.Sources[len(f.matrix.Sources)-1].Class = "different-source" }, cihealth.UsageOmissionCollectionUnavailable, 0},
		{"origin and epoch without exclusivity", func(f *counterFixture) {
			f.matrix.Sources[len(f.matrix.Sources)-1].Proves = []cihealth.AttributionFact{cihealth.FactProviderOriginJoin, cihealth.FactProcessEpoch, cihealth.FactCPUCounters}
		}, cihealth.UsageOmissionCollectionUnavailable, 0},
		{"overlap", func(f *counterFixture) { f.reason = cihealth.UsageOmissionOverlap }, cihealth.UsageOmissionOverlap, 0},
		{"different exclusive attempt", func(f *counterFixture) { f.w.execution.Job.Attempt.Attempt++ }, cihealth.UsageOmissionOverlap, 0},
		{"missing allocation", func(f *counterFixture) { f.w.origin = cihealth.VerifiedProviderOrigin{} }, cihealth.UsageOmissionJoinUnsupported, 0},
		{"foreign enrolled pool", func(f *counterFixture) { f.reg.enrollment.PoolID = f.reg.row.RepositoryID }, cihealth.UsageOmissionEvidenceMismatch, 0},
		{"reused closing runtime", func(f *counterFixture) { f.w.closing.Runtime.ProcessStartTicks++ }, cihealth.UsageOmissionEvidenceMismatch, 0},
		{"missing opening observation", func(f *counterFixture) { f.w.initial.ObservedAt = time.Time{} }, cihealth.UsageOmissionUnbracketed, 0},
		{"opening predates cadence", func(f *counterFixture) {
			f.w.maxGap = time.Second
			f.w.initial.ObservedAt = f.reg.execution.StartedAt.Add(-time.Second - time.Nanosecond)
		}, cihealth.UsageOmissionUnbracketed, 0},
		{"late first bracket", func(f *counterFixture) { f.w.initial.ObservedAt = f.points[1].At }, cihealth.UsageOmissionUnbracketed, 0},
		{"too many points", func(f *counterFixture) { f.w.count = completedUsageSampleLimit + 1 }, cihealth.UsageOmissionCollectionUnavailable, 0},
		{"uncaptured short job", func(f *counterFixture) { f.w.count = 0 }, cihealth.UsageOmissionJobTooShort, 0},
		{"expired close", func(f *counterFixture) { f.now = f.w.closing.ObservedAt.Add(cihealth.MaxJobResourceGap) }, cihealth.UsageOmissionStaleEvidence, 0},
		{"foreign source", func(f *counterFixture) { f.points[0].Job.Attempt.Source = "sibling" }, cihealth.UsageOmissionEvidenceMismatch, 1},
		{"wrong repo", func(f *counterFixture) { f.points[0].Job.Attempt.RepositoryID++ }, cihealth.UsageOmissionEvidenceMismatch, 1},
		{"sibling job", func(f *counterFixture) { f.points[1].Job.JobID++ }, cihealth.UsageOmissionEvidenceMismatch, 2},
		{"changed epoch inside brackets", func(f *counterFixture) { f.epochs[1].BootID = f.epochs[1].Generation }, cihealth.UsageOmissionEvidenceMismatch, 2},
		{"replaced generation", func(f *counterFixture) { f.epochs[1].Generation = f.epochs[1].BootID }, cihealth.UsageOmissionEvidenceMismatch, 2},
		{"duplicate point", func(f *counterFixture) { f.points[2] = f.points[1] }, cihealth.UsageOmissionUnbracketed, 3},
		{"counter reset", func(f *counterFixture) { f.points[2].CPUTimeNanos.Value = 1 }, cihealth.UsageOmissionCounterDiscontinuity, 3},
		{"row receipt expired", func(f *counterFixture) {
			f.reg.current = func() bool { return f.reads == 0 }
		}, cihealth.UsageOmissionCollectionUnavailable, 1},
		{"revoked retained history", func(f *counterFixture) { f.afterRead = func(i int) { f.retained = false } }, cihealth.UsageOmissionCollectionUnavailable, 1},
		{"provider expired after last point", func(f *counterFixture) { f.w.current = func() bool { return f.reads < 3 } }, cihealth.UsageOmissionCollectionUnavailable, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCounterFixture()
			tc.edit(f)
			if got := f.collect(t.Context()); got != tc.reason || f.reads != tc.reads || f.commits != 0 {
				t.Fatalf("reason %q reads %d commits %d", got, f.reads, f.commits)
			}
			if tc.name == "stock" && f.opens != 0 {
				t.Fatal("stock attempted a read")
			}
		})
	}
	var collector *completedJobCollector
	if reason := collector.collect(t.Context(), cihealth.HistoryCursor{}, cihealth.JobKey{}, "", 1); reason != cihealth.UsageOmissionJoinUnsupported {
		t.Fatal(reason)
	}
}

func TestCollectorRechecksEverySupportingSourceBeforeNextRead(t *testing.T) {
	for _, source := range []string{"GARM registration", "GitHub execution", "provider close", "exclusive execution"} {
		t.Run(source, func(t *testing.T) {
			f := newCounterFixture()
			end := f.reg.execution.EndedAt
			// Only the selected observation expires at the next read.
			f.w.closing.ObservedAt = end.Add(time.Second)
			switch source {
			case "GARM registration":
				f.reg.row.ObservedAt = end
				f.w.origin.Row = f.reg.row
			case "GitHub execution":
				f.reg.execution.ObservedAt = end
			case "provider close":
				f.w.closing.ObservedAt = end
			case "exclusive execution":
				f.w.execution.ObservedAt = end
			}
			f.now = end.Add(cihealth.MaxJobResourceGap - time.Nanosecond)
			f.afterRead = func(i int) { f.now = end.Add(cihealth.MaxJobResourceGap) }
			if reason := f.collect(t.Context()); reason != cihealth.UsageOmissionStaleEvidence || f.reads != 1 || f.commits != 0 {
				t.Fatalf("expired %s: reason %q, reads %d, attachments %d", source, reason, f.reads, f.commits)
			}
		})
	}
}

func TestCollectorCancellationReachesInFlightRead(t *testing.T) {
	f := newCounterFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	f.w.read = func(ctx context.Context, _ int) (cihealth.JobResourceSample, cihealth.StockRunnerIncarnation, error) {
		close(started)
		<-ctx.Done()
		return cihealth.JobResourceSample{}, cihealth.StockRunnerIncarnation{}, ctx.Err()
	}
	done := make(chan cihealth.UsageOmissionReason, 1)
	go func() { done <- f.collect(ctx) }()
	<-started
	cancel()
	if got := <-done; got != cihealth.UsageOmissionCollectionUnavailable || f.commits != 0 {
		t.Fatal("canceled read attached", got)
	}
}

// The real clients perform only loopback GETs against disposable fixtures.
// The positive GARM report proves registration; the fixture supplies no
// provider allocation, process epoch, or authority to collect counters.
func collectorRegistrationFixture(t *testing.T) (garmapi.ManagedRunningRegistrationScope, string, garmapi.ManagedRunningRegistrationReport, *garmapi.DemandClient) {
	t.Helper()
	const controller = "11111111-1111-4111-8111-111111111111"
	const repo = "22222222-2222-4222-8222-222222222222"
	const pool = "33333333-3333-4333-8333-333333333333"
	const row = "44444444-4444-4444-8444-444444444444"
	witness := strings.Repeat("a", 64)
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			t.Error("unexpected managed GARM fixture request")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set(managedsource.SourceHeader, witness)
		w.Header().Set(managedsource.ControllerHeader, controller)
		rows := []map[string]any{}
		switch r.URL.Path {
		case "/api/v1/pools":
			rows = append(rows, map[string]any{"id": pool, "provider_name": "fixture", "repo_id": repo, "enabled": true, "generation": 1, "max_runners": 4,
				"tags": []any{map[string]any{"name": "linux"}}, "image": "fixture", "flavor": "small", "os_type": "linux", "os_arch": "amd64"})
		case "/api/v1/instances":
			rows = append(rows, map[string]any{"id": row, "generation": 0, "name": "fixture-runner", "provider_id": "fixture-label", "provider_name": "fixture",
				"pool_id": pool, "status": "running", "runner_status": "idle", "agent_id": 10})
		case "/api/v1/jobs":
		default:
			t.Error("unexpected managed GARM fixture path")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(fixture.Close)
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	trust := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Certificates: fixture.TLS.Certificates}
	pub := sha256.Sum256(fixture.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(pub[:])
	digest := sha256.Sum256([]byte(strings.Join([]string{"managed-stock-source/v1", fixture.URL, pin, witness, controller}, "\x00")))
	scope := garmapi.ManagedRunningRegistrationScope{SourceIdentity: hex.EncodeToString(digest[:]), ControllerID: controller, RepositoryID: repo, PoolID: pool, ProviderName: "fixture",
		OwnershipFingerprint: (stockidentity.Facts{Pool: pool, Repository: repo, Provider: "fixture", Flavor: "small", Image: "fixture", OSType: "linux", OSArch: "amd64"}).Fingerprint()}
	reader, err := garmapi.NewManagedDemand(fixture.URL, pin, controller, trust, []string{"fixture"}, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	report, err := reader.ObserveRunningRegistration(t.Context(), scope, row)
	if err != nil {
		t.Fatal(err)
	}
	return scope, row, report, reader
}

func TestCompletedCollectorRequiresSealedExactAssignment(t *testing.T) {
	start := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	var explicit atomic.Bool
	explicit.Store(true)
	scope, rowID, report, garm := collectorRegistrationFixture(t)
	repos := []demandrepair.CIRepository{{Repository: demandrepair.Repository{GarmID: scope.RepositoryID, Host: "github.example", Owner: "fixture", Name: "runners"}, GitHubID: repoID}}
	source := fixtureRepos(t, repos, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case attemptPath:
			runResponse(w, repoID, "completed", "success")
		case attemptPath + "/jobs":
			row := job(jobID, "completed")
			row["completed_at"], row["conclusion"] = end, "success"
			if explicit.Load() {
				row["run_attempt"] = 2
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "jobs": []any{row}})
		default:
			t.Error("unexpected fixture GET")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	target := jobUsageTarget{job: cihealth.JobKey{Attempt: cihealth.AttemptKey{Source: source.SourceID(), RepositoryID: repoID, WorkflowID: workflowID, RunID: runID, Attempt: 2}, JobID: jobID},
		scope: scope, rowID: rowID, page: 1}
	read := func() (demandrepair.CIAttempt, demandrepair.CIRunnerAssignmentReceipt, bool) {
		return target.readAssignment(t.Context(), source, start, end, func() bool { return true })
	}
	if !target.enrolledBy(source) {
		t.Fatal("exact enrollment refused")
	}
	a, r, ok := read()
	if !ok {
		t.Fatal("exact completed receipt missing")
	}
	if _, ok = target.registration(a, r, garmapi.ManagedRunningRegistrationReport{}); ok {
		t.Fatal("unsealed GARM report accepted")
	}
	reg, ok := target.registration(a, r, report)
	if !ok || !reg.current() || reg.execution.Job != target.job || reg.row.RowID != rowID || reg.row.RepositoryID != scope.RepositoryID ||
		reg.row.AgentID != reg.execution.RunnerAgentID || reg.enrollment.OwnershipFingerprint != scope.OwnershipFingerprint {
		t.Fatal("opaque job/row pair did not bind exactly")
	}
	garm.Close()
	if reg.current() {
		t.Fatal("closed managed reader still released its row")
	}
	target.scope.RepositoryID = "other"
	if target.enrolledBy(source) {
		t.Fatal("cross-repository locator accepted")
	}
	target.scope.RepositoryID = scope.RepositoryID
	target.job.JobID++
	if _, _, ok = read(); ok {
		t.Fatal("sibling job received assignment")
	}
	target.job.JobID--
	explicit.Store(false)
	if _, _, ok = read(); ok {
		t.Fatal("legacy timing projection substituted for exact attempt proof")
	}
}
