package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

func TestManagedWriterSchemaAndProtectedJournalFixture(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/managed-control-plane/testdata/writer.json")
	if err != nil {
		t.Fatal(err)
	}
	var c configuration
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		t.Fatal("invalid actual writer schema")
	}
	if !c.ReadOnly || c.ClientCertificateFile == "" || c.ClientLeaf == "" || c.Journal == "" || c.JournalLineage == "" || c.TLS.CA == "" || c.TLS.Certificate == "" || c.TLS.Key == "" {
		t.Fatal("incomplete fixture")
	}
	if len(c.Pools) != 0 || len(c.Repositories) != 0 || len(c.Providers) != 0 {
		t.Fatal("fixture scope not empty")
	}
	// Only temporary SQLite state is created. No keys, supervisor, Observe or run.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.db")
	if j, err := garmmanagement.OpenJournal(path, c.JournalLineage); err == nil {
		j.Close()
		t.Fatal("missing journal adopted")
	}
	j, err := garmmanagement.InitializeJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	lineage := j.Lineage()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"", strings.Repeat("0", 64)} {
		if j, err := garmmanagement.OpenJournal(path, wrong); err == nil {
			j.Close()
			t.Fatal("missing/wrong lineage accepted")
		}
	}
	j, err = garmmanagement.OpenJournal(path, lineage)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	pending, err := j.Pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatal("unexpected intents", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("journal not private")
	}
}
