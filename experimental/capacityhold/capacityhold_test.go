package capacityhold

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stubVerifier is a TEST SEAM ONLY. It verifies nothing about physical
// reality — it exists to exercise the register's state machine and audit
// semantics. The real EvidenceVerifier must come from an independent
// enforcement boundary (out of scope for this worktree); see the package
// documentation.
type stubVerifier struct {
	err error
}

func (s stubVerifier) VerifyAbsence(_ context.Context, _ AbsenceEvidence) error {
	return s.err
}

// stubEvidence is a TEST SEAM ONLY, standing in for the (unimplemented)
// fenced absence proof the external host-boundary would produce.
type stubEvidence struct {
	opID  string
	kind  string
	fence FenceRef
}

func (e stubEvidence) OperationID() string { return e.opID }
func (e stubEvidence) Kind() string        { return e.kind }
func (e stubEvidence) Fence() FenceRef     { return e.fence }

func testFence(id string) FenceRef {
	return FenceRef{FenceID: id, IssuedAt: time.Now().UTC()}
}

func newRegister(t *testing.T, opts ...Option) (*Register, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "holds.db")
	reg, err := Open(path, opts...)
	if err != nil {
		t.Fatalf("open register: %v", err)
	}
	t.Cleanup(func() { reg.Close() })
	return reg, path
}

func mustCharge(t *testing.T, reg *Register, opID string) {
	t.Helper()
	if err := reg.Charge(context.Background(), opID, map[string]string{"instance": opID, "state": "running"}); err != nil {
		t.Fatalf("charge %q: %v", opID, err)
	}
}

func mustState(t *testing.T, reg *Register, opID string) Hold {
	t.Helper()
	h, err := reg.State(context.Background(), opID)
	if err != nil {
		t.Fatalf("state %q: %v", opID, err)
	}
	return h
}

func mustCharged(t *testing.T, reg *Register) int {
	t.Helper()
	n, err := reg.Charged(context.Background())
	if err != nil {
		t.Fatalf("charged: %v", err)
	}
	return n
}

func auditEvents(t *testing.T, reg *Register, opID string) []string {
	t.Helper()
	entries, err := reg.AuditTrail(context.Background(), opID)
	if err != nil {
		t.Fatalf("audit trail %q: %v", opID, err)
	}
	var events []string
	for _, e := range entries {
		events = append(events, e.Event)
	}
	return events
}

// TestLostAcknowledgement models: the mutating write was dispatched, the
// acknowledgement never arrived (response lost on the wire), the operation
// was marked unknown, and afterwards observations keep arriving. The charge
// must be held throughout, and even a late ack must not clear unknown.
func TestLostAcknowledgement(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()
	op := "delete/inst-001"

	mustCharge(t, reg, op)
	if err := reg.MarkDispatched(ctx, op); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	// The ack is lost: the caller gives up waiting and marks unknown.
	if err := reg.MarkUnknown(ctx, op, "ack deadline exceeded"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}

	// The late ack eventually arrives. It must be recorded and ignored.
	if err := reg.Acknowledge(ctx, op); err != nil {
		t.Fatalf("late ack: %v", err)
	}
	// Later observations arrive: the resource reads as desired (matching
	// read), then the provider reports NOT_FOUND, then it vanishes from
	// listings. None of these may release the charge.
	observations := []Observation{
		{Kind: ObsRead, MatchesDesired: true, Detail: "settings equal desired"},
		{Kind: ObsNotFound, Detail: "provider 404"},
		{Kind: ObsListAbsent, Detail: "gone from upstream list"},
	}
	for _, obs := range observations {
		if err := reg.RecordObservation(ctx, op, obs); err != nil {
			t.Fatalf("record %s: %v", obs.Kind, err)
		}
	}

	h := mustState(t, reg, op)
	if h.State != StateUnknown {
		t.Fatalf("state = %q, want %q", h.State, StateUnknown)
	}
	if got := mustCharged(t, reg); got != 1 {
		t.Fatalf("charged = %d, want 1", got)
	}
	events := auditEvents(t, reg, op)
	want := []string{EventCharged, EventDispatched, EventMarkUnknown, EventLateAckIgnored, EventObsReadInadmissible, EventObsInadmissible, EventObsInadmissible}
	if len(events) != len(want) {
		t.Fatalf("audit events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("audit events = %v, want %v", events, want)
		}
	}
}

