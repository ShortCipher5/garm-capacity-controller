package capacityhold

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// State is the lifecycle state of a hold.
type State string

const (
	// StatePending: a charge was recorded for an operation and is held
	// while the operation's physical outcome is uncertain.
	StatePending State = "pending"
	// StateUnknown: the operation's acknowledgement was lost or its
	// outcome became ambiguous. Unknown states REMAIN CHARGED and cannot
	// be cleared by a matching read or any other unverified observation.
	StateUnknown State = "unknown"
	// StateSettled: terminal. The outcome is kept on the record.
	StateSettled State = "settled"
)

// Outcome is the terminal outcome of a settled hold.
type Outcome string

const (
	// OutcomeNone is the zero outcome for non-settled holds.
	OutcomeNone Outcome = ""
	// OutcomeReleasedProvenAbsent is the only release path: absence proven
	// by admissible, independently verified evidence.
	OutcomeReleasedProvenAbsent Outcome = "released_proven_absent"
)

// ObservationKind identifies the class of an observation arriving from the
// (untrusted, lossy) outside world.
type ObservationKind string

const (
	// ObsRead is a read of the resource's settings/state.
	ObsRead ObservationKind = "read"
	// ObsNotFound is a provider-level NOT_FOUND for the resource.
	ObsNotFound ObservationKind = "provider_not_found"
	// ObsDeleteResult is the successful result of a delete API call.
	ObsDeleteResult ObservationKind = "delete_api_result"
	// ObsDBFlag is a database flag flip claiming completion.
	ObsDBFlag ObservationKind = "db_flag_flip"
	// ObsListAbsent means the operation/resource vanished from upstream
	// listings (orphaned resource).
	ObsListAbsent ObservationKind = "list_absent"
	// ObsReceipt is a signed assertion/receipt arriving without
	// independent verification.
	ObsReceipt ObservationKind = "receipt"
)

// Receipt is a signed assertion referencing the revision of the hold it
// claims to describe.
type Receipt struct {
	// Revision is the hold revision the receipt was issued against.
	Revision uint64
	// Assertion is an opaque signed assertion. This package never
	// verifies signatures; receipts are inadmissible for release.
	Assertion string
}

// Observation is an external observation recorded against a hold.
type Observation struct {
	Kind           ObservationKind
	MatchesDesired bool // ObsRead: observed settings/state equal to desired
	Receipt        *Receipt
	Detail         string
}

// Audit entry event names.
const (
	EventCharged                   = "charged"
	EventDispatched                = "dispatched"
	EventAcknowledged              = "acknowledged"
	EventLateAckIgnored            = "late_ack_ignored"
	EventMarkUnknown               = "mark_unknown"
	EventObsReadInadmissible       = "obs_read_inadmissible"
	EventObsInadmissible           = "obs_inadmissible"
	EventReceiptRejectedStale      = "receipt_rejected_stale"
	EventReceiptInadmissible       = "receipt_inadmissible"
	EventReleaseDeniedNoVerifier   = "release_denied_no_verifier"
	EventReleaseDeniedVerifyFailed = "release_denied_verify_failed"
	EventReleaseDeniedInadmissible = "release_denied_inadmissible"
	EventReleased                  = "released_proven_absent"
)

// FenceRef identifies an admission fence issued by an independent,
// external enforcement boundary. It is the anchor that separates "the
// register believes" from "the boundary fenced". Fences are issued by the
// host-boundary component that is OUT OF SCOPE for this package; this
// struct only names the shape the evidence must carry.
type FenceRef struct {
	FenceID  string
	IssuedAt time.Time
}

// AbsenceEvidence is the ONLY evidence class admissible for releasing a
// charge: an independently fenced proof of physical absence, produced and
// verified by an enforcement boundary this package does not contain. It is
// declared here so the state machine and audit semantics around it can be
// built and tested, but it is UNIMPLEMENTED in this worktree and nothing
// here can produce or verify a real instance of it.
type AbsenceEvidence interface {
	// OperationID identifies the hold (operation) the evidence refers to.
	OperationID() string
	// Kind names the concrete evidence type (e.g. "fenced_absence_proof").
	Kind() string
	// Fence returns the independent admission fence the evidence is bound
	// to. Evidence without a fresh, externally issued fence is inadmissible.
	Fence() FenceRef
}

