// Durable, inactive-by-default controller state for stock pool settings
// changes. A proposal is metadata the operator reviewed; it grants no write
// authority. The only paths out of an unresolved operation are an
// independently proven outcome or explicit operator withdrawal before
// dispatch. A settings value matching the old or new value is an observation,
// never completion evidence.
package brokerinput

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrStockApplyJournal     = errors.New("stock apply journal unavailable")
	ErrStockApplyUnavailable = errors.New("stock apply capability unavailable")
	ErrStockApplyPaused      = errors.New("stock apply paused for maintenance")
	ErrStockApplyBlocked     = errors.New("unresolved stock apply operation blocks new work")
	ErrStockApplyStale       = errors.New("stock apply evidence is stale")
	ErrStockApplyConflict    = errors.New("stock apply operation conflict")
)

const stockApplyApplicationID = 0x47434131

const stockApplySchema = `
CREATE TABLE stock_apply_identity (
 id INTEGER PRIMARY KEY CHECK(id=1), lineage TEXT NOT NULL CHECK(length(lineage)=64)
) STRICT;
CREATE TABLE stock_apply_binding (
 id INTEGER PRIMARY KEY CHECK(id=1), identity TEXT NOT NULL CHECK(length(identity)=64)
) STRICT;
CREATE TABLE stock_apply_mode (
 scope TEXT PRIMARY KEY, paused INTEGER NOT NULL CHECK(paused IN (0,1)), reason TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE stock_apply_operation (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 operation_id TEXT UNIQUE NOT NULL,
 scope TEXT NOT NULL,
 pool_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 source TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 observed INTEGER NOT NULL,
 suggested INTEGER NOT NULL,
 charged INTEGER NOT NULL,
 direction TEXT NOT NULL CHECK(direction IN ('increase','decrease')),
 plan_expires_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('proposed','dispatched','outcome_unknown','applied_when_proven','withdrawn')),
 drift INTEGER NOT NULL CHECK(drift IN (0,1)) DEFAULT 0,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX stock_apply_one_unresolved ON stock_apply_operation(scope) WHERE state IN ('proposed','dispatched','outcome_unknown');
CREATE TABLE stock_apply_audit (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 scope TEXT NOT NULL,
 operation_id TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL,
 recorded_at TEXT NOT NULL,
 detail TEXT NOT NULL
) STRICT;
PRAGMA application_id=1195589937;
PRAGMA user_version=1;
`

// StockApplyState is the conservative operator-facing controller state.
type StockApplyState string

const (
	StockApplyStateUnavailable       StockApplyState = "unavailable"
	StockApplyStateProposed          StockApplyState = "proposed"
	StockApplyStatePausedMaintenance StockApplyState = "paused_maintenance"
	StockApplyStateAppliedWhenProven StockApplyState = "applied_when_proven"
	StockApplyStateOutcomeUnknown    StockApplyState = "outcome_unknown"
)

const (
	stockApplyReasonPauseActive       = "operator_pause_active"
	stockApplyReasonNoOperation       = "no_recorded_operation"
	stockApplyReasonAwaitingReceipt   = "dispatched_awaiting_authoritative_receipt"
	stockApplyReasonOutcomeUncertain  = "outcome_uncertain_pending_resolution"
	stockApplyReasonProven            = "outcome_proven_by_authoritative_receipt"
	stockApplyReasonNoAdapter         = "no_supported_adapter_implemented"
	stockApplyReasonStaleEvidence     = "stale_evidence"
	stockApplyReasonDriftDetected     = "settings_drift_detected"
	stockApplyReasonAlreadyPaused     = "already_paused"
	stockApplyReasonNotPaused         = "not_paused"
	stockApplyReasonReceiptResolution = "receipt_resolution_unavailable"
	stockApplyReasonRollbackSafety    = "rollback_requires_original_outcome_and_current_state"
	stockApplyReasonPauseLiftOnly     = "lifts_local_pause_only"
	stockApplyReasonPauseLiftFirst    = "pause_must_be_lifted_first"
	stockApplyReasonNoOpenOperation   = "no_open_operation"
	stockApplyReasonOperationProven   = "operation_already_proven"
	stockApplyReasonWithdrawNone      = "no_proposal_to_withdraw"
	stockApplyReasonDriftFlag         = "observed_settings_differ_from_journaled_operation"
)

const (
	stockApplyChargedProposed  = "a proposal changes nothing upstream; the configured maximum and all existing runner charges remain fully counted"
	stockApplyChargedUncertain = "an interrupted or unproven change releases nothing; the prior configured maximum and all runner charges remain counted until authoritative reconciliation proves absence"
	stockApplyChargedProven    = "a proven settings change is not teardown evidence; runner charges remain counted until physical absence is independently reconciled"
	stockApplyChargedBaseline  = "no recorded apply operation; ledger accounting is unchanged"
)

