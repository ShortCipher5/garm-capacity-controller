package admission

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
	"time"
)

func TestManagedRotationDurablePhasesReceiptAndReplay(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if e := s.InitializeManagedRotation(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	request := "11111111-1111-4111-8111-111111111111"
	s.db.Exec(`CREATE TRIGGER deny_rotation BEFORE INSERT ON audit WHEN NEW.kind='host_inventory_rotation_requested' BEGIN SELECT RAISE(ABORT,'denied'); END`)
	if _, _, e := s.BeginManagedRotation(ctx, "one", digest, request, false); e == nil {
		t.Fatal("audit bypass")
	}
	ops, _ := s.ManagedRotations(ctx)
	if len(ops) != 0 {
		t.Fatal("partial intent")
	}
	s.db.Exec(`DROP TRIGGER deny_rotation`)
	op, run, e := s.BeginManagedRotation(ctx, "one", digest, request, false)
	if e != nil || !run || op.Phase != "approved" {
		t.Fatal(op, e)
	}
	if _, run, e = s.BeginManagedRotation(ctx, "one", digest, request, false); e != nil || run {
		t.Fatal("auto retry")
	}
	if s.MarkManagedRotation(ctx, "one", digest, request, "committed") == nil {
		t.Fatal("phase skipped")
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(private)
	receipt := firstboot.RotationReceipt{State: "revoked", Digest: strings.Repeat("b", 64), HostKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), OldKeyRemoved: true, SessionsTerminated: true}
	if s.CompleteManagedRotation(ctx, "one", digest, request, receipt) == nil {
		t.Fatal("completion before commit")
	}
	for _, phase := range []string{"staged", "proved", "registry_switched", "committed"} {
		if e = s.MarkManagedRotation(ctx, "one", digest, request, phase); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.MarkManagedRotation(ctx, "one", digest, request, "staged"); e != nil {
		t.Fatal("retry cannot revisit earlier steps")
	}
	ops, _ = s.ManagedRotations(ctx)
	if ops["one"].Phase != "committed" {
		t.Fatal("verified progress regressed")
	}
	s.db.Exec(`CREATE TRIGGER deny_finish BEFORE INSERT ON audit WHEN NEW.kind='host_inventory_rotation_completed' BEGIN SELECT RAISE(ABORT,'denied'); END`)
	if s.CompleteManagedRotation(ctx, "one", digest, request, receipt) == nil {
		t.Fatal("unaudited completion")
	}
	ops, _ = s.ManagedRotations(ctx)
	if ops["one"].Receipt != nil || ops["one"].State != "unknown" {
		t.Fatal("partial receipt")
	}
	s.db.Exec(`DROP TRIGGER deny_finish`)
	if e = s.CompleteManagedRotation(ctx, "one", digest, request, receipt); e != nil {
		t.Fatal(e)
	}
	op, run, e = s.BeginManagedRotation(ctx, "one", digest, request, true)
	if e != nil || run || op.Phase != "verified" || op.Receipt == nil {
		t.Fatal("lost result", op, e)
	}
	if _, _, e = s.BeginManagedRotation(ctx, "one", strings.Repeat("c", 64), request, true); e == nil {
		t.Fatal("request rebound")
	}
}

func TestManagedRotationRegistryPreflightAndScopedAudit(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if e := s.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	old := sshinventory.Config{Address: "192.0.2.20", Port: 19922, User: "garm-inventory-listener", HostKey: "fixture-public-key", CredentialRef: "old"}
	next := old
	next.CredentialRef = "new"
	od := sshinventory.ReviewDigest("old", old)
	nd := sshinventory.ReviewDigest("new", next)
	req := "11111111-1111-4111-8111-111111111111"
	if s.CheckSSHTargetReplacement(ctx, req, "old", od, "new", next, nd) == nil {
		t.Fatal("absent old target accepted")
	}
	if e := s.SaveSSHTarget(ctx, "22222222-2222-4222-8222-222222222222", "old", old, od, 32); e != nil {
		t.Fatal(e)
	}
	if e := s.CheckSSHTargetReplacement(ctx, req, "old", od, "new", next, nd); e != nil {
		t.Fatal(e)
	}
	list, _ := s.ListSSHTargets(ctx)
	if len(list) != 1 || list["old"] != old {
		t.Fatal("preflight changed registry")
	}
	if e := s.ReplaceSSHTargetForRotation(ctx, req, "old", od, "new", next, nd, 32); e != nil {
		t.Fatal(e)
	}
	var audit string
	if e := s.db.QueryRow(`SELECT request_key FROM audit WHERE kind='ssh_target_replaced' ORDER BY sequence DESC LIMIT 1`).Scan(&audit); e != nil || !strings.Contains(audit, "inventory-key-rotation-token") {
		t.Fatal("wrong approval role", audit, e)
	}
	if e := s.CheckSSHTargetReplacement(ctx, req, "old", od, "new", next, nd); e != nil {
		t.Fatal("own replacement cannot resume", e)
	}
	if e := s.RevokeSSHTarget(ctx, "33333333-3333-4333-8333-333333333333", "new", nd); e != nil {
		t.Fatal(e)
	}
	if s.CheckSSHTargetReplacement(ctx, req, "old", od, "new", next, nd) == nil {
		t.Fatal("revoked new permission resurrected")
	}
	if s.ReplaceSSHTargetForRotation(ctx, req, "old", od, "new", next, nd, 32) == nil {
		t.Fatal("revoked replacement replayed")
	}
}
