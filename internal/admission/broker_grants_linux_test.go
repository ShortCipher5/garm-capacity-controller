package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const brokerGrantDowngradeSQL = nativeExecutionDowngradeSQL + `
DROP TRIGGER broker_batch_no_update;
DROP TRIGGER broker_grant_identity_immutable;
DROP TABLE broker_grant_archives;
DROP INDEX broker_grant_audit_kind;
DELETE FROM audit WHERE kind IN ('broker_grant_batch_archived','schema_migrated_v18');

DROP TRIGGER dispatch_current_accounting;
ALTER TABLE dispatches DROP COLUMN grant_accounting_version;
DROP TRIGGER reservation_current_accounting;
ALTER TABLE reservations DROP COLUMN accounting_version;
DROP TABLE broker_grants;
DROP TABLE broker_grant_batches;
DROP TABLE broker_grant_legacy_stock;
DELETE FROM audit WHERE kind IN ('broker_grants_issued','broker_grant_consumed','broker_grant_revoked','broker_grant_expired','schema_migrated_v17');
`

func issueStockTestGrant(t *testing.T, s *SQLiteStore, name string) BrokerGrant {
	t.Helper()
	ctx := context.Background()
	request := placementRequest(name)
	execution := request.Execution
	execution.Name = ""
	for _, host := range []string{"a", "b"} {
		state, e := s.ReadBrokerGrantState(ctx)
		if e != nil {
			t.Fatal(e)
		}
		binding, e := s.LookupHostBinding(ctx, host)
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256([]byte(name + host))
		batch := BrokerGrantBatch{ID: hex.EncodeToString(hash[:]), ExpectedSequence: state.Sequence, PolicyRevision: state.Policy.Revision, Grants: []BrokerGrantSpec{{Authority: stockAuthority(), Host: host, HostBindingSequence: binding.Sequence, Resources: request.Resources, Execution: execution, ExpiresAt: s.now().Add(time.Hour)}}}
		result, e := s.IssueBrokerGrants(ctx, batch)
		if e == nil {
			return result[0]
		}
	}
	t.Fatal("no grant capacity")
	return BrokerGrant{}
}

