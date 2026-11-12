package garmmanagement

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Operates ONLY on disposable, closed fixture databases. It is not a production
// restore routine or permission to rename/edit an installed management journal.
func exerciseWrongMounts(t *testing.T, path, lineage string) *Journal {
	t.Helper()
	preserved := path + ".preserved"
	if err := os.Rename(path, preserved); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path, lineage); !errors.Is(err, ErrJournal) {
		t.Fatal("missing mount was opened")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing mount silently created a journal")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path, lineage); !errors.Is(err, ErrJournal) {
		t.Fatal("empty replacement was adopted")
	}
	if _, err := InitializeJournal(path); !errors.Is(err, ErrJournal) {
		t.Fatal("initializer adopted existing empty file")
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatal("rejected empty file was changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	unrelated, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if unrelated.Lineage() == lineage {
		t.Fatal("initializer reused lineage")
	}
	if _, err = New(remoteFixture(), unrelated); err != nil {
		t.Fatal(err)
	}
	if err = unrelated.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJournal(path, lineage); !errors.Is(err, ErrJournal) {
		t.Fatal("different valid same-endpoint lineage accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected wrong lineage was modified")
	}
	if err = os.Rename(path, path+".unrelated"); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(preserved, path); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	return reopened
}
func TestJournalLineageMissingEmptyReplacementAndRestart(t *testing.T) {
	manager, journal, remote, path := setup(t)
	remote.fail = errors.New("unclassified transport outcome")
	if _, err := manager.UpdatePool(context.Background(), requestID, poolID, fingerprint, structPoolPriority(2), "uncertain fixture"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	lineage := journal.Lineage()
	if !validJournalLineage(lineage) {
		t.Fatal("invalid initialized lineage")
	}
	if journal.Close() != nil {
		t.Fatal("close failed")
	}
	reopened := exerciseWrongMounts(t, path, lineage)
	defer reopened.Close()
	manager, err := New(remote, reopened)
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil || !status.WriteBlocked || len(status.Pending) != 1 {
		t.Fatalf("pending lost after mount recovery %+v %v", status, err)
	}
	if _, err = manager.UpdatePool(context.Background(), nextID, poolID, remote.pool.Fingerprint, structPoolPriority(3), "must remain blocked"); !errors.Is(err, ErrBlocked) || remote.writes != 1 {
		t.Fatal("wrong mount allowed another dispatch")
	}
	for _, invalid := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64), lineage[:63]} {
		if _, err = OpenJournal(path, invalid); !errors.Is(err, ErrJournal) {
			t.Fatal("malformed lineage accepted")
		}
	}
	if _, err = reopened.db.Exec("UPDATE journal_identity SET lineage=? WHERE id=1", strings.Repeat("d", 64)); err == nil {
		t.Fatal("lineage mutable")
	}
	if _, err = reopened.db.Exec("DELETE FROM journal_identity"); err == nil {
		t.Fatal("lineage deletable")
	}
}
func TestJournalSchemaOneIsNotSilentlyMigrated(t *testing.T) {
	path := filepath.Join(privateJournalDir(t), "legacy.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema + `; DROP TRIGGER journal_identity_immutable_update; DROP TRIGGER journal_identity_immutable_delete; DROP TABLE journal_identity; PRAGMA user_version=1; PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = OpenJournal(path, strings.Repeat("a", 64)); !errors.Is(err, ErrJournal) {
		t.Fatal("legacy journal adopted")
	}
	if _, err = InitializeJournal(path); !errors.Is(err, ErrJournal) {
		t.Fatal("legacy journal reinitialized")
	}
	db, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var mode string
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || db.QueryRow("PRAGMA journal_mode").Scan(&mode) != nil || version != 1 || mode != "delete" {
		t.Fatal("rejected legacy metadata changed")
	}
}
func TestJournalRequiresPrivateParentWithoutChangingIt(t *testing.T) {
	directory := privateJournalDir(t)
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "journal.db")
	if _, err := InitializeJournal(path); !errors.Is(err, ErrJournal) {
		t.Fatal("public parent accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected parent still created a journal")
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("initializer changed unapproved directory permissions")
	}
}
func TestJournalInitializationIndependentProcessesExactlyOnce(t *testing.T) {
	path := filepath.Join(privateJournalDir(t), "journal.db")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	var wait sync.WaitGroup
	results := make(chan string, workers)
	for n := 0; n < workers; n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.Command(binary, "-test.run=^TestJournalInitializationProcessHelper$")
			command.Env = append(os.Environ(), "GARM_INITIALIZE_HELPER="+path)
			data, err := command.CombinedOutput()
			if err != nil {
				results <- "ERROR" + err.Error() + string(data)
			} else {
				results <- strings.TrimSpace(string(data))
			}
		}()
	}
	wait.Wait()
	close(results)
	lineage := ""
	refused := 0
	for result := range results {
		if strings.HasPrefix(result, "initialized:") {
			if lineage != "" {
				t.Fatal("multiple initializers succeeded")
			}
			lineage = strings.TrimPrefix(result, "initialized:")
		} else if result == "refused" {
			refused++
		} else {
			t.Fatal(result)
		}
	}
	if !validJournalLineage(lineage) || refused != workers-1 {
		t.Fatal("initialization was not exclusive")
	}
	journal, err := OpenJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
}
func TestJournalInitializationProcessHelper(t *testing.T) {
	path := os.Getenv("GARM_INITIALIZE_HELPER")
	if path == "" {
		return
	}
	journal, err := InitializeJournal(path)
	if err != nil {
		fmt.Print("refused")
		os.Exit(0)
	}
	lineage := journal.Lineage()
	if journal.Close() != nil {
		os.Exit(2)
	}
	fmt.Print("initialized:" + lineage)
	os.Exit(0)
}

func structPoolPriority(value uint64) garmapi.PoolUpdate { return garmapi.PoolUpdate{Priority: &value} }

func TestJournalInitializerRefusesOrphanedSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		path := filepath.Join(privateJournalDir(t), "journal.db")
		if err := os.WriteFile(path+suffix, []byte("forensic fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := InitializeJournal(path); !errors.Is(err, ErrJournal) {
			t.Fatal("orphaned sidecar adopted")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("initializer created main next to unknown history")
		}
	}
}
