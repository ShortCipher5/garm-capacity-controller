package admission

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func batchForGrant(t *testing.T, s *SQLiteStore, g BrokerGrant) BrokerGrantBatch {
	t.Helper()
	var raw string
	if e := s.db.QueryRow("SELECT b.payload FROM broker_grant_batches b JOIN broker_grants g ON g.batch_id=b.batch_id WHERE g.grant_id=?", g.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var b BrokerGrantBatch
	if e := json.Unmarshal([]byte(raw), &b); e != nil {
		t.Fatal(e)
	}
	return b
}
func TestBrokerArchiveRetainsExactReplayAndReopensHotCapacity(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "cold")
	batch := batchForGrant(t, s, g)
	if _, e := s.CompactBrokerGrantBatch(ctx, batch.ID); !errors.Is(e, ErrBrokerGrant) {
		t.Fatalf("unused archived %v", e)
	}
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	result, e := s.CompactBrokerGrantBatch(ctx, batch.ID)
	if e != nil || result.Grants != 1 || result.OriginalBytes < 1 {
		t.Fatalf("%+v %v", result, e)
	}
	again, e := s.CompactBrokerGrantBatch(ctx, batch.ID)
	if e != nil || !again.Replay {
		t.Fatalf("%+v %v", again, e)
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil || len(state.Grants) != 0 || state.Allocated.Global.Runners != 0 {
		t.Fatalf("hot state %+v %v", state, e)
	}
	replay, e := s.IssueBrokerGrants(ctx, batch)
	if e != nil || len(replay) != 1 || replay[0].State != "revoked" || !reflect.DeepEqual(replay[0].Spec, g.Spec) {
		t.Fatalf("replay %+v %v", replay, e)
	}
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	// A small real hot bound demonstrates that cold lifetime entries no longer
	// exhaust issuance, while the original ID remains a permanent replay tombstone.
	batch.ID = strings.Repeat("a", 64)
	batch.ExpectedSequence = state.Sequence
	next, e := s.issueBrokerGrants(ctx, batch, 1)
	if e != nil || len(next) != 1 {
		t.Fatalf("cold rows counted against hot bound %v", e)
	}
}
func TestBrokerArchiveConsumedOnlyAfterCanonicalCancellation(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "consume")
	batch := batchForGrant(t, s, g)
	p, e := s.AdmitStockProviderPlacement(ctx, placementRequest("real"), stockAuthority(), stockDigest())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompactBrokerGrantBatch(ctx, batch.ID); !errors.Is(e, ErrBrokerGrant) {
		t.Fatal("active consumed archived", e)
	}
	if _, e = s.CancelUndispatched(ctx, p.Request); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompactBrokerGrantBatch(ctx, batch.ID); e != nil {
		t.Fatal(e)
	}
	replay, e := s.IssueBrokerGrants(ctx, batch)
	if e != nil || replay[0].State != "consumed" || replay[0].RequestKey != p.Request.Key {
		t.Fatalf("consumed replay %+v %v", replay, e)
	}
	if _, e = s.AdmitStockProviderPlacement(ctx, placementRequest("real"), stockAuthority(), stockDigest()); !errors.Is(e, ErrCancelled) {
		t.Fatal("cancelled revived", e)
	}
}
func TestBrokerArchiveUnknownDispatchNeverCompacts(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "unknown")
	batch := batchForGrant(t, s, g)
	p, e := s.AdmitStockProviderPlacement(ctx, placementRequest("actual"), stockAuthority(), stockDigest())
	if e != nil {
		t.Fatal(e)
	}
	binding, e := s.LookupHostBinding(ctx, p.Request.Host)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExecuteStockProviderDispatch(ctx, p, binding, stockAuthority(), stockDigest(), func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		return DispatchReceipt{}, errors.New("lost")
	}); !errors.Is(e, ErrDispatchUnknown) {
		t.Fatal(e)
	}
	if _, e = s.CompactBrokerGrantBatch(ctx, batch.ID); !errors.Is(e, ErrBrokerGrant) {
		t.Fatal("unknown compacted", e)
	}
	snapshot, e := s.ReadSnapshot(ctx)
	if e != nil || snapshot.Allocated.Global.Runners != 1 {
		t.Fatal("unknown charge lost", e)
	}
}
func TestBrokerArchiveRestoreRetainsColdHistoryAndHotCharges(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "archive")
	batch := batchForGrant(t, s, g)
	if e := s.RetireUnusedBrokerGrant(ctx, g.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CompactBrokerGrantBatch(ctx, batch.ID); e != nil {
		t.Fatal(e)
	}
	issueStockTestGrant(t, s, "hot")
	archive := filepath.Join(sqliteBackupDir(t), "archive.db")
	if e := s.BackupSQLite(ctx, archive); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restore.db")
	if e := RestoreSQLite(ctx, archive, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	state, e := copy.ReadBrokerGrantState(ctx)
	if e != nil || state.Ready || state.Allocated.Global.Runners != 1 || len(state.Grants) != 1 {
		t.Fatalf("restore %+v %v", state, e)
	}
	replay, e := copy.IssueBrokerGrants(ctx, batch)
	if e != nil || replay[0].State != "revoked" {
		t.Fatal("cold replay lost", e)
	}
}
func TestBrokerArchiveCorruptionFailsReplayReopenAndBackup(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "corrupt")
	batch := batchForGrant(t, s, g)
	s.RetireUnusedBrokerGrant(ctx, g.ID, false)
	if _, e := s.CompactBrokerGrantBatch(ctx, batch.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("UPDATE broker_grant_archives SET payload=X'00'"); e == nil {
		t.Fatal("mutable archive")
	}
	if _, e := s.db.Exec("DROP TRIGGER broker_archive_no_update; UPDATE broker_grant_archives SET payload=X'00'"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.IssueBrokerGrants(ctx, batch); e == nil {
		t.Fatal("corrupt replay")
	}
	if e := s.BackupSQLite(ctx, filepath.Join(sqliteBackupDir(t), "bad.db")); e == nil {
		t.Fatal("corrupt backup")
	}
	path := s.path
	s.Close()
	if store, e := OpenSQLite(path, Config{}); e == nil {
		store.Close()
		t.Fatal("corrupt reopen")
	}
}
func TestBrokerArchiveAtomicFailureAndConcurrentRetry(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "atomic")
	batch := batchForGrant(t, s, g)
	s.RetireUnusedBrokerGrant(ctx, g.ID, false)
	if _, e := s.db.Exec("CREATE TRIGGER archive_fail BEFORE UPDATE ON broker_grant_batches BEGIN SELECT RAISE(ABORT,'injected'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CompactBrokerGrantBatch(ctx, batch.ID); e == nil {
		t.Fatal("partial accepted")
	}
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil || len(state.Grants) != 1 {
		t.Fatal("partial archived", e)
	}
	s.db.Exec("DROP TRIGGER archive_fail")
	other, e := OpenSQLite(s.path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var wg sync.WaitGroup
	for _, store := range []*SQLiteStore{s, other} {
		wg.Add(1)
		go func(store *SQLiteStore) {
			defer wg.Done()
			if _, e := store.CompactBrokerGrantBatch(ctx, batch.ID); e != nil {
				t.Error(e)
			}
		}(store)
	}
	wg.Wait()
	var count int
	s.db.QueryRow("SELECT count(*) FROM broker_grant_archives").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate archive")
	}
}

func TestBrokerArchiveMissingCertificateBlocksAccounting(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "missing")
	b := batchForGrant(t, s, g)
	s.RetireUnusedBrokerGrant(ctx, g.ID, false)
	if _, e := s.CompactBrokerGrantBatch(ctx, b.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("DROP TRIGGER broker_archive_no_delete; DELETE FROM broker_grant_archives"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadSnapshot(ctx); !errors.Is(e, ErrBrokerGrant) {
		t.Fatal("missing archive ignored", e)
	}
	if _, e := s.CompactBrokerGrantHistory(ctx, 1); !errors.Is(e, ErrBrokerGrant) {
		t.Fatal("corrupt maintenance reported success", e)
	}
}
func TestBrokerArchiveInflationAndTrailingBytesAreBounded(t *testing.T) {
	for _, mode := range []string{"inflation", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			s := placementFixture(t)
			ctx := context.Background()
			g := issueStockTestGrant(t, s, mode)
			b := batchForGrant(t, s, g)
			s.RetireUnusedBrokerGrant(ctx, g.ID, false)
			if _, e := s.CompactBrokerGrantBatch(ctx, b.ID); e != nil {
				t.Fatal(e)
			}
			var payload []byte
			s.db.QueryRow("SELECT payload FROM broker_grant_archives").Scan(&payload)
			if mode == "inflation" {
				var buf bytes.Buffer
				z := gzip.NewWriter(&buf)
				z.Write(make([]byte, (1<<20)+1))
				z.Close()
				payload = buf.Bytes()
			} else {
				payload = append(payload, 0)
			}
			if _, e := s.db.Exec("DROP TRIGGER broker_archive_no_update; UPDATE broker_grant_archives SET payload=?", payload); e != nil {
				t.Fatal(e)
			}
			if _, e := s.IssueBrokerGrants(ctx, b); !errors.Is(e, ErrBrokerGrant) {
				t.Fatal("unsafe archive replay", e)
			}
		})
	}
}

