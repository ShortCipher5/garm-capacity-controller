package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Remove every schema10 addition before constructing actual historical fixtures;
// lowering only user_version would test an impossible hybrid schema.
const sqliteTestRemovePoolRouteGuardSchema = `
DROP TRIGGER pool_route_reservation_provenance;
DROP TRIGGER pool_route_plan_provenance;
DROP TRIGGER pool_route_dispatch_provenance;
DROP TRIGGER pool_route_reservation_identity_immutable;
DROP TRIGGER pool_route_dispatch_identity_immutable;
DROP TRIGGER pool_route_source_provenance;
DROP TABLE pool_route_states;
DROP INDEX pool_route_audit_history;
ALTER TABLE reservations DROP COLUMN pool_route_identity_sequence;
ALTER TABLE reservations DROP COLUMN pool_route_revision;
ALTER TABLE dispatches DROP COLUMN pool_route_identity_sequence;
ALTER TABLE dispatches DROP COLUMN pool_route_revision;
`

// This explicit TEST-ONLY state fixture is not a real enable proof or production writer.
// The store installs schema10, but no ACTIVE transition writer is exposed. It exercises canonical history and
// transaction-local revision comparison without granting any external authority.
func routeStateFixture(t *testing.T) (*SQLiteStore, PoolRouteRecord) {
	t.Helper()
	s, r, source := inactiveRouteFixture(t)
	record, err := r.RegisterInactive(context.Background(), source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	return s, record
}
func appendTestOnlyRouteState(t *testing.T, s *SQLiteStore, route PoolRouteRecord, revision int64, mode string) PoolRouteState {
	t.Helper()
	state, err := tryAppendTestOnlyRouteState(s, route, revision, mode)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func tryAppendTestOnlyRouteState(s *SQLiteStore, route PoolRouteRecord, revision int64, mode string) (PoolRouteState, error) {
	var state PoolRouteState
	err := s.transaction(context.Background(), func(tx *sql.Tx) error {
		now := s.now().UTC()
		kind := "pool_route_deactivated"
		if mode == "active" {
			kind = "pool_route_activated"
		}
		if err := sqliteAudit(tx, kind, route.Identity.PoolID, now); err != nil {
			return err
		}
		var sequence int64
		if err := tx.QueryRow("SELECT last_insert_rowid()").Scan(&sequence); err != nil {
			return err
		}
		state = PoolRouteState{Protocol: "pool-route-state/v1", PoolID: route.Identity.PoolID, IdentitySequence: route.Identity.IdentitySequence, Revision: revision, StateSequence: sequence, Mode: mode, Reason: "TEST ONLY fixture state, not external enable authority", RecordedAt: now}
		if mode == "active" {
			var ledgerLineage string
			if err := tx.QueryRow("SELECT lineage_id FROM ledger_lineage WHERE id=1").Scan(&ledgerLineage); err != nil {
				return err
			}
			state.Enable = &PoolRouteEnableReference{LedgerLineage: ledgerLineage, ReviewDigest: bindingHash(fmt.Sprintf("TEST ONLY review %s %d %d", route.Identity.PoolID, route.Identity.IdentitySequence, revision)), ManagementIdentity: route.Identity.ManagementIdentity, JournalLineage: route.Identity.JournalLineage, RequestID: fmt.Sprintf("%08x-0000-4000-8000-000000000001", revision), PoolID: route.Identity.PoolID, Generation: route.Identity.Generation, SpecFingerprint: route.Identity.SpecFingerprint, PoolFingerprint: bindingHash("test enabled fixture"), ObservedAt: now}
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		_, err = tx.Exec("INSERT INTO pool_route_states(pool_id,revision,identity_sequence,state_sequence,mode,payload,payload_hash) VALUES(?,?,?,?,?,?,?)", state.PoolID, state.Revision, state.IdentitySequence, state.StateSequence, state.Mode, string(raw), hex.EncodeToString(sum[:]))
		return err
	})
	return state, err
}
func TestPoolRouteStateOriginalInactiveAndExactCurrentRevision(t *testing.T) {
	s, route := routeStateFixture(t)
	ctx := context.Background()
	original, err := s.LookupPoolRouteState(ctx, route.Identity.PoolID)
	if err != nil || original.Mode != "inactive" || original.Revision != 0 || original.IdentitySequence != route.Identity.IdentitySequence {
		t.Fatal("registration activated", original, err)
	}
	check := func(pool string, id, rev int64) error {
		return s.readTransaction(ctx, func(tx *sql.Tx) error { return requirePoolRouteRevision(tx, pool, id, rev) })
	}
	if err := check(route.Identity.PoolID, route.Identity.IdentitySequence, 0); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("original inactive eligible", err)
	}
	if err := check("app", 0, 0); err != nil {
		t.Fatal("static 0/0 reinterpreted", err)
	}
	if err := check("app", route.Identity.IdentitySequence, 1); !errors.Is(err, ErrPoolRoute) {
		t.Fatal("smuggled static provenance accepted", err)
	}
	active := appendTestOnlyRouteState(t, s, route, 1, "active")
	got, err := s.LookupPoolRouteState(ctx, route.Identity.PoolID)
	if err != nil || !reflect.DeepEqual(active, got) {
		t.Fatal("original fixture history lost", err)
	}
	for _, pair := range [][2]int64{{0, 0}, {route.Identity.IdentitySequence + 1, 1}, {route.Identity.IdentitySequence, 0}, {route.Identity.IdentitySequence, 2}} {
		if err := check(route.Identity.PoolID, pair[0], pair[1]); !errors.Is(err, ErrPoolRouteInactive) {
			t.Fatal("mismatched identity/revision accepted", pair, err)
		}
	}
	if err := check(route.Identity.PoolID, route.Identity.IdentitySequence, 1); err != nil {
		t.Fatal("exact fixture revision comparison failed", err)
	}
	appendTestOnlyRouteState(t, s, route, 2, "inactive")
	if err := check(route.Identity.PoolID, route.Identity.IdentitySequence, 1); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("stale active revision survived inactive", err)
	}
	appendTestOnlyRouteState(t, s, route, 3, "active")
	if err := check(route.Identity.PoolID, route.Identity.IdentitySequence, 1); !errors.Is(err, ErrPoolRouteInactive) {
		t.Fatal("reactivation silently resumed stale revision", err)
	}
	if err := check(route.Identity.PoolID, route.Identity.IdentitySequence, 3); err != nil {
		t.Fatal(err)
	}
}
func TestPoolRouteStateMissingNewestTransitionCannotReopenOldActive(t *testing.T) {
	s, route := routeStateFixture(t)
	appendTestOnlyRouteState(t, s, route, 1, "active")
	appendTestOnlyRouteState(t, s, route, 2, "inactive")
	if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_delete;DELETE FROM pool_route_states WHERE revision=2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID)
	if !errors.Is(err, ErrPoolRoute) || !reflect.DeepEqual(got, PoolRouteState{}) {
		t.Fatal("missing inactive transition reopened prior active", got, err)
	}
}
func TestPoolRouteStateCanonicalAuditAndScopeCorruptionDenied(t *testing.T) {
	for _, kind := range []string{"unknown-payload", "source-lineage", "audit-subject", "mode-column"} {
		t.Run(kind, func(t *testing.T) {
			s, route := routeStateFixture(t)
			state := appendTestOnlyRouteState(t, s, route, 1, "active")
			if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_update"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "audit-subject":
				if _, err := s.db.Exec("UPDATE audit SET request_key='wrong' WHERE sequence=?", state.StateSequence); err != nil {
					t.Fatal(err)
				}
			case "mode-column":
				if _, err := s.db.Exec("UPDATE pool_route_states SET mode='inactive'"); err != nil {
					t.Fatal(err)
				}
			default:
				if kind == "source-lineage" {
					state.Enable.JournalLineage = bindingHash("other lineage")
				}
				raw, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "unknown-payload" {
					raw = append([]byte(`{"unknown":true,`), raw[1:]...)
				}
				sum := sha256.Sum256(raw)
				if _, err := s.db.Exec("UPDATE pool_route_states SET payload=?,payload_hash=?", string(raw), hex.EncodeToString(sum[:])); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID); !errors.Is(err, ErrPoolRoute) {
				t.Fatal("corrupt fixture state considered valid", err)
			}
		})
	}
}
func TestPoolRouteStateAbsentCatalogNeverFallsBackToStatic(t *testing.T) {
	s, route := routeStateFixture(t)
	if _, err := s.db.Exec("DROP TRIGGER pool_routes_no_delete;DELETE FROM pool_routes"); err != nil {
		t.Fatal(err)
	}
	err := s.readTransaction(context.Background(), func(tx *sql.Tx) error { return requirePoolRouteRevision(tx, route.Identity.PoolID, 0, 0) })
	if !errors.Is(err, ErrPoolRoute) {
		t.Fatal("historically registered pool became static", err)
	}
}