type stockApplyJournal struct {
	db      *sql.DB
	lineage string
}

// InitializeStockApplyJournal creates a NEW owner-private journal exactly
// once. Initialization is first-install setup, never recovery: existing
// paths, including empty files, are never adopted. Save Lineage in
// independent trusted configuration before first use.
func InitializeStockApplyJournal(path string) (*stockApplyJournal, error) {
	absolute, err := stockApplyPath(path)
	if err != nil {
		return nil, err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return nil, ErrStockApplyJournal
	}
	lineage := hex.EncodeToString(entropy[:])
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(absolute + suffix); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrStockApplyJournal
		}
	}
	fd, err := syscall.Open(absolute, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrStockApplyJournal
	}
	if syscall.Close(fd) != nil {
		return nil, ErrStockApplyJournal
	}
	journal, err := openStockApplyFile(absolute, lineage, true)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(absolute))
	if err != nil {
		journal.Close()
		return nil, ErrStockApplyJournal
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		journal.Close()
		return nil, ErrStockApplyJournal
	}
	return journal, nil
}

// OpenStockApplyJournal opens ONLY an existing owner-private journal matching
// the independently configured lineage. A missing, empty, replaced or
// swapped journal fails before any write: restrictive state must never
// silently bootstrap fresh.
func OpenStockApplyJournal(path, expectedLineage string) (*stockApplyJournal, error) {
	if !validStockApplyLineage(expectedLineage) {
		return nil, ErrStockApplyJournal
	}
	absolute, err := stockApplyPath(path)
	if err != nil {
		return nil, err
	}
	return openStockApplyFile(absolute, expectedLineage, false)
}

func (j *stockApplyJournal) Lineage() string { return j.lineage }

func validStockApplyLineage(value string) bool {
	if len(value) != 64 {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(raw) == value
}

func stockApplyPath(path string) (string, error) {
	if path == "" {
		return "", ErrStockApplyJournal
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", ErrStockApplyJournal
	}
	parent, err := os.Lstat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return "", ErrStockApplyJournal
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", ErrStockApplyJournal
	}
	return absolute, nil
}

func openStockApplyFile(absolute, expected string, initialize bool) (*stockApplyJournal, error) {
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrStockApplyJournal
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrStockApplyJournal
	}
	location := url.URL{Scheme: "file", Path: absolute}
	location.RawQuery = "mode=ro"
	check, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, ErrStockApplyJournal
	}
	var app, version, tables int
	err = check.QueryRow("PRAGMA application_id").Scan(&app)
	if err == nil {
		err = check.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err == nil && initialize {
		err = check.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&tables)
	}
	var observed string
	if err == nil && !initialize {
		err = check.QueryRow("SELECT lineage FROM stock_apply_identity WHERE id=1").Scan(&observed)
	}
	closeErr := check.Close()
	if err != nil || closeErr != nil {
		return nil, ErrStockApplyJournal
	}
	if initialize {
		if app != 0 || version != 0 || tables != 0 {
			return nil, ErrStockApplyJournal
		}
	} else if app != stockApplyApplicationID || version != 1 || observed != expected {
		return nil, ErrStockApplyJournal
	}
	query := url.Values{"mode": {"rw"}, "_busy_timeout": {"5000"}, "_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_foreign_keys": {"on"}, "_txlock": {"immediate"}}
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, ErrStockApplyJournal
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	j := &stockApplyJournal{db: db, lineage: expected}
	err = j.transaction(context.Background(), func(tx *sql.Tx) error {
		var mode string
		var synchronous, foreignKeys int
		for query, target := range map[string]any{"PRAGMA application_id": &app, "PRAGMA user_version": &version, "PRAGMA journal_mode": &mode, "PRAGMA synchronous": &synchronous, "PRAGMA foreign_keys": &foreignKeys} {
			if tx.QueryRow(query).Scan(target) != nil {
				return ErrStockApplyJournal
			}
		}
		if mode != "wal" || synchronous != 2 || foreignKeys != 1 {
			return ErrStockApplyJournal
		}
		if initialize {
			if app != 0 || version != 0 {
				return ErrStockApplyJournal
			}
			if tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&tables) != nil || tables != 0 {
				return ErrStockApplyJournal
			}
			if _, err := tx.Exec(stockApplySchema); err != nil {
				return ErrStockApplyJournal
			}
			_, err := tx.Exec("INSERT INTO stock_apply_identity(id,lineage) VALUES(1,?)", expected)
			return err
		}
		if app != stockApplyApplicationID || version != 1 || tx.QueryRow("SELECT lineage FROM stock_apply_identity WHERE id=1").Scan(&observed) != nil || observed != expected {
			return ErrStockApplyJournal
		}
		_, err := tx.Exec("SELECT scope,paused,reason,updated_at FROM stock_apply_mode LIMIT 0")
		if err != nil {
			return err
		}
		_, err = tx.Exec("SELECT operation_id,scope,pool_id,repository_id,source,fingerprint,observed,suggested,charged,direction,plan_expires_at,state,drift,created_at,updated_at FROM stock_apply_operation LIMIT 0")
		return err
	})
	if err != nil {
		db.Close()
		return nil, ErrStockApplyJournal
	}
	return j, nil
}

