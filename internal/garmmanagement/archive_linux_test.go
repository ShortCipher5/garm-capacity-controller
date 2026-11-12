package garmmanagement

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestArchiveIncludesCommittedWALAndPreservesUnknownWithoutActivation(t *testing.T) {
	manager, journal, remote, path := setup(t)
	ctx := context.Background()
	if _, err := journal.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdatePool(ctx, requestID, poolID, fingerprint, structPoolPriority(2), "confirmed fixture"); err != nil {
		t.Fatal(err)
	}
	remote.fail = errors.New("ambiguous fixture")
	if _, err := manager.UpdatePool(ctx, nextID, poolID, remote.pool.Fingerprint, structPoolPriority(3), "unknown fixture"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() <= 32 {
		t.Fatal("fixture did not retain committed WAL pages")
	}
	destination := filepath.Join(privateJournalDir(t), "backup.db")
	info, err := journal.Backup(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != archiveFormat || info.Operation != "backup" || info.Pending != 1 || info.Intents != 2 || info.AuditEvents != 4 || info.WritesPermitted || info.JournalLineage != journal.Lineage() || info.ManagementIdentity != remote.ManagementIdentity() || !validArchiveHash(info.ArchiveID) {
		t.Fatalf("archive metadata %+v", info)
	}
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJournal(destination, journal.Lineage()); !errors.Is(err, ErrJournal) {
		t.Fatal("historical backup became active journal")
	}
	after, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected archive was modified")
	}
	if archiveNoSidecars(destination) != nil {
		t.Fatal("backup requires sidecar")
	}
	inspected, err := InspectArchive(ctx, destination)
	if err != nil || inspected.ArchiveID != info.ArchiveID || inspected.Pending != 1 {
		t.Fatalf("inspection %+v %v", inspected, err)
	}
	restored := filepath.Join(privateJournalDir(t), "restore.db")
	recovery, err := RestoreInspection(ctx, destination, restored, journal.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Operation != "restore_inspection" || recovery.ParentArchiveID != info.ArchiveID || recovery.ArchiveID == info.ArchiveID || recovery.JournalLineage != journal.Lineage() || recovery.Intents != 2 || recovery.Pending != 1 || recovery.WritesPermitted {
		t.Fatalf("inspection restore %+v", recovery)
	}
	if _, err = OpenJournal(restored, journal.Lineage()); !errors.Is(err, ErrJournal) {
		t.Fatal("inspection restore allowed mutation")
	}
	again, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(before, again) {
		t.Fatal("restore modified source archive")
	}
	state, err := manager.Status(ctx)
	if err != nil || !state.WriteBlocked || remote.writes != 2 {
		t.Fatal("backup/restore altered remote/active journal state")
	}
}
func TestArchiveOfEmptyJournalNeverClaimsSafeRecovery(t *testing.T) {
	path := filepath.Join(privateJournalDir(t), "journal.db")
	journal, err := InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	archive := filepath.Join(privateJournalDir(t), "archive.db")
	info, err := BackupFile(context.Background(), path, archive, journal.Lineage())
	if err != nil {
		t.Fatal(err)
	}
	if info.Pending != 0 || info.Intents != 0 || info.WritesPermitted || info.JournalLineage != journal.Lineage() {
		t.Fatal("empty historical archive claimed mutable state")
	}
	if _, err = OpenJournal(archive, journal.Lineage()); !errors.Is(err, ErrJournal) {
		t.Fatal("zero pending treated as recovery proof")
	}
}
func TestArchiveWhileRemoteWritePendingIsConsistentAndSourceRemainsLive(t *testing.T) {
	manager, journal, remote, _ := setup(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	remote.hook = func() { close(started); <-release }
	result := make(chan error, 1)
	go func() {
		_, err := manager.UpdatePool(context.Background(), requestID, poolID, fingerprint, structPoolPriority(2), "pending fixture")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("remote fixture not entered")
	}
	destination := filepath.Join(privateJournalDir(t), "pending.db")
	info, err := journal.Backup(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Pending != 1 || info.Intents != 1 || info.AuditEvents != 1 {
		t.Fatalf("pending snapshot is torn %+v", info)
	}
	once.Do(func() { close(release) })
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source writer blocked")
	}
	status, err := manager.Status(context.Background())
	if err != nil || status.WriteBlocked {
		t.Fatal("backup mutated source completion")
	}
	inspected, err := InspectArchive(context.Background(), destination)
	if err != nil || inspected.Pending != 1 {
		t.Fatal("historical pending artifact changed with source")
	}
}
func TestArchiveRejectsOverwriteWrongLineageAndMissingSourceWithoutCreation(t *testing.T) {
	_, journal, _, source := setup(t)
	ctx := context.Background()
	dir := privateJournalDir(t)
	output := filepath.Join(dir, "output.db")
	if _, err := BackupFile(ctx, source, output, strings.Repeat("d", 64)); !errors.Is(err, ErrArchive) {
		t.Fatal("wrong source lineage copied")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrong-lineage artifact published")
	}
	missing := filepath.Join(dir, "missing.db")
	if _, err := BackupFile(ctx, missing, output, journal.Lineage()); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing source created")
	}
	if _, err := journal.Backup(ctx, output); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(output)
	if _, err := journal.Backup(ctx, output); !errors.Is(err, os.ErrExist) {
		t.Fatalf("destination overwritten: %v", err)
	}
	if _, err := RestoreInspection(ctx, output, output, journal.Lineage()); !errors.Is(err, os.ErrExist) {
		t.Fatal("restore overwrote source")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("existing destination changed")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		dest := filepath.Join(dir, "sidecar"+suffix+".db")
		if os.WriteFile(dest+suffix, []byte("forensic"), 0600) != nil {
			t.Fatal("sidecar fixture")
		}
		if _, err := journal.Backup(ctx, dest); err == nil {
			t.Fatal("existing sidecar ignored")
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("sidecar collision published")
		}
	}
}
func TestArchivePermissionSymlinkForeignAndCorruptionRefusal(t *testing.T) {
	_, journal, _, source := setup(t)
	ctx := context.Background()
	dir := privateJournalDir(t)
	public := filepath.Join(dir, "public.db")
	if os.WriteFile(public, nil, 0644) != nil {
		t.Fatal("fixture")
	}
	if _, err := BackupFile(ctx, public, filepath.Join(dir, "bad.db"), journal.Lineage()); err == nil {
		t.Fatal("public source accepted")
	}
	link := filepath.Join(dir, "link.db")
	if os.Symlink(source, link) != nil {
		t.Fatal("fixture")
	}
	if _, err := BackupFile(ctx, link, filepath.Join(dir, "bad.db"), journal.Lineage()); err == nil {
		t.Fatal("symlink source accepted")
	}
	publicDir := privateJournalDir(t)
	if os.Chmod(publicDir, 0755) != nil {
		t.Fatal("fixture")
	}
	if _, err := journal.Backup(ctx, filepath.Join(publicDir, "bad.db")); err == nil {
		t.Fatal("public destination parent accepted")
	}
	backup := filepath.Join(dir, "valid.db")
	if _, err := journal.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(backup+"-wal", []byte("unexpected"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := InspectArchive(ctx, backup); err == nil {
		t.Fatal("archive sidecar accepted")
	}
	_ = os.Remove(backup + "-wal")
	corrupt := filepath.Join(dir, "corrupt.db")
	if os.WriteFile(corrupt, []byte("not SQLite"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := InspectArchive(ctx, corrupt); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
	if _, err := InspectArchive(ctx, source); err == nil {
		t.Fatal("active journal masqueraded as archive")
	}
	if _, err := RestoreInspection(ctx, source, filepath.Join(dir, "bad.db"), journal.Lineage()); err == nil {
		t.Fatal("active journal accepted as restore artifact")
	}
}
func TestArchiveCancellationAndPublicationFailurePreserveSafety(t *testing.T) {
	_, journal, _, _ := setup(t)
	dir := privateJournalDir(t)
	destination := filepath.Join(dir, "canceled.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journal.Backup(ctx, destination); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled archive published")
	}
	temporary := filepath.Join(dir, ".temporary.db")
	if os.WriteFile(temporary, []byte("already fsynced fixture"), 0600) != nil {
		t.Fatal("fixture")
	}
	published := filepath.Join(dir, "published.db")
	err := archivePublish(temporary, published, func(string) error { return errors.New("injected directory sync failure") })
	if !errors.Is(err, ErrArchiveUncertain) {
		t.Fatalf("publication ambiguity lost %v", err)
	}
	if contents, err := os.ReadFile(published); err != nil || string(contents) != "already fsynced fixture" {
		t.Fatal("published evidence removed or altered")
	}
	if err = archivePublish(temporary, published, archiveSyncDirectory); !errors.Is(err, os.ErrExist) {
		t.Fatal("uncertain destination overwritten")
	}
}
