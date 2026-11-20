package cihealthsource

import (
	"errors"
	"os"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func serviceHistoryRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return path, root
}

func TestOwnedCycleHistoryValidatesBeforeOpeningAndDoesNothingWhenDisabled(t *testing.T) {
	source, _, _, scopes := historySourceCycle(t)
	path, root := serviceHistoryRoot(t)
	if got, cursors, err := OpenOwnedCycleHistory(nil, nil, OwnedCycleHistoryInput{Root: root, Initialize: true, Workflows: scopes}); got != nil || cursors != nil || err != nil {
		t.Fatalf("disabled stage acted: %v, %+v, %v", got, cursors, err)
	}
	foreign := scopes[0]
	foreign.RepositoryID += 100
	for _, bad := range [][]cihealth.HistoryScope{{foreign}, {scopes[0], scopes[0]}, nil} {
		if got, cursors, err := OpenOwnedCycleHistory(t.Context(), source, OwnedCycleHistoryInput{Enabled: true, Root: root, Initialize: true, Workflows: bad}); got != nil || cursors != nil || err != ErrCycleHistory {
			t.Fatalf("invalid scope opened storage: %v, %+v, %v", got, cursors, err)
		}
	}
	if rows, err := os.ReadDir(path); err != nil || len(rows) != 0 {
		t.Fatalf("disabled or refused startup touched held root: %v, %v", rows, err)
	}
}

func TestOwnedCycleHistoryScopesStartupRevisionsAndOneShotPersist(t *testing.T) {
	source, _, cycle, scopes := historySourceCycle(t)
	path, root := serviceHistoryRoot(t)
	ctx := t.Context()
	in := OwnedCycleHistoryInput{Enabled: true, Root: root, Initialize: true, Workflows: []cihealth.HistoryScope{scopes[2], scopes[0]}}
	svc, cursors, err := OpenOwnedCycleHistory(ctx, source, in)
	if err != nil || len(cursors) != 2 || cursors[0] != (HistoryExpectation{Scope: scopes[0]}) || cursors[1] != (HistoryExpectation{Scope: scopes[2]}) {
		t.Fatalf("wrong initial scoped cursors: %+v, %v", cursors, err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.PersistObservedCycle(ctx, cycle, cursors[:1]); err != ErrCycleHistory || len(got.Commits) != 0 {
		t.Fatalf("incomplete CAS plan advanced storage: %+v, %v", got, err)
	}
	got, err := svc.PersistObservedCycle(ctx, cycle, cursors)
	if err != nil || len(got.Commits) != 2 || got.Commits[0].Scope != scopes[0] || got.Commits[1].Scope != scopes[2] {
		t.Fatalf("explicit positive cycle was not committed: %+v, %v", got, err)
	}
	alias := *svc
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PersistObservedCycle(ctx, cycle, cursors); err != ErrCycleHistory {
		t.Fatalf("closed source resumed on old approval: %v", err)
	}
	root, err = os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	in.Root, in.Initialize = root, false
	reopened, expected, err := OpenOwnedCycleHistory(ctx, source, in)
	if err != nil || len(expected) != 2 || expected[0] != (HistoryExpectation{Scope: scopes[0], Cursor: cihealth.HistoryCursor{Revision: 1}}) ||
		expected[1] != (HistoryExpectation{Scope: scopes[2], Cursor: cihealth.HistoryCursor{Revision: 1}}) {
		t.Fatalf("restart forgot committed revisions: %+v, %v", expected, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	// A preexisting unresolved durable intent is not a reason to adopt,
	// reinitialize, delete or retry. Use an inert synthetic marker only.
	const intent = "ci-health-history-v1.intent"
	file, err := root.OpenFile(intent, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, init := range []bool{false, true} {
		in.Initialize = init
		if got, c, err := OpenOwnedCycleHistory(ctx, source, in); got != nil || c != nil || !errors.Is(err, cihealth.ErrHistoryUncertain) {
			t.Fatalf("unresolved history reopened or reinitialized: %v, %+v, %v", got, c, err)
		}
	}
	if _, err := root.Lstat(intent); err != nil {
		t.Fatalf("startup removed unresolved intent: %v", err)
	}
}