func (j *stockApplyJournal) Close() error { return j.db.Close() }

func (j *stockApplyJournal) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrStockApplyJournal
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrStockApplyJournal
	}
	return nil
}

func stockApplyAudit(tx *sql.Tx, scope, operationID, kind, detail string) error {
	_, err := tx.Exec("INSERT INTO stock_apply_audit(scope,operation_id,kind,recorded_at,detail) VALUES(?,?,?,?,?)", scope, operationID, kind, time.Now().UTC().Format(time.RFC3339Nano), detail)
	return err
}

func stockApplyReasonText(value string, max int) bool {
	if len(value) > max || strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

// StockApplyController is the durable, single-writer controller state. All
// mutations and their audit rows commit in one BEGIN IMMEDIATE transaction.
type StockApplyController struct {
	journal *stockApplyJournal
	gate    chan struct{}
}

// NewStockApplyController binds a freshly initialized journal to one
// controller instance identity (64 lowercase hex, e.g. the SHA-256 of an
// instance identifier saved in trusted configuration). Rebinding to a
// different identity, or binding after operations exist, fails closed.
func NewStockApplyController(journal *stockApplyJournal, identity string) (*StockApplyController, error) {
	if journal == nil || len(identity) != 64 {
		return nil, ErrStockApplyJournal
	}
	if _, err := hex.DecodeString(identity); err != nil {
		return nil, ErrStockApplyJournal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if journal.bind(ctx, identity) != nil {
		return nil, ErrStockApplyJournal
	}
	return &StockApplyController{journal: journal, gate: make(chan struct{}, 1)}, nil
}

// OpenStockApplyController opens an existing bound journal. The identity must
// match the durable binding so a restrictive state cannot be silently adopted
// by a different controller instance.
func OpenStockApplyController(journal *stockApplyJournal, identity string) (*StockApplyController, error) {
	if journal == nil || len(identity) != 64 {
		return nil, ErrStockApplyJournal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if journal.checkBinding(ctx, identity) != nil {
		return nil, ErrStockApplyJournal
	}
	return &StockApplyController{journal: journal, gate: make(chan struct{}, 1)}, nil
}

func (j *stockApplyJournal) bind(ctx context.Context, identity string) error {
	return j.transaction(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRow("SELECT identity FROM stock_apply_binding WHERE id=1").Scan(&old)
		if err == nil {
			if old != identity {
				return ErrStockApplyJournal
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyJournal
		}
		var operations, events int
		if tx.QueryRow("SELECT count(*) FROM stock_apply_operation").Scan(&operations) != nil || operations != 0 ||
			tx.QueryRow("SELECT count(*) FROM stock_apply_audit").Scan(&events) != nil || events != 0 {
			return ErrStockApplyJournal
		}
		_, err = tx.Exec("INSERT INTO stock_apply_binding(id,identity) VALUES(1,?)", identity)
		return err
	})
}

func (j *stockApplyJournal) checkBinding(ctx context.Context, identity string) error {
	return j.transaction(ctx, func(tx *sql.Tx) error {
		var old string
		if tx.QueryRow("SELECT identity FROM stock_apply_binding WHERE id=1").Scan(&old) != nil || old != identity {
			return ErrStockApplyJournal
		}
		return nil
	})
}

func (c *StockApplyController) enter(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrStockApplyJournal
	}
}
func (c *StockApplyController) leave() { <-c.gate }

func (c *StockApplyController) Close() error { return c.journal.Close() }

// StockApplyOperation is the durable, sanitized record of one reviewed
// change proposal and its conservative lifecycle. It never contains
// credentials, raw upstream payloads or provider data.
type StockApplyOperation struct {
	OperationID  string    `json:"operation_id"`
	Scope        string    `json:"scope"`
	PoolID       string    `json:"pool_id"`
	RepositoryID string    `json:"repository_id"`
	Source       string    `json:"source"`
	Fingerprint  string    `json:"fingerprint"`
	Observed     int64     `json:"observed"`
	Suggested    int64     `json:"suggested"`
	Charged      int64     `json:"charged"`
	Direction    string    `json:"direction"`
	State        string    `json:"state"`
	Drift        bool      `json:"drift"`
	PlanExpires  time.Time `json:"plan_expires_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func scanStockApplyOperation(row interface{ Scan(...any) error }) (StockApplyOperation, error) {
	var op StockApplyOperation
	var scope, expires, created, updated string
	var drift int
	err := row.Scan(&op.OperationID, &scope, &op.PoolID, &op.RepositoryID, &op.Source, &op.Fingerprint,
		&op.Observed, &op.Suggested, &op.Charged, &op.Direction, &expires, &op.State, &drift, &created, &updated)
	op.Scope = scope
	op.Drift = drift == 1
	op.PlanExpires, _ = time.Parse(time.RFC3339Nano, expires)
	op.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	op.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return op, err
}

const stockApplyOperationColumns = "operation_id,scope,pool_id,repository_id,source,fingerprint,observed,suggested,charged,direction,plan_expires_at,state,drift,created_at,updated_at"

type stockApplyScopeState struct {
	paused       bool
	modeRecorded bool
	operation    *StockApplyOperation
}

// loadStockApplyScope reads both rows inside the caller's SQLite transaction.
// The operator view also shows the last proven change after it leaves recovery.
func loadStockApplyScope(tx *sql.Tx, scope string, includeProven bool) (stockApplyScopeState, error) {
	var state stockApplyScopeState
	err := tx.QueryRow("SELECT paused FROM stock_apply_mode WHERE scope=?", scope).Scan(&state.paused)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stockApplyScopeState{}, ErrStockApplyJournal
	}
	state.modeRecorded = err == nil
	states := "('proposed','dispatched','outcome_unknown')"
	if includeProven {
		states = "('proposed','dispatched','outcome_unknown','applied_when_proven')"
	}
	row := tx.QueryRow("SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN "+states+" ORDER BY sequence DESC LIMIT 1", scope)
	op, err := scanStockApplyOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return stockApplyScopeState{}, ErrStockApplyJournal
	}
	state.operation = &op
	return state, nil
}

func (j *stockApplyJournal) scopeSnapshot(ctx context.Context, key string, includeProven bool) (stockApplyScopeState, error) {
	var state stockApplyScopeState
	err := j.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = loadStockApplyScope(tx, key, includeProven)
		return err
	})
	return state, err
}

func (s stockApplyScopeState) admitProposal() error {
	if s.paused {
		return ErrStockApplyPaused
	}
	if s.operation != nil {
		return ErrStockApplyBlocked
	}
	return nil
}

// RecordProposal durably records an operator-reviewed, already-validated
// StockChangePlan as a proposal. Recording changes nothing upstream and
// grants no write authority. The plan must still be fresh, must not claim to
// be actionable, and its charged count must come from the same bounded
// capture that produced the plan.
func (c *StockApplyController) RecordProposal(ctx context.Context, plan StockChangePlan, charged int64, now time.Time) error {
	if plan.Actionable || plan.Scope == ([sha256.Size]byte{}) || plan.PoolID == "" || len(plan.PoolID) > 256 ||
		plan.RepositoryID == "" || len(plan.RepositoryID) > 256 || plan.Source == "" || len(plan.Source) > 256 ||
		plan.Fingerprint == "" || len(plan.Fingerprint) > 256 || plan.Sequence < 0 || plan.Revision < 0 ||
		plan.Observed < 1 || plan.Observed > 1000000 || plan.Suggested < 1 || plan.Suggested > 1000000 ||
		(plan.Delta != 1 && plan.Delta != -1) || plan.Suggested-plan.Observed != plan.Delta ||
		(plan.Direction != "increase" && plan.Direction != "decrease") ||
		(plan.Direction == "increase") != (plan.Delta > 0) || charged < 0 || charged > 100000 {
		return ErrStockApplyConflict
	}
	if now.IsZero() || !plan.ExpiresAt.After(now) {
		return ErrStockApplyStale
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		key := hex.EncodeToString(plan.Scope[:])
		state, err := loadStockApplyScope(tx, key, false)
		if err != nil {
			return err
		}
		if err := state.admitProposal(); err != nil {
			return err
		}
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return ErrStockApplyJournal
		}
		operationID := hex.EncodeToString(entropy[:])
		stamp := now.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec("INSERT INTO stock_apply_operation(operation_id,scope,pool_id,repository_id,source,fingerprint,observed,suggested,charged,direction,plan_expires_at,state,drift,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,'proposed',0,?,?)",
			operationID, key, plan.PoolID, plan.RepositoryID, plan.Source, plan.Fingerprint,
			plan.Observed, plan.Suggested, charged, plan.Direction, plan.ExpiresAt.UTC().Format(time.RFC3339Nano), stamp, stamp); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, operationID, "proposal_recorded", plan.PoolID)
	})
}

// WithdrawProposal removes a never-dispatched proposal after explicit
// operator review. It refuses to touch a dispatched or unknown operation:
// withdrawal is not recovery.
func (c *StockApplyController) WithdrawProposal(ctx context.Context, scope [sha256.Size]byte, reason string, now time.Time) error {
	if scope == ([sha256.Size]byte{}) || now.IsZero() || !stockApplyReasonText(reason, 256) {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var paused int
		err := tx.QueryRow("SELECT paused FROM stock_apply_mode WHERE scope=?", key).Scan(&paused)
		if err == nil && paused == 1 {
			return ErrStockApplyPaused
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyJournal
		}
		var state, operationID string
		err = tx.QueryRow("SELECT state,operation_id FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key).Scan(&state, &operationID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyConflict
		}
		if err != nil {
			return ErrStockApplyJournal
		}
		if state != "proposed" {
			return ErrStockApplyBlocked
		}
		if _, err := tx.Exec("UPDATE stock_apply_operation SET state='withdrawn',updated_at=? WHERE operation_id=?", now.UTC().Format(time.RFC3339Nano), operationID); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, operationID, "proposal_withdrawn", reason)
	})
}

// Pause enters durable maintenance mode for the scope. It takes effect for
// new work only and never modifies an existing operation: pending and unknown
// states are preserved exactly.
func (c *StockApplyController) Pause(ctx context.Context, scope [sha256.Size]byte, reason string, now time.Time) error {
	if scope == ([sha256.Size]byte{}) || now.IsZero() || !stockApplyReasonText(reason, 256) {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		return writeStockApplyMode(tx, key, true, "pause_entered", reason, now)
	})
}

// Resume exits maintenance mode. Resume only restores the ability to record
// new proposals; it never resolves, clears or reclassifies a pending or
// unknown operation.
func (c *StockApplyController) Resume(ctx context.Context, scope [sha256.Size]byte, reason string, now time.Time) error {
	if scope == ([sha256.Size]byte{}) || now.IsZero() || !stockApplyReasonText(reason, 256) {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		return writeStockApplyMode(tx, key, false, "pause_exited", reason, now)
	})
}

// MarkDispatched records that a supported adapter accepted the exact
// journaled proposal. It refuses stock capability claims and everything stale,
// drifted or paused; without a qualified adapter this transition is
// unreachable and proposals stay inert metadata.
func (c *StockApplyController) MarkDispatched(ctx context.Context, scope [sha256.Size]byte, claim StockApplyCapabilityClaim, now time.Time) error {
	if scope == ([sha256.Size]byte{}) {
		return ErrStockApplyConflict
	}
	verdict := assessStockApplyCapability(claim)
	if !verdict.Supported {
		return ErrStockApplyUnavailable
	}
	if now.IsZero() {
		return ErrStockApplyJournal
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var paused int
		err := tx.QueryRow("SELECT paused FROM stock_apply_mode WHERE scope=?", key).Scan(&paused)
		if err == nil && paused == 1 {
			return ErrStockApplyPaused
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyJournal
		}
		var op StockApplyOperation
		row := tx.QueryRow("SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key)
		op, err = scanStockApplyOperation(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyBlocked
		}
		if err != nil {
			return ErrStockApplyJournal
		}
		if op.State != "proposed" {
			return ErrStockApplyBlocked
		}
		if !op.PlanExpires.After(now) {
			return ErrStockApplyStale
		}
		if op.Drift {
			return ErrStockApplyConflict
		}
		if _, err := tx.Exec("UPDATE stock_apply_operation SET state='dispatched',updated_at=? WHERE operation_id=?", now.UTC().Format(time.RFC3339Nano), op.OperationID); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, op.OperationID, "dispatched", claim.Interface)
	})
}

// MarkOutcomeUncertain records that a dispatched write can no longer be
// attributed: interruption, timeout without receipt, or restart. The
// operation stays charged and blocked until an independently authoritative
// outcome is proven.
func (c *StockApplyController) MarkOutcomeUncertain(ctx context.Context, scope [sha256.Size]byte, now time.Time) error {
	if scope == ([sha256.Size]byte{}) {
		return ErrStockApplyConflict
	}
	if now.IsZero() {
		return ErrStockApplyJournal
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var state, operationID string
		err := tx.QueryRow("SELECT state,operation_id FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key).Scan(&state, &operationID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyConflict
		}
		if err != nil {
			return ErrStockApplyJournal
		}
		if state == "outcome_unknown" {
			return nil
		}
		if state != "dispatched" {
			return ErrStockApplyConflict
		}
		if _, err := tx.Exec("UPDATE stock_apply_operation SET state='outcome_unknown',updated_at=? WHERE operation_id=?", now.UTC().Format(time.RFC3339Nano), operationID); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, operationID, "outcome_uncertain", "interrupted_without_authoritative_receipt")
	})
}

// StockApplyOutcomeProof binds an independently authoritative outcome to the
// exact journaled operation. The digests are verified evidence references
// supplied by a qualified adapter; their wire format is defined by that
// future adapter, not by this controller.
type StockApplyOutcomeProof struct {
	OperationID     string
	ReceiptDigest   [sha256.Size]byte
	ChallengeDigest [sha256.Size]byte
}

// RecordProvenOutcome is the ONLY transition into applied_when_proven. It
// requires a supported capability and a complete proof bound to the exact
// operation. A readback matching the old or new value can never satisfy it.
func (c *StockApplyController) RecordProvenOutcome(ctx context.Context, scope [sha256.Size]byte, claim StockApplyCapabilityClaim, proof StockApplyOutcomeProof, now time.Time) error {
	if scope == ([sha256.Size]byte{}) {
		return ErrStockApplyConflict
	}
	verdict := assessStockApplyCapability(claim)
	if !verdict.Supported {
		return ErrStockApplyUnavailable
	}
	if now.IsZero() || proof.OperationID == "" || len(proof.OperationID) > 64 ||
		proof.ReceiptDigest == ([sha256.Size]byte{}) || proof.ChallengeDigest == ([sha256.Size]byte{}) {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var op StockApplyOperation
		row := tx.QueryRow("SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown') ORDER BY sequence DESC LIMIT 1", key)
		var err error
		op, err = scanStockApplyOperation(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyConflict
		}
		if err != nil {
			return ErrStockApplyJournal
		}
		if op.OperationID != proof.OperationID {
			return ErrStockApplyConflict
		}
		if op.State != "dispatched" && op.State != "outcome_unknown" {
			return ErrStockApplyBlocked
		}
		if _, err := tx.Exec("UPDATE stock_apply_operation SET state='applied_when_proven',updated_at=? WHERE operation_id=?", now.UTC().Format(time.RFC3339Nano), op.OperationID); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, op.OperationID, "outcome_proven", hex.EncodeToString(proof.ReceiptDigest[:]))
	})
}

// stockApplyReadbackConsistent reports whether one read-only settings value
// agrees with the journaled operation for its state. Consistency is drift
// bookkeeping only; it is never completion evidence.
func stockApplyReadbackConsistent(op StockApplyOperation, readback int64) bool {
	switch op.State {
	case "dispatched", "outcome_unknown":
		return readback == op.Observed || readback == op.Suggested
	case "applied_when_proven":
		return readback == op.Suggested
	default:
		return readback == op.Observed
	}
}

// RecordObservation records one read-only settings observation for the scope's
// open or proven operation. Matching the old or the new value is retained as
// an audit observation only; it never changes the operation state and never
// releases a charge. Charged is the CURRENT ledger-charged runner baseline:
// it is stored durably so the held-capacity display keeps reflecting live
// charging, including baseline movements while an operation is open. A
// readback inconsistent with the journaled operation raises a sticky drift
// flag that blocks dispatch until the operator reviews.
func (c *StockApplyController) RecordObservation(ctx context.Context, scope [sha256.Size]byte, readback, charged int64, now time.Time) error {
	if scope == ([sha256.Size]byte{}) || now.IsZero() || readback < 0 || readback > 1000000 || charged < 0 || charged > 100000 {
		return ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	return c.journal.transaction(ctx, func(tx *sql.Tx) error {
		var op StockApplyOperation
		row := tx.QueryRow("SELECT "+stockApplyOperationColumns+" FROM stock_apply_operation WHERE scope=? AND state IN ('proposed','dispatched','outcome_unknown','applied_when_proven') ORDER BY sequence DESC LIMIT 1", key)
		var err error
		op, err = scanStockApplyOperation(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStockApplyConflict
		}
		if err != nil {
			return ErrStockApplyJournal
		}
		// Do not let an earlier or same-time observation replace the last
		// recorded charge. This orders local records, not their authority.
		if !now.After(op.UpdatedAt) {
			return ErrStockApplyConflict
		}
		consistent := stockApplyReadbackConsistent(op, readback)
		detail := strconv.FormatInt(readback, 10)
		if err := stockApplyAudit(tx, key, op.OperationID, "observation", detail); err != nil {
			return err
		}
		// Advance ordering even when the reported count is unchanged.
		if _, err := tx.Exec("UPDATE stock_apply_operation SET charged=?,updated_at=? WHERE operation_id=?", charged, now.UTC().Format(time.RFC3339Nano), op.OperationID); err != nil {
			return ErrStockApplyJournal
		}
		if charged != op.Charged {
			if err := stockApplyAudit(tx, key, op.OperationID, "charge_updated", strconv.FormatInt(charged, 10)); err != nil {
				return err
			}
		}
		if consistent || op.Drift {
			return nil
		}
		if _, err := tx.Exec("UPDATE stock_apply_operation SET drift=1,updated_at=? WHERE operation_id=?", now.UTC().Format(time.RFC3339Nano), op.OperationID); err != nil {
			return ErrStockApplyJournal
		}
		return stockApplyAudit(tx, key, op.OperationID, "drift_detected", stockApplyReasonDriftFlag)
	})
}

// StockApplyEvaluation carries the fresh, caller-owned evidence used to build
// the operator view. BaselineCharged is the ledger-charged runner count for
// the scope; Readback is the current observed configured maximum from a
// read-only capture, nil when no fresh read exists; Fresh reports whether the
// capture evidence is inside its expiry.
type StockApplyEvaluation struct {
	Now             time.Time
	Claim           StockApplyCapabilityClaim
	BaselineCharged int64
	Readback        *int64
	Fresh           bool
}

// StockApplyActionView exposes one operator command with the exact reason it
// is disabled. No stock path ever enables a mutating action.
type StockApplyActionView struct {
	Action  string `json:"action"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// StockApplyChargedView reports what is still charged and why.
type StockApplyChargedView struct {
	Runners int64  `json:"runners"`
	Reason  string `json:"reason"`
}

// StockApplyResumeView separates the two different things an operator can do
// after maintenance. LiftPause is the LOCAL pause control: available whenever
// the scope is paused, and it never clears or resolves pending or unknown
// state. Ready is readiness to resume APPLY work, which additionally demands
// fresh, drift-free evidence for the open operation. Lifting a pause is
// therefore never presented as readiness to resume apply work.
type StockApplyResumeView struct {
	LiftPause  bool   `json:"lift_pause"`
	LiftReason string `json:"lift_reason,omitempty"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason,omitempty"`
}

// StockApplyReadbackView reports a setting from the evaluation's read-only
// capture. Its freshness belongs to that capture, and it carries no receipt.
type StockApplyReadbackView struct {
	Maximum int64 `json:"maximum"`
	Fresh   bool  `json:"fresh"`
}

// StockApplyView is the conservative operator contract: the controller state,
// why each action is disabled, and what capacity remains charged.
type StockApplyView struct {
	Scope      string                      `json:"scope"`
	State      StockApplyState             `json:"state"`
	Reason     string                      `json:"reason"`
	Paused     bool                        `json:"paused"`
	Drift      bool                        `json:"drift"`
	Operation  *StockApplyOperation        `json:"operation,omitempty"`
	Actions    []StockApplyActionView      `json:"actions"`
	Charged    StockApplyChargedView       `json:"charged"`
	Resume     StockApplyResumeView        `json:"resume"`
	Capability StockApplyCapabilityVerdict `json:"capability"`
	Readback   *StockApplyReadbackView     `json:"readback,omitempty"`
}

// View derives the operator view from durable state and fresh evidence.
// Stale evidence, drift, pause, unknown outcomes and unavailable capability
// all keep every mutating action disabled with a visible reason.
func (c *StockApplyController) View(ctx context.Context, scope [sha256.Size]byte, eval StockApplyEvaluation) (StockApplyView, error) {
	if scope == ([sha256.Size]byte{}) || eval.Now.IsZero() || eval.BaselineCharged < 0 || eval.BaselineCharged > 100000 {
		return StockApplyView{}, ErrStockApplyConflict
	}
	if eval.Readback != nil && (*eval.Readback < 0 || *eval.Readback > 1000000) {
		return StockApplyView{}, ErrStockApplyConflict
	}
	if err := c.enter(ctx); err != nil {
		return StockApplyView{}, err
	}
	defer c.leave()
	key := hex.EncodeToString(scope[:])
	mode, err := c.journal.scopeSnapshot(ctx, key, true)
	if err != nil {
		return StockApplyView{}, err
	}
	op := mode.operation
	verdict := assessStockApplyCapability(eval.Claim)
	view := StockApplyView{
		Scope:      key,
		Paused:     mode.paused,
		Capability: verdict,
		Actions:    []StockApplyActionView{},
	}
	if eval.Readback != nil {
		view.Readback = &StockApplyReadbackView{Maximum: *eval.Readback, Fresh: eval.Fresh}
	}
	switch {
	case mode.paused:
		view.State, view.Reason = StockApplyStatePausedMaintenance, stockApplyReasonPauseActive
	case op == nil:
		view.State, view.Reason = StockApplyStateUnavailable, stockApplyReasonNoOperation
	case op.State == "dispatched":
		view.State, view.Reason = StockApplyStateOutcomeUnknown, stockApplyReasonAwaitingReceipt
	case op.State == "outcome_unknown":
		view.State, view.Reason = StockApplyStateOutcomeUnknown, stockApplyReasonOutcomeUncertain
	case op.State == "applied_when_proven":
		view.State, view.Reason = StockApplyStateAppliedWhenProven, stockApplyReasonProven
	default:
		view.State = StockApplyStateProposed
	}
	if op != nil {
		copy := *op
		view.Operation = &copy
		view.Drift = op.Drift
		view.Charged = StockApplyChargedView{Runners: op.Charged, Reason: map[string]string{
			"proposed":            stockApplyChargedProposed,
			"dispatched":          stockApplyChargedUncertain,
			"outcome_unknown":     stockApplyChargedUncertain,
			"applied_when_proven": stockApplyChargedProven,
		}[op.State]}
		if view.Charged.Reason == "" {
			view.Charged.Reason = stockApplyChargedBaseline
		}
	} else {
		view.Charged = StockApplyChargedView{Runners: eval.BaselineCharged, Reason: stockApplyChargedBaseline}
	}

	firstRefusal := ""
	if !verdict.Supported && len(verdict.Refusals) != 0 {
		firstRefusal = string(verdict.Refusals[0].Code)
	}
	applyReason := func() string {
		switch {
		case mode.paused:
			return stockApplyReasonPauseActive
		case op != nil && (op.State == "dispatched" || op.State == "outcome_unknown"):
			return view.Reason
		case op != nil && op.State == "applied_when_proven":
			return stockApplyReasonProven
		case op != nil && op.Drift:
			return stockApplyReasonDriftDetected
		case !verdict.Supported:
			return firstRefusal
		case !eval.Fresh || (op != nil && !op.PlanExpires.After(eval.Now)):
			return stockApplyReasonStaleEvidence
		default:
			return stockApplyReasonNoAdapter
		}
	}
	withdrawReason := func() string {
		switch {
		case mode.paused:
			return stockApplyReasonPauseActive
		case op == nil:
			return stockApplyReasonWithdrawNone
		case op.State != "proposed":
			return stockApplyReasonOutcomeUncertain
		default:
			return ""
		}
	}
	resolveReason := func() string {
		if mode.paused {
			return stockApplyReasonPauseActive
		}
		return stockApplyReasonReceiptResolution
	}
	rollbackReason := func() string {
		if mode.paused {
			return stockApplyReasonPauseActive
		}
		return stockApplyReasonRollbackSafety
	}
	// The local pause control and apply readiness are different states:
	// lifting a pause is always available while paused and changes nothing
	// about the operation, while readiness additionally requires lifted
	// pause, a known-or-proposed operation, supported capability and fresh
	// drift-free evidence. Ready stays false on every production path.
	view.Resume = StockApplyResumeView{
		LiftPause:  mode.paused,
		LiftReason: map[bool]string{true: "", false: stockApplyReasonNotPaused}[mode.paused],
	}
	switch {
	case mode.paused:
		view.Resume.Reason = stockApplyReasonPauseLiftFirst
	case op == nil:
		view.Resume.Reason = stockApplyReasonNoOpenOperation
	case op.State == "dispatched" || op.State == "outcome_unknown":
		view.Resume.Reason = view.Reason
	case op.State == "applied_when_proven":
		view.Resume.Reason = stockApplyReasonOperationProven
	case !verdict.Supported:
		view.Resume.Reason = firstRefusal
	case op.Drift:
		view.Resume.Reason = stockApplyReasonDriftDetected
	case !eval.Fresh || eval.Readback == nil || !op.PlanExpires.After(eval.Now):
		view.Resume.Reason = stockApplyReasonStaleEvidence
	case !stockApplyReadbackConsistent(*op, *eval.Readback):
		view.Resume.Reason = stockApplyReasonDriftDetected
	default:
		view.Resume.Ready = true
	}
	view.Actions = append(view.Actions,
		StockApplyActionView{Action: "apply", Enabled: false, Reason: applyReason()},
		StockApplyActionView{Action: "withdraw_proposal", Enabled: withdrawReason() == "", Reason: withdrawReason()},
		StockApplyActionView{Action: "pause", Enabled: !mode.paused, Reason: map[bool]string{true: "", false: stockApplyReasonAlreadyPaused}[!mode.paused]},
		StockApplyActionView{Action: "resume", Enabled: mode.paused, Reason: map[bool]string{true: stockApplyReasonPauseLiftOnly, false: stockApplyReasonNotPaused}[mode.paused]},
		StockApplyActionView{Action: "resolve_outcome", Enabled: false, Reason: resolveReason()},
		StockApplyActionView{Action: "rollback", Enabled: false, Reason: rollbackReason()},
	)
	return view, nil
}
