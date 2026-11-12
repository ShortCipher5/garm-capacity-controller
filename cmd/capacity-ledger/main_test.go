package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func fixture(t *testing.T) (string, *admission.SQLiteStore) {
	t.Helper()
	budget := admission.Resources{CPU: 8000, RAM: 16384, Runners: 8}
	config := admission.Config{Global: budget, MaxObservationAge: time.Hour,
		Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}},
		Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}}
	path := filepath.Join(privateDir(t), "source.db")
	s, err := admission.OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	fence, err := s.BeginReconciliation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteReconciliation(context.Background(), fence, inventory()); err != nil {
		t.Fatal(err)
	}
	return path, s
}
func inventory() admission.InventorySnapshot {
	return admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host", ObservedAt: time.Now(), Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}}
}
func request(key string) admission.Request {
	return admission.Request{Key: key, Host: "host", Pool: "pool", Resources: admission.Resources{CPU: 1000, RAM: 2048, Runners: 1}}
}
func invoke(ctx context.Context, args ...string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	code := run(ctx, args, &out, &diagnostic)
	return code, out.String(), diagnostic.String()
}
func TestBackupRestoreCLICommittedWALAndReconciliationBoundary(t *testing.T) {
	ctx := context.Background()
	path, source := fixture(t)
	location := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dispatched", "waiting"} {
		if _, err = source.Admit(ctx, request(key)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = source.ClaimDispatch(ctx, request("dispatched")); err != nil {
		t.Fatal(err)
	}
	// An independent non-checkpointing writer leaves this commit in WAL.
	if _, err = db.Exec("UPDATE reservations SET state='unknown' WHERE request_key='dispatched'"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("missing WAL fixture: %v", err)
	}
	before, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(privateDir(t), "backup.db")
	if code, out, diagnostic := invoke(ctx, "backup", "--source", path, "--destination", backup); code != 0 || !strings.Contains(out, "admission blocked") || diagnostic != "" {
		t.Fatalf("backup %d %q %q", code, out, diagnostic)
	}
	after, err := source.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("source modified: %v", err)
	}
	restoredPath := filepath.Join(privateDir(t), "restore.db")
	if code, out, diagnostic := invoke(ctx, "restore", "--source", backup, "--destination", restoredPath, "--old-writers-fenced"); code != 0 || !strings.Contains(out, "NEW reconciliation barrier") || diagnostic != "" {
		t.Fatalf("restore %d %q %q", code, out, diagnostic)
	}
	for _, artifact := range []string{backup, restoredPath} {
		info, err := os.Stat(artifact)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("not private: %v", err)
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err = os.Lstat(artifact + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("artifact needs %s: %v", suffix, err)
			}
		}
	}
	restored, err := admission.OpenSQLite(restoredPath, admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, err := restored.Snapshot(ctx)
	if err != nil || state.Ready || state.Allocated.Global != before.Allocated.Global || state.Reservations["dispatched"].State != "unknown" {
		t.Fatalf("lost WAL/accounting or enabled admission: %+v %v", state, err)
	}
	var kinds []string
	for _, event := range state.Audit {
		if strings.HasPrefix(event.Kind, "backup_") {
			kinds = append(kinds, event.Kind)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"backup_restored", "backup_created"}) {
		t.Fatalf("export audit: %v", kinds)
	}
	if _, err = restored.Admit(ctx, request("new")); !errors.Is(err, admission.ErrReconciliation) {
		t.Fatalf("premature admission %v", err)
	}
	if _, err = restored.ClaimDispatch(ctx, request("waiting")); !errors.Is(err, admission.ErrReconciliation) {
		t.Fatalf("premature dispatch %v", err)
	}
	fence, err := restored.BeginReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.CompleteReconciliation(ctx, fence, admission.InventorySnapshot{}); !errors.Is(err, admission.ErrReconciliation) {
		t.Fatalf("incomplete reconcile accepted: %v", err)
	}
	fresh := inventory()
	fresh.Resources = []admission.InventoryResource{{ID: "post-backup-operation", Host: "host", Pool: "pool", State: "creating", Resources: request("orphan").Resources}}
	if err = restored.CompleteReconciliation(ctx, fence, fresh); err != nil {
		t.Fatal(err)
	}
	state, err = restored.Snapshot(ctx)
	if err != nil || !state.Ready || state.Allocated.Global.Runners != before.Allocated.Global.Runners+1 {
		t.Fatalf("post-backup orphan lost: %+v %v", state, err)
	}
	if _, err = restored.ClaimDispatch(ctx, request("dispatched")); !errors.Is(err, admission.ErrDispatchClaimed) {
		t.Fatalf("historical dispatch replayed: %v", err)
	}
}
func TestCLIValidationBeforeDatabaseAccess(t *testing.T) {
	for _, action := range []string{"backup", "restore"} {
		for _, extra := range [][]string{{"--unknown=private-secret"}, {"unexpected"}, {"--destination"}, {"--ready"}, {"--force"}, {"--source="}} {
			t.Run(action+strings.Join(extra, " "), func(t *testing.T) {
				dir := privateDir(t)
				args := append([]string{action, "--source", filepath.Join(dir, "missing.db"), "--destination", filepath.Join(dir, "out.db")}, extra...)
				code, out, diagnostic := invoke(context.Background(), args...)
				if code != 2 || out != "" || strings.Contains(diagnostic, "private-secret") || strings.Contains(diagnostic, dir) {
					t.Fatalf("validation %d %q %q", code, out, diagnostic)
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatal("invalid arguments accessed DB")
				}
			})
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"backup"}, {"restore"}} {
		if code, _, _ := invoke(context.Background(), args...); code != 2 {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"--help"}, {"backup", "--help"}, {"restore", "--help"}} {
		if code, out, diagnostic := invoke(context.Background(), args...); code != 0 || !strings.Contains(out, "cannot verify") || diagnostic != "" {
			t.Fatalf("help %d %q %q", code, out, diagnostic)
		}
	}
}
func TestCLIRestoreRequiresExplicitExternalFencing(t *testing.T) {
	path, _ := fixture(t)
	for _, extra := range [][]string{nil, {"--old-writers-fenced=false"}} {
		output := filepath.Join(privateDir(t), "new.db")
		args := append([]string{"restore", "--source", path, "--destination", output}, extra...)
		if code, out, diagnostic := invoke(context.Background(), args...); code != 2 || out != "" || !strings.Contains(diagnostic, "cannot verify external fencing") {
			t.Fatalf("fencing %d %q %q", code, out, diagnostic)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("restore wrote before acknowledgement")
		}
	}
}
func TestCLIRefusesUnsafeSourcesAndDestinations(t *testing.T) {
	ctx := context.Background()
	path, _ := fixture(t)
	for _, action := range []string{"backup", "restore"} {
		for _, kind := range []string{"existing", "wal", "shm", "journal", "symlink", "public-parent", "missing-source", "invalid-source", "symlink-source"} {
			t.Run(action+"/"+kind, func(t *testing.T) {
				dir := privateDir(t)
				output := filepath.Join(dir, "out.db")
				source := path
				protected := output
				switch kind {
				case "existing", "wal", "shm", "journal":
					if kind != "existing" {
						protected += "-" + kind
					}
					if err := os.WriteFile(protected, []byte("retain this evidence"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(path, protected); err != nil {
						t.Fatal(err)
					}
				case "public-parent":
					if err := os.Chmod(dir, 0755); err != nil {
						t.Fatal(err)
					}
				case "missing-source":
					source = filepath.Join(dir, "missing.db")
				case "invalid-source":
					source = filepath.Join(dir, "invalid.db")
					if err := os.WriteFile(source, []byte("not a ledger"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink-source":
					source = filepath.Join(dir, "link.db")
					if err := os.Symlink(path, source); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{action, "--source", source, "--destination", output}
				if action == "restore" {
					args = append(args, "--old-writers-fenced")
				}
				if code, out, diagnostic := invoke(ctx, args...); code != 1 || out != "" || strings.Contains(diagnostic, dir) {
					t.Fatalf("unsafe path %d %q %q", code, out, diagnostic)
				}
				switch kind {
				case "symlink":
					if target, err := os.Readlink(protected); err != nil || target != path {
						t.Fatal("changed symlink")
					}
				case "existing", "wal", "shm", "journal":
					if data, err := os.ReadFile(protected); err != nil || string(data) != "retain this evidence" {
						t.Fatal("changed evidence")
					}
				}
				if kind != "existing" && kind != "symlink" {
					if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("published unsafe output")
					}
				}
				if kind == "missing-source" {
					if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("created missing source")
					}
				}
			})
		}
	}
}
func TestCLIErrorCategoriesAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		text string
	}{{fmt.Errorf("secret: %w", admission.ErrUncertain), 3, "may have been published"}, {os.ErrExist, 1, "already exists"}, {context.Canceled, 1, "interrupted"}, {context.DeadlineExceeded, 1, "interrupted"}, {errors.New("private-secret"), 1, "export failed"}} {
		var diagnostic bytes.Buffer
		if code := reportError(tc.err, &diagnostic); code != tc.code || !strings.Contains(diagnostic.String(), tc.text) || strings.Contains(diagnostic.String(), "secret") {
			t.Fatalf("unsafe diagnostic %d %q", code, diagnostic.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path, _ := fixture(t)
	output := filepath.Join(privateDir(t), "cancelled.db")
	if code, out, diagnostic := invoke(ctx, "backup", "--source", path, "--destination", output); code != 1 || out != "" || !strings.Contains(diagnostic, "interrupted") {
		t.Fatalf("cancel %d %q %q", code, out, diagnostic)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled export published")
	}
}
func TestCLIBinaryLocalBackupAndRestore(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "capacity-ledger")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	path, _ := fixture(t)
	backup := filepath.Join(privateDir(t), "backup.db")
	restored := filepath.Join(privateDir(t), "restored.db")
	for _, args := range [][]string{{"backup", "--source", path, "--destination", backup}, {"restore", "--source", backup, "--destination", restored, "--old-writers-fenced"}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil || !bytes.Contains(out, []byte("admission blocked")) {
			t.Fatalf("binary %s %v", out, err)
		}
	}
	for _, args := range [][]string{{"restore", "--source", backup, "--destination", filepath.Join(privateDir(t), "not-created.db")}, {"backup", "--source", path, "--destination", backup}} {
		if out, err := exec.Command(binary, args...).CombinedOutput(); err == nil || bytes.Contains(out, []byte("created:")) {
			t.Fatalf("failure reported success %s %v", out, err)
		}
	}
}
