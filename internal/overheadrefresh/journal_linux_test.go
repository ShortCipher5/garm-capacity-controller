package overheadrefresh

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func journalDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestCollectionJournalCrashLineageAndLock(t *testing.T) {
	p := journalDir(t)
	lineage := strings.Repeat("a", 64)
	j, ops, e := openOperationJournal(p, lineage)
	if e != nil || len(ops) != 0 {
		t.Fatal(e)
	}
	if _, _, e = openOperationJournal(p, lineage); e == nil {
		t.Fatal("concurrent writer admitted")
	}
	op := Operation{RequestID: strings.Repeat("b", 32), Recipe: "owner", Revision: strings.Repeat("c", 64), State: "pending", StartedAt: time.Now(), Results: []Result{}}
	if j.persist(op) != nil {
		t.Fatal("pending not durable")
	}
	j.close()
	if _, _, e = openOperationJournal(p, strings.Repeat("f", 64)); e == nil {
		t.Fatal("foreign ledger accepted")
	}
	j, ops, e = openOperationJournal(p, lineage)
	if e != nil || ops[op.RequestID].State != "pending" {
		t.Fatal(e, ops)
	}
	s := &Service{store: &admission.SQLiteStore{}, journal: j, operations: ops, recipes: map[string]Recipe{}, choices: []Choice{}}
	v, e := s.Snapshot()
	if e != nil || v.Running || v.Operations[0].State != "unknown" {
		t.Fatal(v, e)
	}
	op.State = "partial"
	op.Results = []Result{{BindingSequence: 3, ArtifactSequence: 9}}
	op.FinishedAt = time.Now()
	if j.persist(op) != nil {
		t.Fatal("partial not durable")
	}
	j.close()
	j, ops, e = openOperationJournal(p, lineage)
	if e != nil || ops[op.RequestID].Results[0].ArtifactSequence != 9 {
		t.Fatal(e, ops)
	}
	j.close()
	if e = os.WriteFile(filepath.Join(p, op.RequestID+".json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = openOperationJournal(p, lineage); e == nil {
		t.Fatal("corrupt receipt accepted")
	}
}
func TestCollectionJournalRejectsNonprivateAndMissingLineage(t *testing.T) {
	p := journalDir(t)
	if e := os.WriteFile(filepath.Join(p, strings.Repeat("a", 32)+".json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e := openOperationJournal(p, strings.Repeat("b", 64)); e == nil {
		t.Fatal("missing lineage silently initialized")
	}
}
func TestCollectionCapacityAndConflictingReplayFailBeforeWork(t *testing.T) {
	p := journalDir(t)
	j, ops, e := openOperationJournal(p, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	defer j.close()
	revision := strings.Repeat("b", 64)
	s := &Service{store: &admission.SQLiteStore{}, journal: j, operations: ops, recipes: map[string]Recipe{"owner": {ID: "owner"}}, choices: []Choice{{ID: "owner", Revision: revision}}}
	for i := 0; i < 10000; i++ {
		s.operations[fmt.Sprintf("%032x", i)] = Operation{}
	}
	if _, e = s.Start("owner", revision, strings.Repeat("f", 32)); e == nil {
		t.Fatal("history cap exceeded")
	}
	s.operations = map[string]Operation{strings.Repeat("f", 32): {Recipe: "other", Revision: revision}}
	if _, e = s.Start("owner", revision, strings.Repeat("f", 32)); e == nil {
		t.Fatal("conflicting old request reused")
	}
}
func TestInitialPersistenceFailureClosesCollector(t *testing.T) {
	j, ops, e := openOperationJournal(journalDir(t), strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	defer j.close()
	revision := strings.Repeat("b", 64)
	s := &Service{store: &admission.SQLiteStore{}, journal: j, operations: ops, recipes: map[string]Recipe{"owner": {ID: "owner"}}, choices: []Choice{{ID: "owner", Revision: revision}}}
	j.root.Close()
	if _, e = s.Start("owner", revision, strings.Repeat("c", 32)); e == nil {
		t.Fatal("closed journal accepted")
	}
	if !s.closed {
		t.Fatal("uncertain initial journal persistence did not close service")
	}
	if _, e = s.Snapshot(); e == nil {
		t.Fatal("uncertain journal advertised can_start")
	}
}
func TestCollectionJournalRejectsNonregularReceiptWithoutBlocking(t *testing.T) {
	p := journalDir(t)
	lineage := strings.Repeat("a", 64)
	j, _, e := openOperationJournal(p, lineage)
	if e != nil {
		t.Fatal(e)
	}
	j.close()
	if e = syscall.Mkfifo(filepath.Join(p, strings.Repeat("f", 32)+".json"), 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, _, e := openOperationJournal(p, lineage); done <- e }()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("FIFO receipt accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("nonregular receipt blocked journal read")
	}
}
