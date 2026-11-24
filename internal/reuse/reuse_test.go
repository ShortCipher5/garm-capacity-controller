package reuse

import (
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var reuseTestStart = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func reuseTestDigest(seed byte) string {
	return strings.Repeat(string([]byte{seed}), 64)
}

func reuseTestCharge() ChargeIdentity {
	subject := incusapi.InstanceIdentitySubject{
		BackendKeyFingerprint: reuseTestDigest('a'),
		OriginFingerprint:     reuseTestDigest('b'),
		ScopeFingerprint:      reuseTestDigest('c'),
		ServerName:            "fake-incus-daemon",
		ServerVersion:         "6.0.4",
		Project:               "runners",
		Name:                  "runner-1",
		UUID:                  "11111111-1111-4111-8111-111111111111",
		Generation:            "22222222-2222-4222-8222-222222222222",
		CreatedAt:             reuseTestStart,
		ControllerID:          "controller-1",
		PoolID:                "pool-1",
		ReservationKey:        reuseTestDigest('d'),
		CPUMillis:             1000,
		RAMMiB:                2048,
	}
	return ChargeIdentity{
		Ref: ChargeRef{
			Key:     subject.ReservationKey,
			Pool:    "pool-1",
			Host:    "host-a",
			CPU:     subject.CPUMillis,
			RAM:     subject.RAMMiB,
			Disk:    4096,
			Runners: 1,
		},
		Stock: StockBinding{
			RegistrationDigest: reuseTestDigest('e'),
			ControllerID:       "controller-1",
			PoolID:             "pool-1",
			GitHubURL:          "https://github.example/owner/repo",
			RunnerName:         "runner-1",
			RunnerID:           17,
		},
		Provider: ProviderBinding{
			BackendKeyFingerprint: subject.BackendKeyFingerprint,
			OriginFingerprint:     subject.OriginFingerprint,
			ScopeFingerprint:      subject.ScopeFingerprint,
			ServerName:            subject.ServerName,
			ServerVersion:         subject.ServerVersion,
			Project:               subject.Project,
			Member:                "member-1",
		},
		Incarnation: IncarnationBinding{
			Name:       subject.Name,
			UUID:       subject.UUID,
			Generation: subject.Generation,
			CreatedAt:  subject.CreatedAt,
		},
		Volumes: []VolumeCharge{{Name: "runner-1", BindingDigest: VolumeBindingDigest(reuseTestDigest('c'), "runner-1")}},
	}
}

func reuseTestObservation(subject incusapi.InstanceIdentitySubject, observedAt time.Time) incusapi.InstanceIdentityObservation {
	return incusapi.InstanceIdentityObservation{
		Subject:            subject,
		SubjectFingerprint: canonicalDigest(subject),
		ObservedAt:         observedAt,
		InstanceStatus:     103,
	}
}

func reuseTestTeardown(subject incusapi.InstanceIdentitySubject) *InstanceTeardownProof {
	identity := reuseTestObservation(subject, reuseTestStart.Add(time.Minute))
	return &InstanceTeardownProof{
		Identity: identity,
		Delete: incusapi.TerminalOperationRecord{
			Identity:                identity,
			OperationID:             "87654321-4321-4321-a321-cba987654321",
			Kind:                    incusapi.RecoveryDelete,
			Class:                   "task",
			Description:             "Deleting instance",
			StatusCode:              200,
			CreatedAt:               reuseTestStart.Add(90 * time.Second),
			UpdatedAt:               reuseTestStart.Add(91 * time.Second),
			CapturedAt:              reuseTestStart.Add(2 * time.Minute),
			ResourceReadbackMatched: false,
			RequiresExclusiveFence:  true,
		},
		Absence: incusapi.ProjectAbsenceRecord{
			Identity:                      identity,
			StartedAt:                     reuseTestStart.Add(3 * time.Minute),
			CompletedAt:                   reuseTestStart.Add(4 * time.Minute),
			InstanceInventoryFingerprint:  reuseTestDigest('1'),
			OperationInventoryFingerprint: reuseTestDigest('2'),
			MatchingPasses:                2,
			SnapshotAtomic:                false,
			ProjectOnly:                   true,
		},
	}
}

func reuseTestWorkload() *WorkloadProof {
	return &WorkloadProof{
		RepositoryHost:     "github.example",
		RepositoryOwner:    "owner",
		RepositoryName:     "repo",
		JobID:              91,
		RunID:              12,
		RunAttempt:         1,
		RunnerID:           17,
		RunnerName:         "runner-1",
		Conclusion:         "success",
		StartedAt:          reuseTestStart.Add(-10 * time.Minute),
		CompletedAt:        reuseTestStart.Add(-9 * time.Minute),
		ObservedAt:         reuseTestStart.Add(-8 * time.Minute),
		SourceDigest:       reuseTestDigest('3'),
		RegistrationDigest: reuseTestDigest('e'),
	}
}

func reuseTestCleanup() *CleanupProof {
	return &CleanupProof{Volumes: []VolumeAbsence{{Name: "runner-1", ScopeDigest: reuseTestDigest('c'), ObservedAt: reuseTestStart.Add(3 * time.Minute)}}}
}

func teardownSubject(charge ChargeIdentity) incusapi.InstanceIdentitySubject {
	return incusapi.InstanceIdentitySubject{
		BackendKeyFingerprint: charge.Provider.BackendKeyFingerprint,
		OriginFingerprint:     charge.Provider.OriginFingerprint,
		ScopeFingerprint:      charge.Provider.ScopeFingerprint,
		ServerName:            charge.Provider.ServerName,
		ServerVersion:         charge.Provider.ServerVersion,
		Project:               charge.Provider.Project,
		Name:                  charge.Incarnation.Name,
		UUID:                  charge.Incarnation.UUID,
		Generation:            charge.Incarnation.Generation,
		CreatedAt:             charge.Incarnation.CreatedAt,
		ControllerID:          charge.Stock.ControllerID,
		PoolID:                charge.Stock.PoolID,
		ReservationKey:        charge.Ref.Key,
		CPUMillis:             charge.Ref.CPU,
		RAMMiB:                charge.Ref.RAM,
	}
}

func reuseTestEvidence(charge ChargeIdentity) TerminalEvidence {
	return TerminalEvidence{
		Charge:     charge,
		Workload:   reuseTestWorkload(),
		Teardown:   reuseTestTeardown(teardownSubject(charge)),
		Cleanup:    reuseTestCleanup(),
		CapturedAt: reuseTestStart.Add(5 * time.Minute),
	}
}

func heldContains(held []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range held {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func TestChargeIdentityValidAndNameReuseDistinct(t *testing.T) {
	charge := reuseTestCharge()
	if !charge.Valid() {
		t.Fatal("fixture charge invalid")
	}
	if charge.Digest() == "" {
		t.Fatal("empty charge digest")
	}
	replacement := charge
	replacement.Incarnation.UUID = "33333333-3333-4333-8333-333333333333"
	if SameCharge(charge, replacement) {
		t.Fatal("replacement with new incarnation treated as same charge")
	}
	if !DisplayNameReused(charge, replacement) {
		t.Fatal("same-name replacement not detected")
	}
	otherRunner := charge
	otherRunner.Stock.RunnerID = 18
	otherRunner.Incarnation.Name = "runner-2"
	if SameCharge(charge, otherRunner) || DisplayNameReused(charge, otherRunner) {
		t.Fatal("unrelated charge misclassified")
	}
}

func TestAssessFullyQualifiedReleasesEveryDimension(t *testing.T) {
	charge := reuseTestCharge()
	verdict := Assess(charge, []TerminalEvidence{reuseTestEvidence(charge)})
	if len(verdict.Held) != 0 || !verdict.Releasable.All() {
		t.Fatalf("qualified evidence held: %+v", verdict)
	}
	again := Assess(charge, []TerminalEvidence{reuseTestEvidence(charge), reuseTestEvidence(charge)})
	if again.Digest != verdict.Digest || len(again.Held) != 0 || !again.Releasable.All() {
		t.Fatalf("duplicate delivery changed verdict: %+v", again)
	}
}

func TestAssessReorderedAndSplitDeliveriesAreDeterministic(t *testing.T) {
	charge := reuseTestCharge()
	full := reuseTestEvidence(charge)
	splitWorkload := TerminalEvidence{Charge: charge, Workload: full.Workload, CapturedAt: full.CapturedAt.Add(-time.Minute)}
	splitTeardown := TerminalEvidence{Charge: charge, Teardown: full.Teardown, Cleanup: full.Cleanup, CapturedAt: full.CapturedAt}
	a := Assess(charge, []TerminalEvidence{full})
	b := Assess(charge, []TerminalEvidence{splitTeardown, splitWorkload})
	c := Assess(charge, []TerminalEvidence{splitWorkload, splitTeardown})
	if a.Digest != b.Digest || b.Digest != c.Digest {
		t.Fatalf("reordered/partial delivery changed verdict: %s %s %s", a.Digest, b.Digest, c.Digest)
	}
	if len(b.Held) != 0 || !b.Releasable.All() {
		t.Fatalf("split delivery held: %+v", b)
	}
}

func TestAssessStillAssignedRunnerStaysCharged(t *testing.T) {
	charge := reuseTestCharge()
	// A still-assigned runner has not been torn down: no captured delete, no
	// post-delete absence and no cleanup proof, positive or otherwise.
	evidence := reuseTestEvidence(charge)
	evidence.Workload = nil
	evidence.Teardown = nil
	evidence.Cleanup = nil
	verdict := Assess(charge, []TerminalEvidence{evidence})
	if verdict.Releasable.Any() {
		t.Fatalf("still-assigned runner released: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldAwaitingCompletedWorkload, HeldAwaitingTerminalDelete, HeldAwaitingPostDeleteAbsence, HeldResidualVolumeUnverified) {
		t.Fatalf("missing hold reasons: %v", verdict.Held)
	}
	// An in-progress assignment observation never mints a completed proof.
	evidence.Teardown = reuseTestTeardown(teardownSubject(charge))
	evidence.Cleanup = reuseTestCleanup()
	busy := *reuseTestWorkload()
	busy.Conclusion = "in_progress"
	evidence.Workload = &busy
	if verdict := Assess(charge, []TerminalEvidence{evidence}); verdict.Releasable.CPU || verdict.Releasable.RAM || verdict.Releasable.Runners || !heldContains(verdict.Held, HeldAwaitingCompletedWorkload) {
		t.Fatalf("unqualified conclusion released compute: %+v", verdict)
	}
}

func TestAssessDelayedTeardownWithoutDeleteOrAbsenceStaysCharged(t *testing.T) {
	charge := reuseTestCharge()
	evidence := reuseTestEvidence(charge)
	identity := evidence.Teardown.Identity
	evidence.Teardown = &InstanceTeardownProof{Identity: identity}
	evidence.Cleanup = nil
	verdict := Assess(charge, []TerminalEvidence{evidence})
	if verdict.Releasable.Any() {
		t.Fatalf("teardown without terminal delete released: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldAwaitingTerminalDelete, HeldAwaitingPostDeleteAbsence, HeldResidualVolumeUnverified) {
		t.Fatalf("missing teardown hold reasons: %v", verdict.Held)
	}
	// A positively observed absent charged volume frees disk even while the
	// unproven compute dimensions stay charged: verified dimensions only.
	evidence.Cleanup = reuseTestCleanup()
	verdict = Assess(charge, []TerminalEvidence{evidence})
	if verdict.Releasable.CPU || verdict.Releasable.RAM || verdict.Releasable.Runners || !verdict.Releasable.Disk {
		t.Fatalf("dimension separation violated: %+v", verdict)
	}
}

func TestAssessResidualVolumeKeepsDiskCharged(t *testing.T) {
	charge := reuseTestCharge()
	evidence := reuseTestEvidence(charge)
	evidence.Cleanup = &CleanupProof{Volumes: []VolumeAbsence{{Name: "other-volume", ScopeDigest: reuseTestDigest('c'), ObservedAt: reuseTestStart.Add(3 * time.Minute)}}}
	verdict := Assess(charge, []TerminalEvidence{evidence})
	if !verdict.Releasable.CPU || !verdict.Releasable.RAM || !verdict.Releasable.Runners || verdict.Releasable.Disk {
		t.Fatalf("residual volume released disk: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldResidualVolumeUnverified) {
		t.Fatalf("missing residual volume reason: %v", verdict.Held)
	}
}

func TestAssessAuditedInseparabilityQualifiesDisk(t *testing.T) {
	charge := reuseTestCharge()
	evidence := reuseTestEvidence(charge)
	evidence.Cleanup = &CleanupProof{Capability: &AuditedCapability{
		Kind:        CapabilityInstanceStorageInseparable,
		ScopeDigest: charge.Provider.ScopeFingerprint,
		AuditedAt:   reuseTestStart.Add(3 * time.Minute),
	}}
	if verdict := Assess(charge, []TerminalEvidence{evidence}); len(verdict.Held) != 0 || !verdict.Releasable.All() {
		t.Fatalf("audited inseparability held: %+v", verdict)
	}
	// The capability is scoped: another provider scope never qualifies this charge.
	evidence.Cleanup.Capability.ScopeDigest = reuseTestDigest('9')
	if verdict := Assess(charge, []TerminalEvidence{evidence}); verdict.Releasable.Disk || !heldContains(verdict.Held, HeldResidualVolumeUnverified) {
		t.Fatalf("foreign capability released disk: %+v", verdict)
	}
}

func TestAssessSameNameReplacementEvidenceStaysCharged(t *testing.T) {
	charge := reuseTestCharge()
	replacement := charge
	replacement.Incarnation.UUID = "33333333-3333-4333-8333-333333333333"
	verdict := Assess(charge, []TerminalEvidence{reuseTestEvidence(replacement)})
	if verdict.Releasable.Any() {
		t.Fatalf("replacement evidence released original charge: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldSameNameReplacement, HeldAwaitingCompletedWorkload, HeldAwaitingTerminalDelete, HeldAwaitingPostDeleteAbsence, HeldResidualVolumeUnverified) {
		t.Fatalf("missing replacement hold reasons: %v", verdict.Held)
	}
}

func TestAssessCompetingRepositoryEvidenceStaysCharged(t *testing.T) {
	charge := reuseTestCharge()
	other := charge
	other.Stock.GitHubURL = "https://github.example/owner/other"
	other.Stock.RegistrationDigest = reuseTestDigest('g')
	verdict := Assess(charge, []TerminalEvidence{reuseTestEvidence(other)})
	if verdict.Releasable.Any() {
		t.Fatalf("competing repository evidence released charge: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldEvidenceForDistinctCharge) {
		t.Fatalf("missing distinct charge reason: %v", verdict.Held)
	}
}

func TestAssessConflictingEvidenceBlocksRelease(t *testing.T) {
	charge := reuseTestCharge()
	first := reuseTestEvidence(charge)
	second := reuseTestEvidence(charge)
	second.Workload.JobID = 92
	second.Workload.RunID = 13
	verdict := Assess(charge, []TerminalEvidence{first, second})
	if verdict.Releasable.Any() {
		t.Fatalf("conflicting evidence released: %+v", verdict)
	}
	if !heldContains(verdict.Held, HeldConflictingEvidence) {
		t.Fatalf("missing conflict reason: %v", verdict.Held)
	}
}

func TestAssessChronologyConflictBlocksRelease(t *testing.T) {
	charge := reuseTestCharge()
	evidence := reuseTestEvidence(charge)
	late := reuseTestWorkload()
	late.StartedAt = evidence.Teardown.Delete.CapturedAt.Add(time.Minute)
	late.CompletedAt = evidence.Teardown.Delete.CapturedAt.Add(2 * time.Minute)
	late.ObservedAt = evidence.Teardown.Delete.CapturedAt.Add(3 * time.Minute)
	evidence.Workload = late
	verdict := Assess(charge, []TerminalEvidence{evidence})
	if verdict.Releasable.Any() || !heldContains(verdict.Held, HeldConflictingEvidence, HeldChronologyConflict) {
		t.Fatalf("impossible chronology released: %+v", verdict)
	}
}

func TestAssessInvalidChargeAndInvalidEvidenceStayHeld(t *testing.T) {
	charge := reuseTestCharge()
	invalid := charge
	invalid.Ref.Key = ""
	if verdict := Assess(invalid, []TerminalEvidence{reuseTestEvidence(charge)}); len(verdict.Held) != 1 || verdict.Held[0] != HeldInvalidCharge {
		t.Fatalf("invalid charge not held: %+v", verdict)
	}
	evidence := reuseTestEvidence(charge)
	evidence.CapturedAt = time.Time{}
	if verdict := Assess(charge, []TerminalEvidence{evidence}); verdict.Releasable.Any() || !heldContains(verdict.Held, HeldEvidenceForDistinctCharge) {
		t.Fatalf("undated evidence not rejected deterministically: %+v", verdict)
	}
}

func TestAssessZeroDiskChargeNormalizesDiskDimension(t *testing.T) {
	charge := reuseTestCharge()
	charge.Ref.Disk = 0
	charge.Volumes = nil
	evidence := reuseTestEvidence(charge)
	evidence.Cleanup = nil
	verdict := Assess(charge, []TerminalEvidence{evidence})
	if len(verdict.Held) != 0 || !verdict.Releasable.All() {
		t.Fatalf("zero-disk charge held: %+v", verdict)
	}
}
