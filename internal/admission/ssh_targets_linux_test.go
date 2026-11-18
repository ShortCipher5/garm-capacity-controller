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

func TestSSHTargetRevocationIsDurableAtomicAndCannotReactivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	config := fixture(time.Now())
	s, e := OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	c := sshinventory.Config{Address: "192.0.2.20", Port: 22, User: "inventory", CredentialRef: "alias", HostKey: "public-key-placeholder"}
	request := "11111111-1111-4111-8111-111111111111"
	digest := sshinventory.ReviewDigest("candidate", c)
	if e = s.SaveSSHTarget(ctx, request, "candidate", c, digest, 1); e != nil {
		t.Fatal(e)
	}
	before := sqliteSnapshot(t, s)
	if s.RevokeSSHTarget(ctx, request, "candidate", "wrong") == nil {
		t.Fatal("wrong digest revoked")
	}
	if _, e = s.db.Exec(`CREATE TRIGGER reject_revocation BEFORE INSERT ON audit WHEN NEW.kind='ssh_target_revoked' BEGIN SELECT RAISE(ABORT,'denied'); END`); e != nil {
		t.Fatal(e)
	}
	if s.RevokeSSHTarget(ctx, request, "candidate", digest) == nil {
		t.Fatal("audit failure accepted")
	}
	list, e := s.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 {
		t.Fatal("audit failure partly revoked", e)
	}
	if _, e = s.db.Exec(`DROP TRIGGER reject_revocation`); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeSSHTarget(ctx, request, "candidate", digest); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeSSHTarget(ctx, request, "candidate", digest); e != nil {
		t.Fatal("idempotent revocation", e)
	}
	list, e = s.ListSSHTargets(ctx)
	if e != nil || len(list) != 0 {
		t.Fatal("revoked target still active", e)
	}
	if s.SaveSSHTarget(ctx, request, "candidate", c, digest, 1) == nil {
		t.Fatal("replay resurrected revoked target")
	}
	// Exact pre-revocation reader query must not expose the tombstoned target.
	var oldCount int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM ssh_inventory_targets WHERE id='candidate'`).Scan(&oldCount); e != nil || oldCount != 0 {
		t.Fatal("older reader can see revoked target", e)
	}
	var retained string
	if e = s.db.QueryRow(`SELECT config FROM ssh_inventory_revocations WHERE id='candidate'`).Scan(&retained); e != nil || retained == "" {
		t.Fatal("revocation lost original configuration", e)
	}
	// Old #107 writer checks only its active table; database trigger must reject
	// its eventual INSERT even though it does not know the revocation table.
	if _, e = s.db.Exec(`INSERT INTO ssh_inventory_targets(id,config,digest) VALUES('candidate',?,?)`, retained, digest); e == nil {
		t.Fatal("older writer resurrected revoked ID")
	}
	after := sqliteSnapshot(t, s)
	if !reflect.DeepEqual(before.Config, after.Config) || !reflect.DeepEqual(before.Reservations, after.Reservations) || !reflect.DeepEqual(before.HostBindings, after.HostBindings) || before.Ready != after.Ready || before.Epoch != after.Epoch || len(after.Audit) != len(before.Audit)+1 {
		t.Fatal("revocation changed admission or repeated audit")
	}
	// A new independently approved identity can use the vacated active-list slot.
	if e = s.SaveSSHTarget(ctx, request, "replacement", c, sshinventory.ReviewDigest("replacement", c), 1); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	list, e = s.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 || list["replacement"] != c {
		t.Fatal("revocation lost after restart", e)
	}
	if s.SaveSSHTarget(ctx, request, "candidate", c, digest, 32) == nil {
		t.Fatal("restart resurrected revoked identity")
	}
}