// EvidenceVerifier verifies AbsenceEvidence against the independent
// enforcement boundary (fencing, host scope) that produced it. The real
// implementation belongs to the host-boundary component, which is OUT OF
// SCOPE here. A Register without a verifier refuses every release with
// ErrEvidenceUnimplemented. Test stubs are state-machine seams only, not
// physical authority.
type EvidenceVerifier interface {
	VerifyAbsence(ctx context.Context, ev AbsenceEvidence) error
}

var (
	ErrNotFound              = errors.New("capacityhold: hold not found")
	ErrInvalidTransition     = errors.New("capacityhold: invalid state transition")
	ErrStaleReceipt          = errors.New("capacityhold: stale receipt rejected")
	ErrInadmissibleEvidence  = errors.New("capacityhold: inadmissible evidence class")
	ErrEvidenceUnimplemented = errors.New("capacityhold: absence evidence verification is unimplemented (requires external enforcement boundary)")
	ErrEvidenceMismatch      = errors.New("capacityhold: evidence refers to a different operation")
	ErrStateRewound          = errors.New("capacityhold: recorded state rewound past caller expectation")
	ErrSuspect               = errors.New("capacityhold: register is quarantined (suspect), mutations refused")
	ErrVerifierRejected      = errors.New("capacityhold: evidence verification failed")
)

// errLateAck is internal: an ack arrived after the hold went unknown.
var errLateAck = errors.New("capacityhold: late ack on unknown hold")

// Hold is the recorded state of one charge.
type Hold struct {
	OpID       string
	State      State
	Outcome    Outcome
	Revision   uint64
	Desired    map[string]string
	CreatedSeq uint64
	UpdatedSeq uint64
}

// AuditEntry is one row of the append-only audit log.
type AuditEntry struct {
	Seq    uint64
	OpID   string
	From   State
	To     State
	Event  string
	Detail string
	At     time.Time
}

// RegisterStatus is a point-in-time view of the register.
type RegisterStatus struct {
	LastSeq       uint64
	Holds         int
	Charged       int
	ByState       map[State]int
	Suspect       bool
	SuspectReason string
}

type options struct {
	verifier EvidenceVerifier
	now      func() time.Time
}

// Option customizes a Register.
type Option func(*options)

// WithEvidenceVerifier plugs in the (external, out of scope) verification
// boundary for AbsenceEvidence. Passing a nil verifier leaves every release
// refused with ErrEvidenceUnimplemented, which is the default and only
// honest configuration for this worktree.
func WithEvidenceVerifier(v EvidenceVerifier) Option {
	return func(o *options) { o.verifier = v }
}

// WithClock overrides the wall clock, for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// Register is a durable, SQLite-backed hold register. It is safe for
// concurrent use; every mutation runs in its own BEGIN IMMEDIATE
// transaction so the hold row and its audit rows commit atomically.
type Register struct {
	db       *sql.DB
	verifier EvidenceVerifier
	now      func() time.Time
}