// TestMatchingReadNeverClearsUnknown pins rule 1 directly: an unknown hold
// observing exactly the desired settings stays unknown and charged.
func TestMatchingReadNeverClearsUnknown(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()
	op := "create/inst-002"

	mustCharge(t, reg, op)
	if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := reg.RecordObservation(ctx, op, Observation{Kind: ObsRead, MatchesDesired: true, Detail: "still exactly desired"}); err != nil {
			t.Fatalf("matching read %d: %v", i, err)
		}
	}

	h := mustState(t, reg, op)
	if h.State != StateUnknown {
		t.Fatalf("matching read cleared unknown: state = %q, want %q", h.State, StateUnknown)
	}
	if got := mustCharged(t, reg); got != 1 {
		t.Fatalf("charged = %d, want 1", got)
	}
}

// TestNonEvidenceNeverReleases pins rule 2: none of NOT_FOUND, a
// successful delete API call, a database flag flip, or an unverified
// signed receipt proves physical completion, so none releases a charge —
// from either pending or unknown.
func TestNonEvidenceNeverReleases(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
	}{
		{"provider_not_found", Observation{Kind: ObsNotFound, Detail: "404"}},
		{"delete_api_result", Observation{Kind: ObsDeleteResult, Detail: "delete returned success"}},
		{"db_flag_flip", Observation{Kind: ObsDBFlag, Detail: "row marked deleted=true"}},
		{"unverified_receipt", Observation{Kind: ObsReceipt, Receipt: &Receipt{Revision: 5, Assertion: "signed-by-someone"}}},
	}
	for _, tc := range cases {
		for _, state := range []State{StatePending, StateUnknown} {
			name := fmt.Sprintf("%s/from-%s", tc.name, state)
			t.Run(name, func(t *testing.T) {
				reg, _ := newRegister(t)
				ctx := context.Background()
				op := "op/" + name

				mustCharge(t, reg, op)
				if state == StateUnknown {
					if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
						t.Fatalf("mark unknown: %v", err)
					}
				}
				if err := reg.RecordObservation(ctx, op, tc.obs); err != nil {
					t.Fatalf("record: %v", err)
				}
				h := mustState(t, reg, op)
				if h.State == StateSettled {
					t.Fatalf("%s released the charge from %s", tc.name, state)
				}
				if got := mustCharged(t, reg); got != 1 {
					t.Fatalf("charged = %d, want 1", got)
				}
			})
		}
	}
}

// TestStaleReceiptRejected: a receipt referencing an old hold revision is
// actively rejected (the register must not let stale assertions even
// influence bookkeeping), the rejection is audited, and the state is
// unchanged.
func TestStaleReceiptRejected(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()
	op := "delete/inst-003"

	mustCharge(t, reg, op)
	// Two state changes bump the hold revision to 1 (charge->pending is the
	// initial insert; dispatched is state-preserving, unknown is the first
	// state change).
	if err := reg.MarkDispatched(ctx, op); err != nil {
		t.Fatalf("dispatched: %v", err)
	}
	if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
		t.Fatalf("unknown: %v", err)
	}
	h := mustState(t, reg, op)
	if h.Revision != 1 {
		t.Fatalf("revision = %d, want 1", h.Revision)
	}

	err := reg.RecordObservation(ctx, op, Observation{
		Kind:    ObsReceipt,
		Receipt: &Receipt{Revision: h.Revision - 1, Assertion: "signed-old-state"},
		Detail:  "receipt against superseded state",
	})
	if !errors.Is(err, ErrStaleReceipt) {
		t.Fatalf("err = %v, want ErrStaleReceipt", err)
	}
	h = mustState(t, reg, op)
	if h.State != StateUnknown || h.Revision != 1 {
		t.Fatalf("stale receipt changed state: %+v", h)
	}
	if got := mustCharged(t, reg); got != 1 {
		t.Fatalf("charged = %d, want 1", got)
	}
	events := auditEvents(t, reg, op)
	if events[len(events)-1] != EventReceiptRejectedStale {
		t.Fatalf("last audit event = %q, want %q", events[len(events)-1], EventReceiptRejectedStale)
	}
}