func TestBrokerGrantCountsAgainstEveryAdmissionAndRedeemsOnce(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("missing"), stockAuthority(), stockDigest()); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("ungranted: %v", e)
	}
	g := issueStockTestGrant(t, s, "one")
	state, e := s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 1 {
		t.Fatalf("uncharged grant: %+v %v", state.Allocated, e)
	}
	ordinary, e := s.AdmitPlacement(ctx, placementRequest("ordinary"))
	if e != nil || ordinary.Request.Host == g.Spec.Host {
		t.Fatalf("ignored grant host: %+v %v", ordinary, e)
	}
	if _, e := s.Admit(ctx, Request{Key: "overflow", Pool: "app", Host: "a", Resources: placementRequest("x").Resources}); !errors.Is(e, ErrDenied) {
		t.Fatalf("legacy ignored grant: %v", e)
	}
	plan, e := s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest())
	if e != nil || plan.Request.Host != g.Spec.Host {
		t.Fatalf("redemption %+v %v", plan, e)
	}
	for i := 0; i < 2; i++ {
		retry, e := s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest())
		if e != nil || !retry.Replay || retry.Request != plan.Request {
			t.Fatalf("replay %+v %v", retry, e)
		}
	}
	state, e = s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 2 {
		t.Fatalf("double count %+v %v", state.Allocated, e)
	}
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("consumed grant revoked: %v", e)
	}
	if _, e := s.CancelUndispatched(ctx, plan.Request); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest()); !errors.Is(e, ErrCancelled) {
		t.Fatalf("cancelled revived: %v", e)
	}
	if _, e := s.AdmitStockProviderPlacement(ctx, placementRequest("replacement"), stockAuthority(), stockDigest()); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("consumed grant reused: %v", e)
	}
}
func TestBrokerGrantExpiryRetainsChargeUntilDurableRetirement(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "expire")
	original := s.now
	s.now = func() time.Time { return g.Spec.ExpiresAt.Add(time.Second) }
	// Refresh freshness only through a real reconciliation fence.
	fence, e := s.BeginReconciliation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.ReadSnapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteReconciliation(ctx, fence, InventorySnapshot{Hosts: observations(snap.Config, s.now())}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AdmitStockProviderPlacement(ctx, placementRequest("late"), stockAuthority(), stockDigest()); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("expired admitted: %v", e)
	}
	snap, e = s.ReadSnapshot(ctx)
	if e != nil || snap.Allocated.Global.Runners != 1 {
		t.Fatal("wall clock released capacity")
	}
	if e = s.RetireUnusedBrokerGrant(ctx, g.ID, true); e != nil {
		t.Fatal(e)
	}
	s.now = original
	if e = s.RetireUnusedBrokerGrant(ctx, g.ID, true); e != nil {
		t.Fatal("terminal replay must work after rollback", e)
	}
	var state string
	if e = s.db.QueryRow("SELECT state FROM broker_grants WHERE grant_id=?", g.ID).Scan(&state); e != nil || state != "expired" {
		t.Fatal("revived grant")
	}
}
func TestBrokerGrantExactBatchReplayAndStaleRevision(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "batch")
	var raw string
	if e := s.db.QueryRow("SELECT payload FROM broker_grant_batches LIMIT 1").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var b BrokerGrantBatch
	if e := json.Unmarshal([]byte(raw), &b); e != nil {
		t.Fatal(e)
	}
	if _, e := s.IssueBrokerGrants(ctx, b); e != nil {
		t.Fatal(e)
	}
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	replay, e := s.IssueBrokerGrants(ctx, b)
	if e != nil || len(replay) != 1 || replay[0].State != "revoked" {
		t.Fatalf("retry revived %+v %v", replay, e)
	}
	b.Grants[0].ExpiresAt = b.Grants[0].ExpiresAt.Add(time.Second)
	if _, e = s.IssueBrokerGrants(ctx, b); !errors.Is(e, ErrConflict) {
		t.Fatalf("batch identity changed %v", e)
	}
	b.ID = strings.Repeat("f", 64)
	if _, e = s.IssueBrokerGrants(ctx, b); !errors.Is(e, ErrBrokerConflict) {
		t.Fatalf("stale state accepted %v", e)
	}
}
func TestBrokerGrantConcurrentNamesConsumeOnlyOnce(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	issueStockTestGrant(t, s, "race")
	second, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	var count atomic.Int32
	var wg sync.WaitGroup
	for i, store := range []*SQLiteStore{s, second} {
		wg.Add(1)
		go func(i int, store *SQLiteStore) {
			defer wg.Done()
			_, e := store.AdmitStockProviderPlacement(ctx, placementRequest(fmt.Sprintf("name%d", i)), stockAuthority(), stockDigest())
			if e == nil {
				count.Add(1)
			} else if !errors.Is(e, ErrBrokerGrant) {
				t.Errorf("unexpected %v", e)
			}
		}(i, store)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("admitted %d", count.Load())
	}
	state, e := s.ReadSnapshot(ctx)
	if e != nil || state.Allocated.Global.Runners != 1 || len(state.Reservations) != 1 {
		t.Fatalf("state %+v %v", state.Allocated, e)
	}
}
func TestBrokerGrantWrongProfileAndAuthorityCannotConsume(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	issueStockTestGrant(t, s, "profile")
	for _, change := range []func(*PlacementRequest, *StockProviderAuthority){func(r *PlacementRequest, _ *StockProviderAuthority) { r.Execution.Image = "other" }, func(r *PlacementRequest, _ *StockProviderAuthority) { r.Resources.RAM++ }, func(_ *PlacementRequest, a *StockProviderAuthority) { a.ProviderName = "other" }, func(_ *PlacementRequest, a *StockProviderAuthority) { a.ClientFingerprint = strings.Repeat("f", 64) }} {
		r := placementRequest("actual")
		a := stockAuthority()
		change(&r, &a)
		if _, e := s.AdmitStockProviderPlacement(ctx, r, a, stockDigest()); !errors.Is(e, ErrBrokerGrant) {
			t.Fatalf("mismatch: %v", e)
		}
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil || state.Grants[0].State != "unused" {
		t.Fatal("mismatch consumed")
	}
}
func TestBrokerGrantOldWriterSQLAndVersionRollbackBlocked(t *testing.T) {
	s := placementFixture(t)
	plan, e := s.AdmitPlacement(context.Background(), placementRequest("reserved"))
	if e != nil {
		t.Fatal(e)
	}
	// Exact old-version INSERT shape on another open connection cannot skip grants.
	old, e := sql.Open("sqlite3", "file:"+s.path+"?_foreign_keys=true")
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if _, e = old.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,disk,runners,state,created_at) VALUES('old','app','a',1,1,0,1,'creating',?)", time.Now().UTC().Format(time.RFC3339Nano)); e == nil {
		t.Fatal("old writer inserted")
	}
	if _, e = old.Exec("INSERT INTO dispatches(reservation_key,permit_id,state,operation_id,resource_id,created_at,updated_at) VALUES(?,?,'intent','','',?,?)", plan.Request.Key, strings.Repeat("a", 64), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); e == nil {
		t.Fatal("old dispatcher inserted")
	}
	if _, e = s.db.Exec("PRAGMA user_version=16"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Admit(context.Background(), Request{Key: "new", Pool: "app", Host: "a", Resources: placementRequest("x").Resources}); e == nil {
		t.Fatal("schema rollback accepted")
	}
	if _, e = s.db.Exec("PRAGMA user_version=25"); e != nil {
		t.Fatal(e)
	}
}

