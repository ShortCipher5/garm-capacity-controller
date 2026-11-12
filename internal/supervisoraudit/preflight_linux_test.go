//go:build linux && cgo

package supervisoraudit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func assertNoPreflightFiles(t *testing.T, dir string) {
	t.Helper()
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal("private preflight files not cleaned", len(entries), e)
	}
}
func assertNoOriginalSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, e := os.Lstat(path + suffix); !os.IsNotExist(e) {
			t.Fatal("original sidecar created", suffix, e)
		}
	}
}
func TestCopiedMetadataOnlyRetriesPhaseSpecificCorrupt(t *testing.T) {
	j, _ := setup(t)
	corrupt := &metadataReadFailure{sqlite3.Error{Code: sqlite3.ErrCorrupt, ExtendedCode: sqlite3.ErrNoExtended(sqlite3.ErrCorrupt)}}
	for name, issue := range map[string]error{"semantic": ErrHistory, "plain_sql_corrupt": sqlite3.Error{Code: sqlite3.ErrCorrupt}, "read_busy": &metadataReadFailure{sqlite3.Error{Code: sqlite3.ErrBusy}}, "read_io": &metadataReadFailure{sqlite3.Error{Code: sqlite3.ErrIoErr}}, "untyped": errors.New("database disk image is malformed")} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			e := j.preflightCopiedMetadata(func() error { calls++; return issue })
			if e != ErrHistory || calls != 1 {
				t.Fatal("non-copy-corrupt observation retried", calls, e)
			}
		})
	}
	calls := 0
	if e := j.preflightCopiedMetadata(func() error {
		calls++
		if calls == 1 {
			return corrupt
		}
		return nil
	}); e != nil || calls != 2 {
		t.Fatal("fresh copy observation did not recover", calls, e)
	}
	calls = 0
	if e := j.preflightCopiedMetadata(func() error { calls++; return corrupt }); e != ErrHistory || calls != copiedMetadataAttempts {
		t.Fatal("retry exhaustion not bounded", calls, e)
	}
	calls = 0
	if e := j.preflightCopiedMetadata(func() error {
		calls++
		if e := os.Chmod(j.path, 0644); e != nil {
			t.Fatal(e)
		}
		return corrupt
	}); e != ErrHistory || calls != 1 {
		t.Fatal("filesystem guard failure retried", calls, e)
	}
	if e := os.Chmod(j.path, 0600); e != nil {
		t.Fatal(e)
	}
	snapshot, e := j.Snapshot()
	if e != nil || snapshot.Total != 0 || snapshot.Boundary != Boundary {
		t.Fatal("preflight created historical facts", snapshot, e)
	}
}
func TestCopiedPreflightCleanupAndWrongHistoryPreservation(t *testing.T) {
	j, path := setup(t)
	history := j.HistoryID()
	j.Close()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{claim(999), history, claim(998)} {
		opened, e := OpenExisting(path, id)
		if id == history {
			if e != nil {
				t.Fatal(e)
			}
			opened.Close()
		} else if e != ErrHistory || opened != nil {
			t.Fatal("wrong history accepted", e)
		}
		assertNoPreflightFiles(t, directory)
		after, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(before, after) {
			t.Fatal("original history bytes changed", e)
		}
		assertNoOriginalSidecars(t, path)
	}
}
func TestCopiedPreflightOversizeRefusesWithoutOriginalMutation(t *testing.T) {
	j, path := setup(t)
	history := j.HistoryID()
	j.Close()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	if e := os.Truncate(path, MaxPreflightBytes+1); e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := OpenExisting(path, history)
	if e != ErrHistory || opened != nil {
		t.Fatal("oversize source accepted", e)
	}
	after, e := os.Stat(path)
	if e != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		t.Fatal("oversize original changed", e)
	}
	assertNoPreflightFiles(t, directory)
	assertNoOriginalSidecars(t, path)
}
func TestCopiedPreflightPersistentMetadataCorruptionRefuses(t *testing.T) {
	j, path := setup(t)
	history := j.HistoryID()
	j.Close()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	body, e := os.ReadFile(path)
	if e != nil || len(body) <= 100 {
		t.Fatal(e)
	}
	body[100] = 0 // Invalid first B-tree page; app/version raw header remain intact.
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	opened, e := OpenExisting(path, history)
	if e != ErrHistory || opened != nil {
		t.Fatal("persistent corrupt original accepted", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(body, after) {
		t.Fatal("corrupt original modified", e)
	}
	assertNoPreflightFiles(t, directory)
	assertNoOriginalSidecars(t, path)
}
func TestCopiedMetadataNeverSubstitutesForLockedLiveValidation(t *testing.T) {
	j, path := setup(t)
	history := j.HistoryID()
	if _, e := j.RecordClaim(claim(1), claim(1001)); e != nil {
		t.Fatal(e)
	}
	j.Close()
	body, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	marker := []byte(claim(1))
	at := bytes.Index(body, marker)
	if at < 100 {
		t.Fatal("fixture fact not found")
	}
	copy(body[at:at+len(marker)], []byte(claim(2))) // Same-size valid digest, broken facts/index/audit relation.
	if e = os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	f, e := private(path, false)
	if e != nil {
		t.Fatal(e)
	}
	p, e := private(filepath.Dir(path), true)
	if e != nil {
		t.Fatal(e)
	}
	probe := &Journal{path: path, file: f, parent: p, history: history}
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	if e = probe.preflightMetadata(); e != nil {
		t.Fatal("static metadata must remain valid in fixture", e)
	}
	opened, e := OpenExisting(path, history)
	if e != ErrHistory || opened != nil {
		t.Fatal("copy metadata bypassed original locked integrity", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(body, after) {
		t.Fatal("invalid original facts modified", e)
	}
	assertNoPreflightFiles(t, directory)
}
func TestPinnedSourceDescriptorRefusesPathSubstitution(t *testing.T) {
	j, path := setup(t)
	j.Close()
	original, e := private(path, false)
	if e != nil {
		t.Fatal(e)
	}
	target := path + ".preserved"
	if e = os.Rename(path, target); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	if f, e := openPinnedSource(path, original); e != ErrHistory || f != nil {
		t.Fatal("followed substituted source symlink", e)
	}
}