// TestOrphanedResourceStillCharged: the operation vanishes from upstream
// lists entirely. Absence from a listing is an observation, not proof, so
// the charge stands.
func TestOrphanedResourceStillCharged(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()
	op := "scale-down/inst-004"

	mustCharge(t, reg, op)
	if err := reg.MarkUnknown(ctx, op, "no ack, no list entry"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	if err := reg.RecordObservation(ctx, op, Observation{Kind: ObsListAbsent, Detail: "orphaned: not in any upstream listing"}); err != nil {
		t.Fatalf("record list_absent: %v", err)
	}
	h := mustState(t, reg, op)
	if h.State != StateUnknown {
		t.Fatalf("state = %q, want %q", h.State, StateUnknown)
	}
	if got := mustCharged(t, reg); got != 1 {
		t.Fatalf("charged = %d, want 1", got)
	}
}

// checkpointAndClose checkpoints the WAL and closes the register so the
// database file can be copied as a consistent snapshot.
func checkpointAndClose(t *testing.T, reg *Register) {
	t.Helper()
	if _, err := reg.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestRestartDurability: the register survives process death; state, audit
// trail and charge view are intact after reopening the same file.
func TestRestartDurability(t *testing.T) {
	reg, path := newRegister(t)
	ctx := context.Background()
	op := "delete/inst-005"

	mustCharge(t, reg, op)
	if err := reg.MarkDispatched(ctx, op); err != nil {
		t.Fatalf("dispatched: %v", err)
	}
	if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if err := reg.Acknowledge(ctx, op); err != nil {
		t.Fatalf("late ack: %v", err)
	}
	if err := reg.RecordObservation(ctx, op, Observation{Kind: ObsNotFound}); err != nil {
		t.Fatalf("not_found: %v", err)
	}
	checkpointAndClose(t, reg)

	// Reopen: new Register instance, same file.
	reg2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reg2.Close()

	h := mustState(t, reg2, op)
	if h.State != StateUnknown || h.Revision != 1 {
		t.Fatalf("state after restart = %+v, want unknown rev 1", h)
	}
	if h.Desired["instance"] != op {
		t.Fatalf("desired lost across restart: %v", h.Desired)
	}
	if got := mustCharged(t, reg2); got != 1 {
		t.Fatalf("charged after restart = %d, want 1", got)
	}
	events := auditEvents(t, reg2, op)
	if len(events) != 5 {
		t.Fatalf("audit trail after restart = %v (len %d), want 5 entries", events, len(events))
	}
}

// TestRestoredDatabaseDetected models a snapshot rollback: the database
// file is restored from an older copy, rewinding the recorded state. With
// an out-of-band sequence expectation the register detects the rewind at
// open time and quarantines itself: reads still work, the charge in the
// rewound file still counts, and every mutation is refused. The test also
// pins the honest limitation: without an expectation, a whole-file restore
// is not detectable from inside the database.
func TestRestoredDatabaseDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "holds.db")
	snapshot := filepath.Join(dir, "holds.snapshot.db")
	ctx := context.Background()

	reg, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustCharge(t, reg, "op/before-snapshot")
	if err := reg.MarkUnknown(ctx, "op/before-snapshot", "ack lost"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	st, err := reg.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	seqAtSnapshot := st.LastSeq
	checkpointAndClose(t, reg)
	if err := copyFile(path, snapshot); err != nil {
		t.Fatalf("snapshot copy: %v", err)
	}

	// Time passes; more operations commit, advancing the sequence.
	reg2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	mustCharge(t, reg2, "op/after-snapshot")
	checkpointAndClose(t, reg2)

	// Rollback: restore the older snapshot over the live file.
	if err := copyFile(snapshot, path); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}

	// Honest limitation: without an expectation the rewound file opens
	// cleanly; rewind of the WHOLE file is undetectable from inside.
	regPlain, err := Open(path)
	if err != nil {
		t.Fatalf("plain open after restore: %v", err)
	}
	stPlain, err := regPlain.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if stPlain.Suspect {
		t.Fatal("plain open unexpectedly flagged suspect")
	}
	if stPlain.Holds != 1 {
		t.Fatalf("holds in rewound file = %d, want 1 (op/after-snapshot is gone)", stPlain.Holds)
	}
	regPlain.Close()

	// With the caller's out-of-band expectation the rewind is detected at
	// open time and the register quarantines itself.
	reg3, err := OpenWithExpectedSeq(path, seqAtSnapshot+10)
	if err != nil {
		t.Fatalf("open with expectation: %v", err)
	}
	defer reg3.Close()
	st3, err := reg3.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st3.Suspect {
		t.Fatal("rewound register not flagged suspect")
	}
	if st3.Charged != 1 {
		t.Fatalf("charged in quarantined register = %d, want 1", st3.Charged)
	}
	// Reads still answer; mutations are refused.
	if _, err := reg3.State(ctx, "op/before-snapshot"); err != nil {
		t.Fatalf("read in suspect mode: %v", err)
	}
	if err := reg3.Charge(ctx, "op/new", nil); !errors.Is(err, ErrSuspect) {
		t.Fatalf("charge in suspect mode: err = %v, want ErrSuspect", err)
	}
	if err := reg3.Release(ctx, "op/before-snapshot", stubEvidence{opID: "op/before-snapshot", kind: "fenced_absence_proof", fence: testFence("f-1")}); !errors.Is(err, ErrSuspect) {
		t.Fatalf("release in suspect mode: err = %v, want ErrSuspect", err)
	}
	// An expectation matching the rewound file opens clean.
	reg4, err := OpenWithExpectedSeq(path, seqAtSnapshot)
	if err != nil {
		t.Fatalf("open with matching expectation: %v", err)
	}
	reg4.Close()
}