func TestBrokerGrantIssueRollbackAndMissingManifestFailClosed(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "manifest")
	// Mutation is blocked normally. A corrupt/restored database missing a charge
	// must still fail reads and admission rather than treating capacity as free.
	if _, e := s.db.Exec("DELETE FROM broker_grants WHERE grant_id=?", g.ID); e == nil {
		t.Fatal("grant deleted")
	}
	if _, e := s.db.Exec("DROP TRIGGER broker_grant_no_delete; DELETE FROM broker_grants"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadSnapshot(ctx); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("missing grant ignored: %v", e)
	}
	if _, e := s.AdmitPlacement(ctx, placementRequest("unrelated")); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("corrupt history admitted: %v", e)
	}
}
func TestBrokerGrantBatchRollsBackCompletelyOnCapacityOrAuditFailure(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	seed := issueStockTestGrant(t, s, "seed")
	var raw string
	s.db.QueryRow("SELECT payload FROM broker_grant_batches LIMIT 1").Scan(&raw)
	var b BrokerGrantBatch
	json.Unmarshal([]byte(raw), &b)
	if e := s.RetireUnusedBrokerGrant(ctx, seed.ID, false); e != nil {
		t.Fatal(e)
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b.ExpectedSequence = state.Sequence
	b.ID = strings.Repeat("e", 64)
	b.Grants = append(b.Grants, b.Grants[0])
	if _, e = s.IssueBrokerGrants(ctx, b); !errors.Is(e, ErrDenied) {
		t.Fatalf("overbooked batch %v", e)
	}
	state, e = s.ReadBrokerGrantState(ctx)
	if e != nil || len(state.Grants) != 1 {
		t.Fatalf("partial issuance %+v %v", state.Grants, e)
	}
	b.Grants = b.Grants[:1]
	b.ExpectedSequence = state.Sequence
	if _, e = s.db.Exec("CREATE TRIGGER fail_grant_audit BEFORE INSERT ON audit WHEN NEW.kind='broker_grants_issued' BEGIN SELECT RAISE(ABORT,'injected'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.IssueBrokerGrants(ctx, b); e == nil {
		t.Fatal("audit failure ignored")
	}
	state, e = s.ReadBrokerGrantState(ctx)
	if e != nil || len(state.Grants) != 1 {
		t.Fatal("audit partial grant")
	}
}

func TestBrokerGrantMigrationKeepsHistoricalStockReplay(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	plan, _ := stockPlan(t, s, "legacy16")
	// Explicitly reconstruct actual schema16, not just its version number.
	if _, e := s.db.Exec(brokerGrantDowngradeSQL + "PRAGMA user_version=16"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	migrated, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	state, e := migrated.ReadBrokerGrantState(ctx)
	if e != nil || state.Ready || len(state.Grants) != 0 || len(state.Reservations) != 1 {
		t.Fatalf("migration %+v %v", state, e)
	}
	replay, e := migrated.AdmitStockProviderPlacement(ctx, placementRequest("legacy16"), stockAuthority(), stockDigest())
	if e != nil || !replay.Replay || replay.Request != plan.Request {
		t.Fatalf("historical replay %+v %v", replay, e)
	}
	var markers int
	if e = migrated.db.QueryRow("SELECT count(*) FROM broker_grant_legacy_stock").Scan(&markers); e != nil || markers != 1 {
		t.Fatal("missing historical marker", e)
	}
}
func TestBrokerGrantCapacityViewIncludesUnusedCharges(t *testing.T) {
	s := placementFixture(t)
	issueStockTestGrant(t, s, "view")
	view, e := s.ReadCapacityStates(context.Background())
	if e != nil || !view.GrantsAvailable || view.Total.Runners != 1 || len(view.Rows) != 1 || view.Rows[0].State != "granted" {
		t.Fatalf("%+v %v", view, e)
	}
}

func TestBrokerGrantMissingBatchAndGrantsStillFailClosed(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	issueStockTestGrant(t, s, "lost-history")
	if _, e := s.db.Exec("DROP TRIGGER broker_grant_no_delete; DROP TRIGGER broker_batch_no_delete; DELETE FROM broker_grants; DELETE FROM broker_grant_batches"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadSnapshot(ctx); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("missing manifest hidden %v", e)
	}
	if _, e := s.AdmitPlacement(ctx, placementRequest("new")); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("missing history admitted %v", e)
	}
}
func TestBrokerGrantRejectsExhaustedReconciliationEpoch(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "epoch")
	var raw string
	s.db.QueryRow("SELECT payload FROM broker_grant_batches LIMIT 1").Scan(&raw)
	var b BrokerGrantBatch
	json.Unmarshal([]byte(raw), &b)
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("UPDATE metadata SET epoch=9223372036854775807"); e != nil {
		t.Fatal(e)
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b.ID = strings.Repeat("b", 64)
	b.ExpectedSequence = state.Sequence
	if _, e = s.IssueBrokerGrants(ctx, b); !errors.Is(e, ErrReconciliation) {
		t.Fatalf("exhausted epoch granted %v", e)
	}
}

func TestBrokerGrantRevocationAndRedemptionSerializeAcrossHandles(t *testing.T) {
	for attempt := 0; attempt < 5; attempt++ {
		s := placementFixture(t)
		ctx := context.Background()
		g := issueStockTestGrant(t, s, fmt.Sprintf("race%d", attempt))
		other, e := OpenSQLite(s.path, Config{})
		if e != nil {
			t.Fatal(e)
		}
		begin := make(chan struct{})
		var issueErr, revokeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-begin
			_, issueErr = s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest())
		}()
		go func() { defer wg.Done(); <-begin; revokeErr = other.RetireUnusedBrokerGrant(ctx, g.ID, false) }()
		close(begin)
		wg.Wait()
		other.Close()
		snapshot, e := s.ReadSnapshot(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if issueErr == nil {
			if !errors.Is(revokeErr, ErrBrokerGrant) || snapshot.Allocated.Global.Runners != 1 {
				t.Fatalf("consumed release %v %+v", revokeErr, snapshot.Allocated)
			}
		} else if revokeErr == nil {
			if !errors.Is(issueErr, ErrBrokerGrant) || snapshot.Allocated.Global.Runners != 0 {
				t.Fatalf("revoked admission %v %+v", issueErr, snapshot.Allocated)
			}
		} else {
			t.Fatalf("both failed %v %v", issueErr, revokeErr)
		}
	}
}

func TestBrokerGrantHistoryBoundRejectsBeforePoisoningLedger(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "history-bound")
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	var raw string
	s.db.QueryRow("SELECT payload FROM broker_grant_batches LIMIT 1").Scan(&raw)
	var b BrokerGrantBatch
	json.Unmarshal([]byte(raw), &b)
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b.ExpectedSequence = state.Sequence
	b.ID = strings.Repeat("d", 64)
	if _, e = s.issueBrokerGrants(ctx, b, 1); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("exceeded bounded history %v", e)
	}
	after, e := s.ReadBrokerGrantState(ctx)
	if e != nil || len(after.Grants) != 1 || after.Sequence != state.Sequence {
		t.Fatalf("accepted request poisoned ledger: %+v %v", after, e)
	}
	// Real permanent history remains intact and replay still works at the bound.
	json.Unmarshal([]byte(raw), &b)
	if result, e := s.issueBrokerGrants(ctx, b, 1); e != nil || len(result) != 1 || result[0].State != "revoked" {
		t.Fatalf("bounded replay %+v %v", result, e)
	}
}
