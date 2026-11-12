package admission

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestReadViewCancellationCannotPoisonAdmissionWriter(t *testing.T) {
	store := sqliteFixtureStore(t, fixture(time.Now()), true)
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		err := store.readTransaction(ctx, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM inventory").Scan(&n); err != nil {
				return err
			}
			cancel()
			return nil
		})
		cancel()
		if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("unexpected canceled read %v", err)
		}
		if store.poisoned.Load() {
			t.Fatal("read peer cancellation poisoned writer")
		}
	}
	if _, err := store.Admit(context.Background(), request("after-read-disconnect")); err != nil {
		t.Fatalf("read cancellation denied unrelated admission %v", err)
	}
}
func TestReadViewCannotWriteAndNeverChangesExpiredReadiness(t *testing.T) {
	store := sqliteFixtureStore(t, fixture(time.Now()), true)
	before := sqliteSnapshot(t, store)
	err := store.readTransaction(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE metadata SET ready=0 WHERE id=1"); return err })
	if err == nil || store.poisoned.Load() {
		t.Fatal("read view wrote or poisoned writer")
	}
	after := sqliteSnapshot(t, store)
	if !after.Ready || !reflect.DeepEqual(before.Audit, after.Audit) {
		t.Fatal("failed read-only write changed source")
	}
	now := time.Now().Add(2 * time.Minute)
	store.now = func() time.Time { return now }
	view, err := store.ReadSnapshot(context.Background())
	if err != nil || view.Ready {
		t.Fatal("read view advertised expired readiness")
	}
	var storedReady bool
	if err := store.db.QueryRow("SELECT ready FROM metadata WHERE id=1").Scan(&storedReady); err != nil || !storedReady {
		t.Fatal("read view mutated readiness instead of only displaying expiry")
	}
	if _, err := store.Admit(context.Background(), request("expired")); !errors.Is(err, ErrReconciliation) {
		t.Fatal("writer ignored its own freshness guard")
	}
}
func TestReadViewMatchesCommittedSnapshotAndHonorsPoison(t *testing.T) {
	store := sqliteFixtureStore(t, fixture(time.Now()), true)
	if _, err := store.Admit(context.Background(), request("one")); err != nil {
		t.Fatal(err)
	}
	expected := sqliteSnapshot(t, store)
	actual, err := store.ReadSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("inconsistent read view %v", err)
	}
	// Keep the existing ambiguous-commit guarantee: even administrative reads fail
	// closed after an actual unavailable writer commit result.
	err = store.transaction(context.Background(), func(tx *sql.Tx) error { return tx.Commit() })
	if !errors.Is(err, ErrUncertain) {
		t.Fatal("ambiguous write no longer poisoned")
	}
	if _, err := store.ReadSnapshot(context.Background()); !errors.Is(err, ErrUncertain) {
		t.Fatal("read view ignored poisoned writer")
	}
}
