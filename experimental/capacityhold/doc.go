// Package capacityhold is an offline, experimental model of a durable
// capacity-charge ("hold") register. It is part of a multi-agent research
// worktree (branch exp/agent5-capacity-hold) and is strictly bounded:
//
//   - OFFLINE MODEL ONLY. Nothing in this package connects to garm's
//     runner/ execution paths, worker loops, main() or any daemon. It is
//     never imported by production code and nothing it defines is enabled
//     by default.
//   - NOT A CAPACITY AUTHORITY. This package records and audits charges; it
//     does not enforce capacity against any real resource. Physical fencing
//     and host enforcement are explicitly OUT OF SCOPE: they belong to a
//     separate host-boundary component (Agent 4). The interface where that
//     boundary's proof would plug in is declared here (see AbsenceEvidence
//     and EvidenceVerifier) but is NOT implemented and NOT satisfied by
//     anything in this repository. A Register created without a verifier
//     refuses every release with ErrEvidenceUnimplemented.
//
// The register models one charge per operation (for example "delete
// instance X" or "create instance Y"). A charge flows
//
//	pending -> unknown -> settled
//
// with the terminal outcome kept on the record. Every transition and every
// recorded observation is written to an append-only audit table inside the
// same SQLite transaction that changes the hold, so the charge story can be
// replayed after process death. SQLite (WAL mode, immediate transactions)
// provides restart durability; tests use temporary files only.
//
// Core rules encoded here (and covered by fault tests):
//
//  1. A matching read (observed settings/state equal to desired) NEVER
//     clears an unknown state by itself.
//  2. None of the following, alone, proves physical completion and therefore
//     never releases a charge: a provider NOT_FOUND, a successful delete API
//     call result, a database flag flip, or a signed assertion/receipt
//     without independent verification. These observations are recorded in
//     the audit log marked inadmissible; the charge stands.
//  3. Unknown states remain charged. Release requires an explicitly
//     designed evidence class: an independently fenced absence proof
//     (AbsenceEvidence) verified by an enforcement boundary the register
//     does not contain (EvidenceVerifier). That class is UNIMPLEMENTED in
//     this worktree; tests that inject a stub verifier exercise only the
//     state machine, not physical reality.
//
// Known limitations:
//
//   - Whole-file restore rewinds the recorded state undetectably from
//     inside the database; OpenWithExpectedSeq lets a caller with an
//     out-of-band monotonic expectation flag the rewind and quarantine the
//     register (see RegisterStatus.Suspect). Without such an anchor only
//     intra-database inconsistencies (audit-log discontinuities) are
//     detectable at open time.
//   - The package deliberately has no GC of settled holds; retention is a
//     policy question for a future, qualified component.
package capacityhold
