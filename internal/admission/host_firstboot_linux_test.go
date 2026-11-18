package admission

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"golang.org/x/crypto/ssh"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirstbootUnknownIsAtomicDurableAndNeverAutoRetried(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if e := s.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	id := "11111111-1111-4111-8111-111111111111"
	if _, e := s.db.Exec(`CREATE TRIGGER reject_firstboot BEFORE INSERT ON audit WHEN NEW.kind='host_firstboot_requested' BEGIN SELECT RAISE(ABORT,'denied'); END`); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.BeginFirstboot(ctx, "one", digest, id, false); e == nil {
		t.Fatal("audit failure ignored")
	}
	ops, e := s.FirstbootOperations(ctx)
	if e != nil || len(ops) != 0 {
		t.Fatal("partial durable operation")
	}
	s.db.Exec(`DROP TRIGGER reject_firstboot`)
	op, run, e := s.BeginFirstboot(ctx, "one", digest, id, false)
	if e != nil || !run || op.State != "unknown" {
		t.Fatal(op, run, e)
	}
	if _, run, e = s.BeginFirstboot(ctx, "one", digest, id, false); e != nil || run {
		t.Fatal("replay triggered contact")
	}
	if _, _, e = s.BeginFirstboot(ctx, "one", strings.Repeat("b", 64), id, true); e == nil {
		t.Fatal("changed plan replay")
	}
	if _, _, e = s.BeginFirstboot(ctx, "other", digest, id, false); e == nil {
		t.Fatal("request reused for different target")
	}
	if e = s.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	op, run, e = s.BeginFirstboot(ctx, "one", digest, id, true)
	if e != nil || !run || op.Attempts != 2 {
		t.Fatal(op, run, e)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(private)
	receipt := firstboot.Receipt{State: "installed", Digest: digest, HostKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), ListenPort: 19922}
	if e = s.CompleteFirstboot(ctx, "one", digest, id, receipt); e != nil {
		t.Fatal(e)
	}
	if _, run, e = s.BeginFirstboot(ctx, "one", digest, id, true); e != nil || run {
		t.Fatal("installed reran")
	}
}

func TestFirstbootUnknownSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	config := fixture(time.Now())
	s, e := OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	request := "11111111-1111-4111-8111-111111111111"
	if _, run, e := s.BeginFirstboot(ctx, "one", digest, request, false); e != nil || !run {
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
	if e = s.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	op, run, e := s.BeginFirstboot(ctx, "one", digest, request, false)
	if e != nil || run || op.State != "unknown" || op.Attempts != 1 {
		t.Fatal("restart replayed or lost operation", op, run, e)
	}
}

func TestFirstbootReceiptCommitIsAtomicAndImmutable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	config := fixture(time.Now())
	s, e := OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	id := "11111111-1111-4111-8111-111111111111"
	if _, _, e = s.BeginFirstboot(ctx, "one", digest, id, false); e != nil {
		t.Fatal(e)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(private)
	receipt := firstboot.Receipt{State: "installed", Digest: digest, ListenAddress: "192.0.2.20", ListenPort: 19922, InventoryAddress: "192.0.2.20", InventoryPort: 19922, HostKey: string(ssh.MarshalAuthorizedKey(key.PublicKey()))}
	if _, e = s.db.Exec(`CREATE TRIGGER reject_firstboot_complete BEFORE INSERT ON audit WHEN NEW.kind='host_firstboot_installed' BEGIN SELECT RAISE(ABORT,'denied'); END`); e != nil {
		t.Fatal(e)
	}
	if s.CompleteFirstboot(ctx, "one", digest, id, receipt) == nil {
		t.Fatal("unaudited receipt")
	}
	ops, e := s.FirstbootOperations(ctx)
	if e != nil || ops["one"].State != "unknown" || ops["one"].Receipt != nil {
		t.Fatal("partial completion", ops, e)
	}
	s.db.Exec(`DROP TRIGGER reject_firstboot_complete`)
	if e = s.CompleteFirstboot(ctx, "one", digest, id, receipt); e != nil {
		t.Fatal(e)
	}
	changed := receipt
	changed.InventoryAddress = "192.0.2.21"
	if s.CompleteFirstboot(ctx, "one", digest, id, changed) == nil {
		t.Fatal("receipt overwritten")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ops, e = s.FirstbootOperations(ctx)
	if e != nil || ops["one"].Receipt == nil || *ops["one"].Receipt != receipt {
		t.Fatal("receipt lost on reopen", ops, e)
	}
}
