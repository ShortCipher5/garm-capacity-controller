package admission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapUnknownIsAtomicDurableAndNeverAutoRetried(t *testing.T) {
	ctx := context.Background()
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	if e := s.InitializeBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	id := "11111111-1111-4111-8111-111111111111"
	if _, e := s.db.Exec(`CREATE TRIGGER reject_bootstrap BEFORE INSERT ON audit WHEN NEW.kind='host_bootstrap_requested' BEGIN SELECT RAISE(ABORT,'denied'); END`); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.BeginBootstrap(ctx, "one", digest, id, false); e == nil {
		t.Fatal("audit failure ignored")
	}
	ops, e := s.BootstrapOperations(ctx)
	if e != nil || len(ops) != 0 {
		t.Fatal("partial durable operation")
	}
	s.db.Exec(`DROP TRIGGER reject_bootstrap`)
	op, run, e := s.BeginBootstrap(ctx, "one", digest, id, false)
	if e != nil || !run || op.State != "unknown" {
		t.Fatal(op, run, e)
	}
	if _, run, e = s.BeginBootstrap(ctx, "one", digest, id, false); e != nil || run {
		t.Fatal("replay triggered contact")
	}
	if _, _, e = s.BeginBootstrap(ctx, "one", strings.Repeat("b", 64), id, true); e == nil {
		t.Fatal("changed plan replay")
	}
	if _, _, e = s.BeginBootstrap(ctx, "other", digest, id, false); e == nil {
		t.Fatal("request reused for different target")
	}
	if e = s.InitializeBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	op, run, e = s.BeginBootstrap(ctx, "one", digest, id, true)
	if e != nil || !run || op.Attempts != 2 {
		t.Fatal(op, run, e)
	}
	if e = s.CompleteBootstrap(ctx, "one", digest, id); e != nil {
		t.Fatal(e)
	}
	if _, run, e = s.BeginBootstrap(ctx, "one", digest, id, true); e != nil || run {
		t.Fatal("installed reran")
	}
}

func TestBootstrapUnknownSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	config := fixture(time.Now())
	s, e := OpenSQLite(path, config)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InitializeBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	digest := strings.Repeat("a", 64)
	request := "11111111-1111-4111-8111-111111111111"
	if _, run, e := s.BeginBootstrap(ctx, "one", digest, request, false); e != nil || !run {
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
	if e = s.InitializeBootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	op, run, e := s.BeginBootstrap(ctx, "one", digest, request, false)
	if e != nil || run || op.State != "unknown" || op.Attempts != 1 {
		t.Fatal("restart replayed or lost operation", op, run, e)
	}
}
