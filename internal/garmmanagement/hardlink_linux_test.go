package garmmanagement

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Closed disposable SQLite files ONLY. Hard-link rejection is a namespace
// precondition, not an external-writer fence or historical-copy custody proof.
func TestJournalRejectsHardLinkedStateAndRetainsUnknown(t *testing.T) {
	manager, journal, remote, path := setup(t)
	remote.fail = errors.New("synthetic uncertain transport")
	if _, err := manager.UpdatePool(context.Background(), requestID, poolID, fingerprint, structPoolPriority(2), "offline uncertain fixture"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("fixture did not remain uncertain", err)
	}
	lineage := journal.Lineage()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "same-inode-alias.db")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, alias} {
		opened, err := OpenJournal(candidate, lineage)
		if opened != nil {
			_ = opened.Close()
		}
		if !errors.Is(err, ErrJournal) {
			t.Fatalf("hard-linked journal accepted: %s: %v", filepath.Base(candidate), err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused alias changed closed database", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(alias + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused alias created sidecar %s: %v", suffix, err)
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatal("single-link journal no longer opens", err)
	}
	defer reopened.Close()
	restored, err := New(remote, reopened)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restored.Status(context.Background())
	if err != nil || !status.WriteBlocked || len(status.Pending) != 1 || status.Pending[0].RequestID != requestID || status.Pending[0].State != "unknown" {
		t.Fatal("alias rejection cleared or changed unresolved history", status, err)
	}
}

func TestJournalOpenerKeepsUnsafePathRefusals(t *testing.T) {
	dir := privateJournalDir(t)
	path := filepath.Join(dir, "single-link.db")
	j, err := InitializeJournal(path)
	if err != nil {
		t.Fatal("legitimate single-link initialization", err)
	}
	lineage := j.Lineage()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(dir, "symlink.db")
		if err := os.Symlink(path, alias); err != nil {
			t.Fatal(err)
		}
		if opened, err := OpenJournal(alias, lineage); !errors.Is(err, ErrJournal) {
			if opened != nil {
				_ = opened.Close()
			}
			t.Fatal("symlink accepted", err)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		fifo := filepath.Join(dir, "fifo.db")
		if err := syscall.Mkfifo(fifo, 0600); err != nil {
			t.Fatal(err)
		}
		if opened, err := OpenJournal(fifo, lineage); !errors.Is(err, ErrJournal) {
			if opened != nil {
				_ = opened.Close()
			}
			t.Fatal("FIFO accepted", err)
		}
	})
	t.Run("group visible", func(t *testing.T) {
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(path, 0600); err != nil {
				t.Error(err)
			}
		}()
		if opened, err := OpenJournal(path, lineage); !errors.Is(err, ErrJournal) {
			if opened != nil {
				_ = opened.Close()
			}
			t.Fatal("group-visible file accepted", err)
		}
	})
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsafe path refusals modified database", err)
	}
	reopened, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatal("safe original stopped opening", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