func dsn(path string) string {
	return fmt.Sprintf("%s?_txlock=immediate&_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
}

// Open opens (initializing if needed) the register at path.
func Open(path string, opts ...Option) (*Register, error) {
	return open(path, nil, opts...)
}

// OpenWithExpectedSeq opens the register and additionally checks an
// out-of-band monotonic expectation: the caller passes the LastSeq value it
// observed before a restart. If the recorded sequence does not match (for
// example because the database file was restored from an older snapshot),
// the register is opened in suspect mode: reads are allowed, every mutation
// is refused with ErrSuspect, and the charge view still counts all holds
// that exist in the rewound file. Without an external expectation like this
// a whole-file restore is undetectable from inside the database; see the
// package documentation.
func OpenWithExpectedSeq(path string, expected uint64, opts ...Option) (*Register, error) {
	return open(path, &expected, opts...)
}

func open(path string, expectedSeq *uint64, opts ...Option) (*Register, error) {
	o := &options{now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(o)
	}
	db, err := sql.Open("sqlite3", dsn(path))
	if err != nil {
		return nil, err
	}
	r := &Register{db: db, verifier: o.verifier, now: o.now}
	if err := r.init(); err != nil {
		db.Close()
		return nil, err
	}
	if err := r.checkIntegrity(expectedSeq); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

func (r *Register) Close() error { return r.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS hold_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_seq INTEGER NOT NULL,
    suspect INTEGER NOT NULL DEFAULT 0,
    suspect_reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS holds (
    op_id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    desired TEXT NOT NULL DEFAULT '{}',
    created_seq INTEGER NOT NULL,
    updated_seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS hold_audit (
    seq INTEGER PRIMARY KEY,
    op_id TEXT NOT NULL,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    event TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL
);
`

func (r *Register) init() error {
	_, err := r.db.Exec(schema)
	return err
}

// checkIntegrity validates the audit log and applies any caller-supplied
// rewind expectation, quarantining the register instead of failing the
// open when inconsistency is found.
func (r *Register) checkIntegrity(expectedSeq *uint64) error {
	ctx := context.Background()
	return r.tx(ctx, func(tx *sql.Tx) error {
		var lastSeq int64
		var suspect int
		var reason string
		err := tx.QueryRow(`SELECT last_seq, suspect, suspect_reason FROM hold_meta WHERE id = 1`).Scan(&lastSeq, &suspect, &reason)
		if err == sql.ErrNoRows {
			if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM hold_audit`).Scan(&lastSeq); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO hold_meta (id, last_seq) VALUES (1, ?)`, lastSeq); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// Intra-database consistency: the audit log is append-only and
		// dense, so its row count must equal the last allocated sequence.
		// A discontinuity means the file is not a state this register
		// could have committed.
		var count int
		var maxSeq int64
		if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(seq),0) FROM hold_audit`).Scan(&count, &maxSeq); err != nil {
			return err
		}
		if int64(count) != maxSeq || maxSeq != lastSeq {
			return r.quarantine(tx, fmt.Sprintf("audit log discontinuity: count=%d max=%d last=%d", count, maxSeq, lastSeq))
		}
		if expectedSeq != nil && *expectedSeq != uint64(lastSeq) {
			return r.quarantine(tx, fmt.Sprintf("%v: expected seq %d, recorded %d", ErrStateRewound, *expectedSeq, lastSeq))
		}
		return nil
	})
}

func (r *Register) quarantine(tx *sql.Tx, reason string) error {
	_, err := tx.Exec(`UPDATE hold_meta SET suspect = 1, suspect_reason = ? WHERE id = 1`, reason)
	return err
}

// tx runs fn inside a BEGIN IMMEDIATE transaction (via the _txlock DSN
// parameter), committing on nil error and rolling back otherwise. Callers
// that must durably record a domain-level denial (stale receipt, refused
// release) manage their own transaction instead.
func (r *Register) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// nextSeq allocates the next global audit sequence inside the current
// transaction, keeping the allocator and the audit log atomic.
func nextSeq(tx *sql.Tx) (uint64, error) {
	if _, err := tx.Exec(`UPDATE hold_meta SET last_seq = last_seq + 1 WHERE id = 1`); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRow(`SELECT last_seq FROM hold_meta WHERE id = 1`).Scan(&seq); err != nil {
		return 0, err
	}
	return uint64(seq), nil
}

func (r *Register) audit(tx *sql.Tx, seq uint64, opID string, from, to State, event, detail string) error {
	_, err := tx.Exec(
		`INSERT INTO hold_audit (seq, op_id, from_state, to_state, event, detail, at) VALUES (?,?,?,?,?,?,?)`,
		seq, opID, string(from), string(to), event, detail, r.now().Format(time.RFC3339Nano))
	return err
}

func (r *Register) guardSuspect(tx *sql.Tx) error {
	var suspect int
	if err := tx.QueryRow(`SELECT suspect FROM hold_meta WHERE id = 1`).Scan(&suspect); err != nil {
		return err
	}
	if suspect != 0 {
		return ErrSuspect
	}
	return nil
}