// TestReleaseUnimplemented: with no verifier registered (the honest default
// for this worktree) release is audited as denied and refused with
// ErrEvidenceUnimplemented — from pending AND from unknown.
func TestReleaseUnimplemented(t *testing.T) {
	for _, from := range []State{StatePending, StateUnknown} {
		t.Run(string(from), func(t *testing.T) {
			reg, _ := newRegister(t)
			ctx := context.Background()
			op := "op/release-" + string(from)

			mustCharge(t, reg, op)
			if from == StateUnknown {
				if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
					t.Fatalf("mark unknown: %v", err)
				}
			}
			err := reg.Release(ctx, op, stubEvidence{opID: op, kind: "fenced_absence_proof", fence: testFence("f-1")})
			if !errors.Is(err, ErrEvidenceUnimplemented) {
				t.Fatalf("err = %v, want ErrEvidenceUnimplemented", err)
			}
			h := mustState(t, reg, op)
			if h.State != from {
				t.Fatalf("state = %q, want %q", h.State, from)
			}
			if got := mustCharged(t, reg); got != 1 {
				t.Fatalf("charged = %d, want 1", got)
			}
			events := auditEvents(t, reg, op)
			if events[len(events)-1] != EventReleaseDeniedNoVerifier {
				t.Fatalf("last audit event = %q, want %q", events[len(events)-1], EventReleaseDeniedNoVerifier)
			}
		})
	}
}

// TestReleaseWithStubVerifier exercises the state machine around the
// evidence seam. The stub verifies NOTHING physical; it only proves the
// register settles with the outcome kept once a verifier accepts evidence,
// and that a rejecting verifier leaves the charge held.
func TestReleaseWithStubVerifier(t *testing.T) {
	accepting, _ := newRegister(t, WithEvidenceVerifier(stubVerifier{}))
	rejecting, _ := newRegister(t, WithEvidenceVerifier(stubVerifier{err: errors.New("fence mismatch")}))
	ctx := context.Background()

	mustCharge(t, accepting, "op/settle")
	if err := accepting.MarkUnknown(ctx, "op/settle", "ack lost"); err != nil {
		t.Fatalf("mark unknown: %v", err)
	}
	if err := accepting.Release(ctx, "op/settle", stubEvidence{opID: "op/settle", kind: "fenced_absence_proof", fence: testFence("f-1")}); err != nil {
		t.Fatalf("release with accepting stub: %v", err)
	}
	h := mustState(t, accepting, "op/settle")
	if h.State != StateSettled || h.Outcome != OutcomeReleasedProvenAbsent {
		t.Fatalf("settled hold = %+v, want settled with outcome kept", h)
	}
	if got := mustCharged(t, accepting); got != 0 {
		t.Fatalf("charged after settle = %d, want 0", got)
	}
	events := auditEvents(t, accepting, "op/settle")
	if events[len(events)-1] != EventReleased {
		t.Fatalf("last audit event = %q, want %q", events[len(events)-1], EventReleased)
	}

	mustCharge(t, rejecting, "op/reject")
	err := rejecting.Release(ctx, "op/reject", stubEvidence{opID: "op/reject", kind: "fenced_absence_proof", fence: testFence("f-2")})
	if !errors.Is(err, ErrVerifierRejected) {
		t.Fatalf("err = %v, want ErrVerifierRejected", err)
	}
	if got := mustCharged(t, rejecting); got != 1 {
		t.Fatalf("charged after rejected evidence = %d, want 1", got)
	}
	// Evidence bound to a different operation is refused.
	err = rejecting.Release(ctx, "op/reject", stubEvidence{opID: "op/other", kind: "fenced_absence_proof", fence: testFence("f-3")})
	if !errors.Is(err, ErrEvidenceMismatch) {
		t.Fatalf("err = %v, want ErrEvidenceMismatch", err)
	}
}