const restoreSchema17ArchiveFixture = `
DROP TRIGGER broker_batch_no_update;
DROP TRIGGER broker_grant_identity_immutable;
DROP TRIGGER broker_grant_terminal;
DROP TABLE broker_grant_archives;
DROP INDEX broker_grants_by_batch;
DROP INDEX broker_grants_hot;
DROP INDEX broker_batches_hot;
DROP INDEX broker_grant_audit_kind;
CREATE TRIGGER broker_batch_no_update BEFORE UPDATE ON broker_grant_batches BEGIN SELECT RAISE(ABORT,'grant batch immutable'); END;
CREATE TRIGGER broker_grant_identity_immutable BEFORE UPDATE ON broker_grants WHEN OLD.grant_id IS NOT NEW.grant_id OR OLD.batch_id IS NOT NEW.batch_id OR OLD.payload IS NOT NEW.payload OR OLD.payload_hash IS NOT NEW.payload_hash BEGIN SELECT RAISE(ABORT,'grant identity immutable'); END;
CREATE TRIGGER broker_grant_terminal BEFORE UPDATE ON broker_grants WHEN OLD.state!='unused' OR NEW.state='unused' BEGIN SELECT RAISE(ABORT,'grant transition forbidden'); END;
DROP TRIGGER reservation_current_accounting;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations WHEN NEW.accounting_version!=17 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
DROP TRIGGER dispatch_current_accounting;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches WHEN NEW.grant_accounting_version!=17 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
DELETE FROM audit WHERE kind='schema_migrated_v18';
PRAGMA user_version=17;
`

func TestBrokerArchiveMigrationPreservesOutstandingGrantsAndClosesReadiness(t *testing.T) {
	s := placementFixture(t)
	ctx := context.Background()
	g := issueStockTestGrant(t, s, "v17")
	batch := batchForGrant(t, s, g)
	if _, e := s.db.Exec(restoreSchema17ArchiveFixture); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	migrated, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	state, e := migrated.ReadBrokerGrantState(ctx)
	if e != nil || state.Ready || state.Allocated.Global.Runners != 1 || len(state.Grants) != 1 || state.Grants[0].ID != g.ID {
		t.Fatalf("migration %+v %v", state, e)
	}
	if replay, e := migrated.IssueBrokerGrants(ctx, batch); e != nil || len(replay) != 1 || replay[0].State != "unused" {
		t.Fatal("migration replay", e)
	}
	if _, e = migrated.db.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,disk,runners,state,created_at,accounting_version) VALUES('old17','app','b',1,1,0,1,'creating','2026-01-01T00:00:00Z',17)"); e == nil {
		t.Fatal("old17 writer admitted")
	}
}
