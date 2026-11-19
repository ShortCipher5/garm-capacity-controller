package managedwriter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
)

// A stopped service cannot publish a replacement listener/session after failed
// explicit supervision. The empty boundary fails before Docker, credentials or
// namespace operations. This does not simulate successful authenticated Resume.
func TestFailedExplicitResumeRetainsReadOnlyAndJournal(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := garmmanagement.InitializeJournal(filepath.Join(directory, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	service := &Service{options: Options{ReadOnly: true}, journal: journal, stopped: true, terminal: make(chan error, 1)}
	defer service.Close()
	for attempt := 0; attempt < 2; attempt++ {
		err := service.Resume(context.Background())
		if err == nil {
			t.Fatal("resume without existing protected authority succeeded")
		}
		if !service.stopped || service.closed || service.server != nil || service.session != nil || service.done != nil {
			t.Fatal("failed resume published live state or closed explicit recovery path")
		}
		if !service.options.ReadOnly || service.journal != journal {
			t.Fatal("failed recovery replaced read-only policy or durable journal")
		}
		pending, err := journal.Pending(context.Background())
		if err != nil || len(pending) != 0 {
			t.Fatal("failed recovery wrote a mutation intent", err, pending)
		}
		select {
		case <-service.terminal:
			t.Fatal("failed explicit resume fabricated service termination")
		default:
		}
	}
}
