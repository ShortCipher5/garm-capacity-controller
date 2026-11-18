package admission

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"path/filepath"
	"reflect"

	"testing"
	"time"
)

func TestSSHTargetPersistenceIsAtomicIdempotentAndSeparateFromCapacity(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	before := sqliteSnapshot(t, s)
	if e := s.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	c := sshinventory.Config{Address: "192.0.2.20", Port: 22, User: "inventory", CredentialRef: "alias", HostKey: "public-key-placeholder"}
	request := "11111111-1111-4111-8111-111111111111"
	digest := sshinventory.ReviewDigest("new", c)
	if e := s.SaveSSHTarget(ctx, request, "new", c, digest, 1); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveSSHTarget(ctx, request, "new", c, digest, 1); e != nil {
		t.Fatal("idempotent retry", e)
	}
	changed := c
	changed.Address = "192.0.2.21"
	if s.SaveSSHTarget(ctx, request, "new", changed, digest, 1) == nil {
		t.Fatal("existing tuple overwritten")
	}
	if s.SaveSSHTarget(ctx, request, "other", c, sshinventory.ReviewDigest("other", c), 1) == nil {
		t.Fatal("limit escaped")
	}
	// Reinitialize as on restart: committed records survive and remain readable.
	if e := s.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	list, e := s.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 || list["new"] != c {
		t.Fatal(list, e)
	}
	after := sqliteSnapshot(t, s)
	if !reflect.DeepEqual(before.Config, after.Config) || !reflect.DeepEqual(before.Reservations, after.Reservations) || !reflect.DeepEqual(before.HostBindings, after.HostBindings) || before.Ready != after.Ready || before.Epoch != after.Epoch || len(after.Audit) != len(before.Audit)+1 {
		t.Fatal("registration altered admission")
	}
	if _, e = s.db.Exec(`CREATE TRIGGER reject_ssh_target BEFORE INSERT ON audit WHEN NEW.kind='ssh_target_approved' BEGIN SELECT RAISE(ABORT,'denied'); END`); e != nil {
		t.Fatal(e)
	}
	if s.SaveSSHTarget(ctx, request, "audit-failed", c, sshinventory.ReviewDigest("audit-failed", c), 32) == nil {
		t.Fatal("audit failure allowed registration")
	}
	list, e = s.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 {
		t.Fatal("partial registration escaped", e)
	}
	if _, e = s.db.Exec(`UPDATE ssh_inventory_targets SET digest='corrupt' WHERE id='new'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListSSHTargets(ctx); e == nil {
		t.Fatal("corrupt approval digest ignored")
	}
}

func TestSSHTargetSurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	config := fixture(time.Now())
	store, e := OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	c := sshinventory.Config{Address: "192.0.2.20", Port: 22, User: "inventory", CredentialRef: "alias", HostKey: "public-key-placeholder"}
	if e = store.SaveSSHTarget(ctx, "11111111-1111-4111-8111-111111111111", "candidate", c, sshinventory.ReviewDigest("candidate", c), 32); e != nil {
		t.Fatal(e)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store, e = OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	list, e := store.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 || list["candidate"] != c {
		t.Fatal("approval lost after restart", e)
	}
}