// loadHold reads a hold inside the current transaction.
func loadHold(tx *sql.Tx, opID string) (Hold, error) {
	var h Hold
	var desired string
	err := tx.QueryRow(
		`SELECT state, outcome, revision, desired, created_seq, updated_seq FROM holds WHERE op_id = ?`, opID,
	).Scan(&h.State, &h.Outcome, &h.Revision, &desired, &h.CreatedSeq, &h.UpdatedSeq)
	if err == sql.ErrNoRows {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	h.OpID = opID
	if err := json.Unmarshal([]byte(desired), &h.Desired); err != nil {
		return h, err
	}
	return h, nil
}

// Charge records a new hold in state pending. One charge per operation id;
// re-charging an existing (even settled) operation is an invalid transition
// — the audit trail of an operation must stay single-threaded.
func (r *Register) Charge(ctx context.Context, opID string, desired map[string]string) error {
	if opID == "" {
		return fmt.Errorf("capacityhold: empty operation id")
	}
	if desired == nil {
		desired = map[string]string{}
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	return r.tx(ctx, func(tx *sql.Tx) error {
		if err := r.guardSuspect(tx); err != nil {
			return err
		}
		if _, err := loadHold(tx, opID); err == nil {
			return fmt.Errorf("%w: operation %q already charged", ErrInvalidTransition, opID)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		seq, err := nextSeq(tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO holds (op_id, state, outcome, revision, desired, created_seq, updated_seq) VALUES (?,?,?,?,?,?,?)`,
			opID, string(StatePending), string(OutcomeNone), 0, string(raw), seq, seq); err != nil {
			return err
		}
		return r.audit(tx, seq, opID, "", StatePending, EventCharged, string(raw))
	})
}

// MarkDispatched records that the operation's mutating write was
// dispatched. The charge remains held: dispatch is not completion.
func (r *Register) MarkDispatched(ctx context.Context, opID string) error {
	return r.event(ctx, opID, EventDispatched, "", func(h Hold) error {
		if h.State == StateSettled {
			return ErrInvalidTransition
		}
		return nil
	})
}

// Acknowledge records a successful acknowledgement for a pending hold. The
// charge REMAINS held: an ack is not physical completion. An ack arriving
// after the hold went unknown (the classic lost-acknowledgement race: the
// response was lost, the operation was marked unknown, the response
// eventually arrives) is recorded and ignored — it never clears unknown.
func (r *Register) Acknowledge(ctx context.Context, opID string) error {
	return r.event(ctx, opID, EventAcknowledged, "", func(h Hold) error {
		switch h.State {
		case StatePending:
			return nil
		case StateUnknown:
			return errLateAck
		case StateSettled:
			return ErrInvalidTransition
		}
		return nil
	})
}

// event applies a state-preserving audited event via the shared transition
// helper.
func (r *Register) event(ctx context.Context, opID, event, detail string, check func(Hold) error) error {
	return r.transition(ctx, opID, detail, func(h Hold) (State, Outcome, string, error) {
		if check != nil {
			if err := check(h); err != nil {
				if errors.Is(err, errLateAck) {
					return h.State, h.Outcome, EventLateAckIgnored, nil
				}
				return h.State, h.Outcome, event, err
			}
		}
		return h.State, h.Outcome, event, nil
	})
}

// MarkUnknown moves a pending hold to unknown: the acknowledgement was lost
// or the outcome became ambiguous. Re-marking unknown is allowed; clearing
// unknown requires admissible evidence, which observations never are.
func (r *Register) MarkUnknown(ctx context.Context, opID, reason string) error {
	return r.transition(ctx, opID, reason, func(h Hold) (State, Outcome, string, error) {
		switch h.State {
		case StatePending, StateUnknown:
			return StateUnknown, h.Outcome, EventMarkUnknown, nil
		default:
			return h.State, h.Outcome, EventMarkUnknown, ErrInvalidTransition
		}
	})
}

// RecordObservation records an external observation and applies the
// admissibility rules: observations of kind read, provider_not_found,
// delete_api_result, db_flag_flip, list_absent and receipt can NEVER
// release a charge by themselves. A matching read never clears unknown
// (rule 1). A receipt referencing an old hold revision is actively
// rejected with ErrStaleReceipt; an up-to-date receipt is recorded as
// inadmissible (rule 2: a signed assertion without independent
// verification proves nothing). Rejections and inadmissible observations
// are durably audited, then the domain outcome is returned.
func (r *Register) RecordObservation(ctx context.Context, opID string, obs Observation) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		tx.Rollback()
		return err
	}
	if err := r.guardSuspect(tx); err != nil {
		return fail(err)
	}
	h, err := loadHold(tx, opID)
	if err != nil {
		return fail(err)
	}
	if h.State == StateSettled {
		return fail(ErrInvalidTransition)
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return fail(err)
	}
	event, detail := obs.auditFields(h)
	if err := r.audit(tx, seq, opID, h.State, h.State, event, detail); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(`UPDATE holds SET updated_seq = ? WHERE op_id = ?`, seq, opID); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if event == EventReceiptRejectedStale {
		return ErrStaleReceipt
	}
	return nil
}

// auditFields maps an observation to its audit event and detail, applying
// the receipt revision policy.
func (obs Observation) auditFields(h Hold) (event, detail string) {
	switch obs.Kind {
	case ObsRead:
		return EventObsReadInadmissible, fmt.Sprintf("matches_desired=%t %s", obs.MatchesDesired, obs.Detail)
	case ObsReceipt:
		if obs.Receipt != nil && obs.Receipt.Revision < h.Revision {
			return EventReceiptRejectedStale, fmt.Sprintf("receipt_revision=%d hold_revision=%d %s", obs.Receipt.Revision, h.Revision, obs.Detail)
		}
		rev := uint64(0)
		if obs.Receipt != nil {
			rev = obs.Receipt.Revision
		}
		return EventReceiptInadmissible, fmt.Sprintf("receipt_revision=%d hold_revision=%d inadmissible_without_verification %s", rev, h.Revision, obs.Detail)
	default:
		return EventObsInadmissible, fmt.Sprintf("kind=%s %s", obs.Kind, obs.Detail)
	}
}

// transition is the write path for state changes: hold row update and
// audit row commit in the same immediate transaction. decide returns the
// target state/outcome and audit event, or an error to abort (which rolls
// the whole attempt back, audit included — aborted attempts never happened).
func (r *Register) transition(ctx context.Context, opID, detail string, decide func(Hold) (State, Outcome, string, error)) error {
	return r.tx(ctx, func(tx *sql.Tx) error {
		if err := r.guardSuspect(tx); err != nil {
			return err
		}
		h, err := loadHold(tx, opID)
		if err != nil {
			return err
		}
		to, outcome, event, err := decide(h)
		if err != nil {
			return err
		}
		seq, err := nextSeq(tx)
		if err != nil {
			return err
		}
		if err := r.audit(tx, seq, opID, h.State, to, event, detail); err != nil {
			return err
		}
		if to != h.State || outcome != h.Outcome {
			_, err = tx.Exec(
				`UPDATE holds SET state = ?, outcome = ?, revision = revision + 1, updated_seq = ? WHERE op_id = ?`,
				string(to), string(outcome), seq, opID)
		} else {
			_, err = tx.Exec(`UPDATE holds SET updated_seq = ? WHERE op_id = ?`, seq, opID)
		}
		return err
	})
}

// Release settles a charge using the only admissible evidence class: an
// independently fenced absence proof verified by the external enforcement
// boundary (see EvidenceVerifier), which is OUT OF SCOPE for this package.
// Without a registered verifier the release is audited as denied and
// refused with ErrEvidenceUnimplemented; unknown states remain charged.
// Denials are durably audited: the audit row commits even though the
// release is refused.
func (r *Register) Release(ctx context.Context, opID string, ev AbsenceEvidence) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		tx.Rollback()
		return err
	}
	if err := r.guardSuspect(tx); err != nil {
		return fail(err)
	}
	h, err := loadHold(tx, opID)
	if err != nil {
		return fail(err)
	}
	if h.State == StateSettled {
		return fail(ErrInvalidTransition)
	}
	seq, err := nextSeq(tx)
	if err != nil {
		return fail(err)
	}
	// commitDeny durably records a refused release: the audit row commits
	// even though the release itself is denied.
	commitDeny := func(event, detail string, denyErr error) error {
		if err := r.audit(tx, seq, opID, h.State, h.State, event, detail); err != nil {
			return fail(err)
		}
		if _, err := tx.Exec(`UPDATE holds SET updated_seq = ? WHERE op_id = ?`, seq, opID); err != nil {
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return denyErr
	}
	if ev == nil {
		return commitDeny(EventReleaseDeniedInadmissible, "nil evidence", ErrInadmissibleEvidence)
	}
	if ev.OperationID() != opID {
		return commitDeny(EventReleaseDeniedInadmissible,
			fmt.Sprintf("evidence op %q != hold op %q", ev.OperationID(), opID), ErrEvidenceMismatch)
	}
	if r.verifier == nil {
		return commitDeny(EventReleaseDeniedNoVerifier,
			fmt.Sprintf("kind=%s fence=%s: %v", ev.Kind(), ev.Fence().FenceID, ErrEvidenceUnimplemented), ErrEvidenceUnimplemented)
	}
	if err := r.verifier.VerifyAbsence(ctx, ev); err != nil {
		return commitDeny(EventReleaseDeniedVerifyFailed, err.Error(), fmt.Errorf("%w: %v", ErrVerifierRejected, err))
	}
	if err := r.audit(tx, seq, opID, h.State, StateSettled, EventReleased,
		fmt.Sprintf("kind=%s fence=%s", ev.Kind(), ev.Fence().FenceID)); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(
		`UPDATE holds SET state = ?, outcome = ?, revision = revision + 1, updated_seq = ? WHERE op_id = ?`,
		string(StateSettled), string(OutcomeReleasedProvenAbsent), seq, opID); err != nil {
		return fail(err)
	}
	return tx.Commit()
}

// State returns the current hold record.
func (r *Register) State(ctx context.Context, opID string) (Hold, error) {
	var h Hold
	err := r.tx(ctx, func(tx *sql.Tx) error {
		var err error
		h, err = loadHold(tx, opID)
		return err
	})
	return h, err
}

// AuditTrail returns the full append-only history of one hold, ordered by
// sequence.
func (r *Register) AuditTrail(ctx context.Context, opID string) ([]AuditEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT seq, op_id, from_state, to_state, event, detail, at FROM hold_audit WHERE op_id = ? ORDER BY seq`, opID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.Seq, &e.OpID, &e.From, &e.To, &e.Event, &e.Detail, &at); err != nil {
			return nil, err
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Charged returns the number of holds that still carry a charge, i.e.
// every hold not in state settled. Unknown states are charged.
func (r *Register) Charged(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM holds WHERE state != ?`, string(StateSettled)).Scan(&n)
	return n, err
}

// Status returns a point-in-time register view, including the quarantine
// flag and its reason.
func (r *Register) Status(ctx context.Context) (RegisterStatus, error) {
	var st RegisterStatus
	err := r.tx(ctx, func(tx *sql.Tx) error {
		var suspect int
		if err := tx.QueryRow(`SELECT last_seq, suspect, suspect_reason FROM hold_meta WHERE id = 1`).Scan(&st.LastSeq, &suspect, &st.SuspectReason); err != nil {
			return err
		}
		st.Suspect = suspect != 0
		rows, err := tx.Query(`SELECT state, COUNT(*) FROM holds GROUP BY state`)
		if err != nil {
			return err
		}
		defer rows.Close()
		st.ByState = map[State]int{}
		for rows.Next() {
			var s string
			var n int
			if err := rows.Scan(&s, &n); err != nil {
				return err
			}
			st.ByState[State(s)] = n
			st.Holds += n
			if State(s) != StateSettled {
				st.Charged += n
			}
		}
		return rows.Err()
	})
	return st, err
}
