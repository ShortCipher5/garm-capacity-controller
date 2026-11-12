//go:build linux && cgo

package supervisoraudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func claim(n int) string { return fmt.Sprintf("%064x", n) }
func setup(t *testing.T) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "audit.db")
	j, e := InitializeNew(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { j.Close() })
	return j, p
}
func TestHistoryReplayAndConflicts(t *testing.T) {
	j, p := setup(t)
	a, e := j.RecordClaim(claim(1), claim(2))
	if e != nil || a.Replay || a.Boundary != Boundary {
		t.Fatal(a, e)
	}
	b, e := j.RecordClaim(claim(1), claim(2))
	if e != nil || !b.Replay || a.Fact != b.Fact {
		t.Fatal(b, e)
	}
	for _, pair := range [][2]int{{1, 3}, {4, 2}} {
		if _, e = j.RecordClaim(claim(pair[0]), claim(pair[1])); e != ErrConflict {
			t.Fatal(e)
		}
	}
	k, e := OpenExisting(p, j.HistoryID())
	if e != nil {
		t.Fatal(e)
	}
	defer k.Close()
	b, e = k.RecordClaim(claim(1), claim(2))
	if e != nil || !b.Replay {
		t.Fatal(b, e)
	}
}
func TestConcurrentHandles(t *testing.T) {
	j, p := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, e := OpenExisting(p, j.HistoryID())
			if e == nil {
				defer k.Close()
				_, e = k.RecordClaim(claim(1), claim(2))
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 1 {
		t.Fatal(s, e)
	}
}
func TestBoundedSnapshot(t *testing.T) {
	j, _ := setup(t)
	for i := 1; i <= 105; i++ {
		if _, e := j.RecordClaim(claim(i), claim(i+1000)); e != nil {
			t.Fatal(e)
		}
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 105 || len(s.Recent) != 100 || s.Boundary != Boundary {
		t.Fatal(s, e)
	}
	o, e := j.RecordClaim(claim(1), claim(1001))
	if e != nil || !o.Replay || o.Fact.Sequence != 1 {
		t.Fatal(o, e)
	}
}
func TestLostCommitReply(t *testing.T) {
	j, p := setup(t)
	j.commit = func(c *sql.Conn) error {
		if _, e := c.ExecContext(context.Background(), "COMMIT"); e != nil {
			t.Fatal(e)
		}
		return errors.New("lost")
	}
	if _, e := j.RecordClaim(claim(1), claim(2)); e != ErrUncertain {
		t.Fatal(e)
	}
	if _, e := j.Snapshot(); e == nil {
		t.Fatal("poison bypass")
	}
	j.Close()
	k, e := OpenExisting(p, j.HistoryID())
	if e != nil {
		t.Fatal(e)
	}
	defer k.Close()
	o, e := k.RecordClaim(claim(1), claim(2))
	if e != nil || !o.Replay || o.Fact.Sequence != 1 || o.Boundary != Boundary {
		t.Fatal(o, e)
	}
}
func TestRollbackOnCommitFailure(t *testing.T) {
	j, p := setup(t)
	j.commit = func(*sql.Conn) error { return errors.New("failure") }
	if _, e := j.RecordClaim(claim(1), claim(2)); e != ErrUncertain {
		t.Fatal(e)
	}
	j.Close()
	k, e := OpenExisting(p, j.HistoryID())
	if e != nil {
		t.Fatal(e)
	}
	defer k.Close()
	s, e := k.Snapshot()
	if e != nil || s.Total != 0 {
		t.Fatal(s, e)
	}
}
func TestClosedSnapshotRollbackCounterexample(t *testing.T) {
	j, p := setup(t)
	id := j.HistoryID()
	j.Close()
	old, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	k, e := OpenExisting(p, id)
	if e != nil {
		t.Fatal(e)
	}
	o, e := k.RecordClaim(claim(1), claim(2))
	if e != nil || o.Replay {
		t.Fatal(e)
	}
	k.Close()
	if e = os.WriteFile(p, old, 0600); e != nil {
		t.Fatal(e)
	}
	r, e := OpenExisting(p, id)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	again, e := r.RecordClaim(claim(1), claim(2))
	if e != nil || again.Replay || again.Boundary != Boundary {
		t.Fatal(again, e)
	} // An old closed same-history snapshot defeats local uniqueness. Neither fact authorizes launch.
}
func TestWrongFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	p := filepath.Join(dir, "other.db")
	os.WriteFile(p, nil, 0600)
	d, e := connect(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.Exec("CREATE TABLE unrelated(value TEXT)"); e != nil {
		t.Fatal(e)
	}
	d.Close()
	before, _ := os.ReadFile(p)
	if _, e = OpenExisting(p, claim(1)); e != ErrHistory {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("wrong DB changed")
	}
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, e = os.Lstat(p + s); !os.IsNotExist(e) {
			t.Fatal("sidecar", s, e)
		}
	}
}
func TestPathAndCorruptionRefusal(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "replace", "schema", "audit", "missing"} {
		t.Run(kind, func(t *testing.T) {
			j, p := setup(t)
			switch kind {
			case "mode":
				os.Chmod(p, 0644)
			case "symlink":
				os.Rename(p, p+".old")
				os.Symlink(p+".old", p)
			case "replace":
				os.Rename(p, p+".old")
				os.WriteFile(p, []byte("other"), 0600)
			case "schema":
				j.db.Exec("DROP TRIGGER facts_no_delete")
			case "audit":
				j.db.Exec("PRAGMA foreign_keys=OFF")
				if _, e := j.db.Exec("INSERT INTO audit VALUES (1,?,?)", claim(1), claim(2)); e != nil {
					t.Fatal(e)
				}
			case "missing":
				os.Remove(p)
			}
			if _, e := j.RecordClaim(claim(1), claim(2)); e == nil {
				t.Fatal("accepted changed history")
			}
		})
	}
}
func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("SUPERVISOR_AUDIT_TEST_CHILD")
	if mode == "" {
		return
	}
	j, e := OpenExisting(os.Getenv("TEST_DB"), os.Getenv("TEST_HISTORY"))
	if e != nil {
		os.Exit(71)
	}
	if mode == "crash-before" {
		j.commit = func(*sql.Conn) error { os.Exit(72); return nil }
	}
	if mode == "crash-after" {
		j.commit = func(c *sql.Conn) error {
			if _, e := c.ExecContext(context.Background(), "COMMIT"); e != nil {
				os.Exit(71)
			}
			os.Exit(72)
			return nil
		}
	}
	if _, e = j.RecordClaim(claim(1), claim(2)); e != nil {
		os.Exit(71)
	}
	j.Close()
	os.Exit(0)
}
func child(t *testing.T, p, id, mode string) *exec.Cmd {
	t.Helper()
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := exec.Command(bin, "-test.run=^TestProcessHelper$")
	c.Env = []string{"SUPERVISOR_AUDIT_TEST_CHILD=" + mode, "TEST_DB=" + p, "TEST_HISTORY=" + id, "GOMAXPROCS=2"}
	return c
}
func TestActualProcessCrash(t *testing.T) {
	for _, mode := range []string{"crash-before", "crash-after"} {
		t.Run(mode, func(t *testing.T) {
			j, p := setup(t)
			id := j.HistoryID()
			j.Close()
			if e := child(t, p, id, mode).Run(); e == nil {
				t.Fatal("expected crash")
			} else {
				var exit *exec.ExitError
				if !errors.As(e, &exit) || exit.ExitCode() != 72 {
					t.Fatalf("unexpected child outcome: %v", e)
				}
			}
			k, e := OpenExisting(p, id)
			if e != nil {
				t.Fatal(e)
			}
			defer k.Close()
			o, e := k.RecordClaim(claim(1), claim(2))
			if e != nil || o.Replay != (mode == "crash-after") || o.Boundary != Boundary {
				t.Fatal(o, e)
			}
		})
	}
}
func TestConcurrentProcesses(t *testing.T) {
	j, p := setup(t)
	commands := []*exec.Cmd{}
	for i := 0; i < 4; i++ {
		c := child(t, p, j.HistoryID(), "record")
		if e := c.Start(); e != nil {
			t.Fatal(e)
		}
		commands = append(commands, c)
	}
	for _, c := range commands {
		if e := c.Wait(); e != nil {
			t.Fatal(e)
		}
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 1 {
		t.Fatal(s, e)
	}
}
func TestProductionHasNoExecutionOrAdmissionImports(t *testing.T) {
	b, e := os.ReadFile("journal_linux.go")
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{`"os/exec"`, `"net/http"`, `syscall.Exec(`, `syscall.ForkExec(`, `/admission"`} {
		if strings.Contains(string(b), bad) {
			t.Fatal(bad)
		}
	}
}

func TestAtomicAuditFailureRollback(t *testing.T) {
	j, _ := setup(t)
	j.mu.Lock()
	e := j.transaction(func(c *sql.Conn) error {
		if _, e := c.ExecContext(context.Background(), "INSERT INTO facts(scope_digest,attempt_digest) VALUES (?,?)", claim(1), claim(2)); e != nil {
			t.Fatal(e)
		}
		// A real audit NOT NULL violation after the fact insert must roll both back.
		if _, e := c.ExecContext(context.Background(), "INSERT INTO audit VALUES (1,NULL,?)", claim(2)); e == nil {
			t.Fatal("expected constraint failure")
		}
		return ErrHistory
	})
	j.mu.Unlock()
	if e != ErrHistory {
		t.Fatal(e)
	}
	s, e := j.Snapshot()
	if e != nil || s.Total != 0 {
		t.Fatal(s, e)
	}
	o, e := j.RecordClaim(claim(1), claim(2))
	if e != nil || o.Fact.Sequence != 1 {
		t.Fatal(o, e)
	}
}
func TestInitializationAndOpenRefusals(t *testing.T) {
	j, p := setup(t)
	if _, e := InitializeNew(p); e == nil {
		t.Fatal("initialized existing history")
	}
	if _, e := OpenExisting(p, claim(88)); e == nil {
		t.Fatal("wrong local history")
	}
	absent := filepath.Join(filepath.Dir(p), "absent")
	if _, e := OpenExisting(absent, j.HistoryID()); e == nil {
		t.Fatal("created missing")
	}
	if _, e := os.Stat(absent); !os.IsNotExist(e) {
		t.Fatal("missing file materialized")
	}
	for _, v := range []string{"", strings.Repeat("A", 64), strings.Repeat("0", 63), strings.Repeat("g", 64)} {
		if _, e := j.RecordClaim(v, claim(2)); e == nil {
			t.Fatal("noncanonical claim")
		}
	}
	j.Close()
	target := filepath.Join(filepath.Dir(p), "target")
	if e := os.WriteFile(target, []byte("untouched"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, p+"-wal"); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenExisting(p, j.HistoryID()); e == nil {
		t.Fatal("followed sidecar symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "untouched" {
		t.Fatal("changed sidecar target")
	}
}
func TestConcurrentConflictingScopes(t *testing.T) {
	j, p := setup(t)
	var wg sync.WaitGroup
	out := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, e := OpenExisting(p, j.HistoryID())
			if e == nil {
				defer k.Close()
				_, e = k.RecordClaim(claim(1), claim(i+100))
			}
			out <- e
		}(i)
	}
	wg.Wait()
	close(out)
	success := 0
	for e := range out {
		if e == nil {
			success++
		} else if e != ErrConflict {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}

func TestPostCommitPathChangeAndZeroSnapshot(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
			j, p := setup(t)
			if _, e := j.RecordClaim(claim(1), claim(2)); e != nil {
				t.Fatal(e)
			}
			j.commit = func(c *sql.Conn) error {
				if _, e := c.ExecContext(context.Background(), "COMMIT"); e != nil {
					return e
				}
				return os.Chmod(p, 0644)
			}
			if snapshot {
				s, e := j.Snapshot()
				if e != ErrUncertain || s.Total != 0 || len(s.Recent) != 0 || s.Boundary != Boundary {
					t.Fatal(s, e)
				}
			} else {
				o, e := j.RecordClaim(claim(3), claim(4))
				if e != ErrUncertain || o.Fact.Sequence != 0 || o.Boundary != Boundary {
					t.Fatal(o, e)
				}
			}
			if _, e := j.Snapshot(); e == nil {
				t.Fatal("not poisoned")
			}
		})
	}
}
func TestHardLinkRefusal(t *testing.T) {
	j, p := setup(t)
	if e := os.Link(p, p+".link"); e != nil {
		t.Fatal(e)
	}
	if _, e := j.Snapshot(); e == nil {
		t.Fatal("hardlinked DB allowed")
	}
	if _, e := OpenExisting(p, j.HistoryID()); e == nil {
		t.Fatal("hardlinked open allowed")
	}
}
func TestHistoryLimitNoResetOrReplayLoss(t *testing.T) {
	j, _ := setup(t)
	j.mu.Lock()
	e := j.transaction(func(c *sql.Conn) error {
		for i := 1; i <= MaxFacts; i++ {
			if _, e := c.ExecContext(context.Background(), "INSERT INTO facts VALUES (?,?,?)", i, claim(i), claim(i+10000)); e != nil {
				return e
			}
			if _, e := c.ExecContext(context.Background(), "INSERT INTO audit VALUES (?,?,?)", i, claim(i), claim(i+10000)); e != nil {
				return e
			}
		}
		return nil
	})
	j.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{claim(1), claim(20000)}, {claim(MaxFacts + 1), claim(10001)}} {
		o, e := j.RecordClaim(pair[0], pair[1])
		if e != ErrConflict || o.Fact.Sequence != 0 || o.Boundary != Boundary {
			t.Fatal(o, e)
		}
	}
	if _, e = j.RecordClaim(claim(MaxFacts+1), claim(20000)); e == nil {
		t.Fatal("unbounded history")
	}
	o, e := j.RecordClaim(claim(1), claim(10001))
	if e != nil || !o.Replay || o.Fact.Sequence != 1 {
		t.Fatal(o, e)
	}
}

func TestInitializeNewPreservesAllOrphanSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		for _, kind := range []string{"regular", "directory", "symlink", "dangling", "hardlink"} {
			t.Run(suffix+kind, func(t *testing.T) {
				dir := t.TempDir()
				os.Chmod(dir, 0700)
				p := filepath.Join(dir, "new.db")
				side := p + suffix
				target := filepath.Join(dir, "sentinel")
				sentinel := []byte("associated history must remain untouched")
				if e := os.WriteFile(target, sentinel, 0600); e != nil {
					t.Fatal(e)
				}
				var e error
				switch kind {
				case "regular":
					e = os.WriteFile(side, sentinel, 0600)
				case "directory":
					e = os.Mkdir(side, 0700)
				case "symlink":
					e = os.Symlink(target, side)
				case "dangling":
					e = os.Symlink(target+".absent", side)
				case "hardlink":
					e = os.Link(target, side)
				}
				if e != nil {
					t.Fatal(e)
				}
				before, e := os.Lstat(side)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = InitializeNew(p); e != ErrHistory {
					t.Fatal(e)
				}
				if _, e = os.Lstat(p); !os.IsNotExist(e) {
					t.Fatal("main was created", e)
				}
				after, e := os.Lstat(side)
				if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
					t.Fatal("orphan changed", e)
				}
				b, e := os.ReadFile(target)
				if e != nil || string(b) != string(sentinel) {
					t.Fatal("sentinel changed", e)
				}
				if kind == "regular" {
					b, e = os.ReadFile(side)
					if e != nil || string(b) != string(sentinel) {
						t.Fatal("orphan contents changed", e)
					}
				}
			})
		}
	}
}
func TestOpenDuringForcedCheckpoints(t *testing.T) {
	j, p := setup(t)
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 1; i <= 80; i++ {
			if _, e := j.RecordClaim(claim(i), claim(i+1000)); e != nil {
				errs <- e
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		d, e := connect(p)
		if e != nil {
			errs <- e
			return
		}
		defer d.Close()
		for i := 0; i < 80; i++ {
			if _, e = d.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
				errs <- e
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 80; i++ {
			k, e := OpenExisting(p, j.HistoryID())
			if e != nil {
				errs <- e
				return
			}
			s, e := k.Snapshot()
			k.Close()
			if e != nil || s.Boundary != Boundary {
				errs <- ErrHistory
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}
func TestReplacementConnectionPolicyAndZeroClose(t *testing.T) {
	var zero Journal
	if e := zero.Close(); e != ErrHistory {
		t.Fatal(e)
	}
	j, _ := setup(t)
	j.db.SetMaxIdleConns(0)
	for i := 1; i <= 3; i++ {
		c, e := j.db.Conn(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		var sync, fk int
		if e = c.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&sync); e != nil {
			t.Fatal(e)
		}
		if e = c.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk); e != nil {
			t.Fatal(e)
		}
		c.Close()
		if sync != 2 || fk != 1 {
			t.Fatal(sync, fk)
		}
		if _, e = j.RecordClaim(claim(i), claim(i+100)); e != nil {
			t.Fatal(e)
		}
	}
}