func TestPoolRouteStateAuditOrderCannotBeReversedAtEqualTimestamps(t *testing.T) {
	s, route := routeStateFixture(t)
	first := appendTestOnlyRouteState(t, s, route, 1, "active")
	second := appendTestOnlyRouteState(t, s, route, 2, "inactive")
	if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_update"); err != nil {
		t.Fatal(err)
	}
	if err := s.transaction(context.Background(), func(tx *sql.Tx) error {
		if err := sqliteAudit(tx, "pool_route_activated", route.Identity.PoolID, second.RecordedAt); err != nil {
			return err
		}
		var later int64
		if err := tx.QueryRow("SELECT last_insert_rowid()").Scan(&later); err != nil {
			return err
		}
		old := first.StateSequence
		first.StateSequence = later
		first.RecordedAt = second.RecordedAt
		raw, err := json.Marshal(first)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if _, err := tx.Exec("UPDATE pool_route_states SET state_sequence=?,payload=?,payload_hash=? WHERE revision=1", later, string(raw), hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		_, err = tx.Exec("DELETE FROM audit WHERE sequence=?", old)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID); !errors.Is(err, ErrPoolRoute) {
		t.Fatal("reversed audit anchors accepted despite equal wall times", err)
	}
}

func TestPoolRouteStateConfirmedEnableIntentCannotBeReused(t *testing.T) {
	s, route := routeStateFixture(t)
	first := appendTestOnlyRouteState(t, s, route, 1, "active")
	appendTestOnlyRouteState(t, s, route, 2, "inactive")
	third := appendTestOnlyRouteState(t, s, route, 3, "active")
	if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_update"); err != nil {
		t.Fatal(err)
	}
	third.Enable.RequestID = first.Enable.RequestID
	raw, err := json.Marshal(third)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if _, err := s.db.Exec("UPDATE pool_route_states SET payload=?,payload_hash=? WHERE revision=3", string(raw), hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("same confirmed enable request reused through SQL")
	}
	if _, err := s.db.Exec("DROP INDEX pool_route_enable_intent_once"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE pool_route_states SET payload=?,payload_hash=? WHERE revision=3", string(raw), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID); !errors.Is(err, ErrPoolRoute) {
		t.Fatal("dropped uniqueness constraint silently blessed reused enable history", err)
	}
}