func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, 0o600)
}

// TestConcurrentAccess hammers the register from many goroutines so the
// race detector can verify the single-writer transaction discipline.
// goroutines own disjoint operation ids; readers scan global views.
func TestConcurrentAccess(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()
	const workers = 8
	const opsPerWorker = 25

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				op := fmt.Sprintf("op/w%d-%d", w, i)
				if err := reg.Charge(ctx, op, nil); err != nil {
					t.Errorf("charge %s: %v", op, err)
					return
				}
				if err := reg.MarkDispatched(ctx, op); err != nil {
					t.Errorf("dispatched %s: %v", op, err)
					return
				}
				if err := reg.Acknowledge(ctx, op); err != nil {
					t.Errorf("ack %s: %v", op, err)
					return
				}
				if err := reg.MarkUnknown(ctx, op, "ack lost"); err != nil {
					t.Errorf("unknown %s: %v", op, err)
					return
				}
				if err := reg.RecordObservation(ctx, op, Observation{Kind: ObsRead, MatchesDesired: true}); err != nil {
					t.Errorf("obs %s: %v", op, err)
					return
				}
			}
		}(w)
	}
	// Concurrent readers.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := reg.Status(ctx); err != nil {
					t.Errorf("status: %v", err)
					return
				}
				if _, err := reg.Charged(ctx); err != nil {
					t.Errorf("charged: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	st, err := reg.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Holds != workers*opsPerWorker {
		t.Fatalf("holds = %d, want %d", st.Holds, workers*opsPerWorker)
	}
	if st.ByState[StateUnknown] != workers*opsPerWorker {
		t.Fatalf("unknown holds = %d, want %d", st.ByState[StateUnknown], workers*opsPerWorker)
	}
	if st.Charged != workers*opsPerWorker {
		t.Fatalf("charged = %d, want %d", st.Charged, workers*opsPerWorker)
	}
	// Audit log must be dense: every allocated sequence committed exactly
	// one row (charge, dispatched, ack, unknown, read = 5 per hold).
	if st.LastSeq != uint64(workers*opsPerWorker*5) {
		t.Fatalf("last seq = %d, want %d", st.LastSeq, workers*opsPerWorker*5)
	}
}

// TestInvalidTransitions pins the guardrails of the state machine.
func TestInvalidTransitions(t *testing.T) {
	reg, _ := newRegister(t)
	ctx := context.Background()

	if _, err := reg.State(ctx, "op/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("state of missing op: err = %v, want ErrNotFound", err)
	}
	if err := reg.MarkUnknown(ctx, "op/missing", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mark unknown of missing op: err = %v, want ErrNotFound", err)
	}

	mustCharge(t, reg, "op/guard")
	// Double charge is an invalid transition.
	if err := reg.Charge(ctx, "op/guard", nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double charge: err = %v, want ErrInvalidTransition", err)
	}
	// Settle it with a stub verifier, then nothing may touch it again.
	accepting := stubVerifier{}
	reg2, _ := newRegister(t, WithEvidenceVerifier(accepting), WithClock(func() time.Time { return time.Now().UTC() }))
	_ = reg2
	mustCharge(t, reg2, "op/terminal")
	if err := reg2.Release(ctx, "op/terminal", stubEvidence{opID: "op/terminal", kind: "fenced_absence_proof", fence: testFence("f-1")}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := reg2.MarkUnknown(ctx, "op/terminal", "too late"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mark unknown after settle: err = %v, want ErrInvalidTransition", err)
	}
	if err := reg2.Acknowledge(ctx, "op/terminal"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ack after settle: err = %v, want ErrInvalidTransition", err)
	}
	if err := reg2.Release(ctx, "op/terminal", stubEvidence{opID: "op/terminal", kind: "fenced_absence_proof", fence: testFence("f-2")}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double release: err = %v, want ErrInvalidTransition", err)
	}
	if err := reg2.RecordObservation(ctx, "op/terminal", Observation{Kind: ObsNotFound}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("observation after settle: err = %v, want ErrInvalidTransition", err)
	}
}
