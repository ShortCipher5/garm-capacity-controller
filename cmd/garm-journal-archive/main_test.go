package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

func privateDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if os.Chmod(directory, 0700) != nil {
		t.Fatal("private fixture")
	}
	return directory
}
func TestArchiveCLIBackupInspectionRestoreNeverActivates(t *testing.T) {
	directory := privateDir(t)
	source := filepath.Join(directory, "journal.db")
	journal, err := garmmanagement.InitializeJournal(source)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	lineage := journal.Lineage()
	backup := filepath.Join(directory, "backup.db")
	var output, diagnostic bytes.Buffer
	if code := run(context.Background(), []string{"backup", "--source", source, "--destination", backup, "--lineage", lineage}, &output, &diagnostic); code != 0 {
		t.Fatalf("backup code%d %s", code, diagnostic.String())
	}
	var info garmmanagement.ArchiveInfo
	if json.Unmarshal(output.Bytes(), &info) != nil || info.WritesPermitted || info.JournalLineage != lineage {
		t.Fatal("bad archive result")
	}
	output.Reset()
	diagnostic.Reset()
	restored := filepath.Join(directory, "restore.db")
	if code := run(context.Background(), []string{"restore-inspection", "--source", backup, "--destination", restored, "--lineage", lineage}, &output, &diagnostic); code != 0 {
		t.Fatalf("restore code%d %s", code, diagnostic.String())
	}
	if _, err = garmmanagement.OpenJournal(restored, lineage); !errors.Is(err, garmmanagement.ErrJournal) {
		t.Fatal("CLI activated historical journal")
	}
	output.Reset()
	diagnostic.Reset()
	if code := run(context.Background(), []string{"inspect", "--source", restored, "--lineage", lineage}, &output, &diagnostic); code != 0 {
		t.Fatal(code)
	}
	if json.Unmarshal(output.Bytes(), &info) != nil || info.Operation != "restore_inspection" || info.WritesPermitted || !strings.Contains(diagnostic.String(), "startup refuses") {
		t.Fatal("inspection implied recovery")
	}
}
func TestArchiveCLIValidatesBeforeIOAndNeverAcceptsActivation(t *testing.T) {
	directory := privateDir(t)
	source := filepath.Join(directory, "missing.db")
	destination := filepath.Join(directory, "missing-output.db")
	lineage := strings.Repeat("a", 64)
	for _, args := range [][]string{{"restore", "--source", source, "--destination", destination, "--lineage", lineage}, {"backup", "--source", source, "--destination", destination}, {"backup", "--source", source, "--destination", destination, "--lineage", lineage, "--force"}, {"restore-inspection", "--source", source, "--destination", destination, "--lineage", lineage, "--old-writers-fenced"}, {"inspect", "--source", source, "--destination", destination}, {"backup", "--source", source, "--destination", destination, "--lineage", "secret-private-value"}} {
		var output, diagnostic bytes.Buffer
		if code := run(context.Background(), args, &output, &diagnostic); code != 2 {
			t.Fatalf("invalid command code%d", code)
		}
		if strings.Contains(diagnostic.String(), source) || strings.Contains(diagnostic.String(), "secret-private-value") {
			t.Fatal("arguments leaked into diagnostic")
		}
		for _, path := range []string{source, destination} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid command touched filesystem")
			}
		}
	}
}
func TestArchiveCLIHelpAndOutputFailureAreHonest(t *testing.T) {
	var output, diagnostic bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, &output, &diagnostic); code != 0 || !strings.Contains(output.String(), "no activate") {
		t.Fatal("unsafe help")
	}
	directory := privateDir(t)
	source := filepath.Join(directory, "journal.db")
	journal, err := garmmanagement.InitializeJournal(source)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	destination := filepath.Join(directory, "archive.db")
	output.Reset()
	diagnostic.Reset()
	if code := run(context.Background(), []string{"backup", "--source", source, "--destination", destination, "--lineage", journal.Lineage()}, failingWriter{}, &diagnostic); code != 3 {
		t.Fatal("output ambiguity ignored")
	}
	if _, err := garmmanagement.InspectArchive(context.Background(), destination); err != nil {
		t.Fatal("valid published evidence lost after stdout failure")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("fixture write failure") }