func TestPoolRouteStateEnableReferenceCannotCrossLedgerIdentity(t *testing.T) {
	s, route := routeStateFixture(t)
	state := appendTestOnlyRouteState(t, s, route, 1, "active")
	if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_update"); err != nil {
		t.Fatal(err)
	}
	state.Enable.LedgerLineage = bindingHash("different initialized admission ledger")
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if _, err := s.db.Exec("UPDATE pool_route_states SET payload=?,payload_hash=?", string(raw), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID); !errors.Is(err, ErrRuntimeLineage) && !errors.Is(err, ErrPoolRoute) {
		t.Fatal("enable observation reused on a different ledger", err)
	}
}

func TestPoolRouteStateSQLRequiresStrictInactiveActiveAlternation(t *testing.T) {
	for _, kind := range []string{"first-inactive", "consecutive-active", "consecutive-inactive"} {
		t.Run(kind, func(t *testing.T) {
			s, route := routeStateFixture(t)
			revision := int64(1)
			mode := "inactive"
			if kind != "first-inactive" {
				appendTestOnlyRouteState(t, s, route, 1, "active")
				revision = 2
				mode = "active"
			}
			if kind == "consecutive-inactive" {
				appendTestOnlyRouteState(t, s, route, 2, "inactive")
				revision = 3
				mode = "inactive"
			}
			before := sqliteSnapshot(t, s)
			if _, err := tryAppendTestOnlyRouteState(s, route, revision, mode); err == nil {
				t.Fatal("SQL accepted impossible route state transition")
			}
			if !reflect.DeepEqual(before, sqliteSnapshot(t, s)) {
				t.Fatal("failed alternation wrote audit or changed accounting")
			}
		})
	}
}
func TestPoolRouteStateReaderRejectsRehashedNonalternatingHistory(t *testing.T) {
	for _, kind := range []string{"first-inactive", "consecutive-active", "consecutive-inactive"} {
		t.Run(kind, func(t *testing.T) {
			s, route := routeStateFixture(t)
			first := appendTestOnlyRouteState(t, s, route, 1, "active")
			target := first
			if kind != "first-inactive" {
				target = appendTestOnlyRouteState(t, s, route, 2, "inactive")
			}
			if kind == "consecutive-inactive" {
				target = appendTestOnlyRouteState(t, s, route, 3, "active")
			}
			if _, err := s.db.Exec("DROP TRIGGER pool_route_states_no_update"); err != nil {
				t.Fatal(err)
			}
			audit := "pool_route_deactivated"
			target.Mode = "inactive"
			target.Enable = nil
			if kind == "consecutive-active" {
				target.Mode = "active"
				proof := *first.Enable
				proof.RequestID = "99999999-9999-4999-8999-999999999999"
				proof.ReviewDigest = bindingHash("distinct test-only review")
				proof.ObservedAt = target.RecordedAt
				target.Enable = &proof
				audit = "pool_route_activated"
			}
			raw, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			if _, err := s.db.Exec("UPDATE pool_route_states SET mode=?,payload=?,payload_hash=? WHERE revision=?", target.Mode, string(raw), hex.EncodeToString(sum[:]), target.Revision); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE audit SET kind=? WHERE sequence=?", audit, target.StateSequence); err != nil {
				t.Fatal(err)
			}
			// Isolate transition semantics: the target remains individually canonical,
			// correctly scoped and audit-linked, unlike ordinary malformed-payload tests.
			if err := s.readTransaction(context.Background(), func(tx *sql.Tx) error { _, err := poolRouteStateRecord(tx, route, target.Revision); return err }); err != nil {
				t.Fatal("fixture did not retain valid individual record", err)
			}
			if _, err := s.LookupPoolRouteState(context.Background(), route.Identity.PoolID); !errors.Is(err, ErrPoolRoute) {
				t.Fatal("reader blessed impossible alternating history", err)
			}
		})
	}
}
