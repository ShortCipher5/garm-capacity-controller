package cihealthreport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func fixturePublication(t *testing.T, attempt cihealth.AttemptKey, artifacts ...fixtureArtifact) Publication {
	t.Helper()
	manifest, archives := fixtureManifest(t, attempt, artifacts...)
	ingested, err := IngestReports(t.Context(), IngestInput{Scope: fixtureScope{attempt: true}, Manifest: manifest, Archives: archives})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := NewPublication(ingested)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestStoreSaveLoadIsIdempotentAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publications.json")
	store, err := OpenPublicationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pub := fixturePublication(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	saved, err := store.SavePublication(t.Context(), pub)
	if err != nil || !saved.Created {
		t.Fatalf("first save must create the record: %+v %v", saved, err)
	}
	again, err := store.SavePublication(t.Context(), pub)
	if err != nil || again.Created || again.Record.ReceivedAt != saved.Record.ReceivedAt {
		t.Fatalf("identical save must return the existing record: %+v %v", again, err)
	}
	loaded, found, err := store.LoadPublication(t.Context(), fixtureAttemptKey)
	if err != nil || !found || loaded.ReceivedAt != saved.Record.ReceivedAt || len(loaded.Reports) != 1 {
		t.Fatalf("load did not return the retained record: %+v %v", loaded, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenPublicationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, found, err = reopened.LoadPublication(t.Context(), fixtureAttemptKey)
	if err != nil || !found || len(loaded.Reports) != 1 || loaded.Scope != pub.Scope {
		t.Fatalf("durable reload lost the publication: %+v %v", loaded, err)
	}
}

func TestStoreRejectsConflictingClaimForSameAttempt(t *testing.T) {
	store, err := OpenPublicationStore(filepath.Join(t.TempDir(), "publications.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pub := fixturePublication(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), pub); err != nil {
		t.Fatal(err)
	}
	conflict := pub
	conflict.Reports[0].Totals.Passed++
	if _, err := store.SavePublication(t.Context(), conflict); !errors.Is(err, ErrReportStore) {
		t.Fatalf("conflicting claim must refuse: %v", err)
	}
}

func TestStoreRejectsCrossAttemptReuse(t *testing.T) {
	store, err := OpenPublicationStore(filepath.Join(t.TempDir(), "publications.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	attemptOne := fixtureAttempt("fixture_source", 10, 11, 12, 1)
	attemptTwo := fixtureAttempt("fixture_source", 10, 11, 12, 2)
	first := fixturePublication(t, attemptOne, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	reused := fixturePublication(t, attemptTwo, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), reused); !errors.Is(err, ErrReportStore) {
		t.Fatalf("cross-attempt reuse of proved report bytes must refuse: %v", err)
	}
	reusedMetadata := fixturePublication(t, attemptTwo, fixtureArtifact{id: 500})
	if _, err := store.SavePublication(t.Context(), reusedMetadata); !errors.Is(err, ErrReportStore) {
		t.Fatalf("cross-attempt reuse of a run-only artifact must refuse: %v", err)
	}
	independent := fixturePublication(t, attemptTwo, fixtureArtifact{id: 501, job: 9002, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), independent); err != nil {
		t.Fatalf("independent attempt evidence must save: %v", err)
	}
}

func TestStorePurgesBySharedRetentionWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publications.json")
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	store, err := openPublicationStore(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	old := fixturePublication(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(cihealth.HistoryRetentionWindow + time.Hour)
	fresh := fixturePublication(t, fixtureAttempt("fixture_source", 10, 11, 12, 2), fixtureArtifact{id: 501, job: 9002, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LoadPublication(t.Context(), fixtureAttemptKey); err != nil || found {
		t.Fatalf("expired publication must be purged: found=%v err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Hour)
	reopened, err := openPublicationStore(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, found, err := reopened.LoadPublication(t.Context(), fresh.Attempt); err != nil || !found {
		t.Fatalf("in-window publication must survive reopen: found=%v err=%v", found, err)
	}
}

func TestStoreRefusesForeignCorruptOrUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte(`{"format":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPublicationStore(corrupt); !errors.Is(err, ErrReportStore) {
		t.Fatalf("foreign file must refuse: %v", err)
	}
	garbled := filepath.Join(dir, "garbled.json")
	if err := os.WriteFile(garbled, []byte("not json at all"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPublicationStore(garbled); !errors.Is(err, ErrReportStore) {
		t.Fatalf("garbled file must refuse: %v", err)
	}
	worldReadable := filepath.Join(dir, "readable.json")
	if err := os.WriteFile(worldReadable, []byte(`{"format":"garm-test-report-publications-v1","publications":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldReadable, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPublicationStore(worldReadable); !errors.Is(err, ErrReportStore) {
		t.Fatalf("non-private file must refuse: %v", err)
	}
	if _, err := OpenPublicationStore(dir); !errors.Is(err, ErrReportStore) {
		t.Fatalf("directory must refuse: %v", err)
	}
}

func TestReadPublicationSnapshotStopsAtBoundPlusOne(t *testing.T) {
	for _, size := range []int{publicationStoreMaxBytes, publicationStoreMaxBytes + 16} {
		reader := strings.NewReader(strings.Repeat(" ", size))
		raw, err := readBoundedPublicationSnapshot(reader)
		if size == publicationStoreMaxBytes {
			if err != nil || len(raw) != size || reader.Len() != 0 {
				t.Fatalf("snapshot at byte ceiling refused: length=%d unread=%d err=%v", len(raw), reader.Len(), err)
			}
		} else if !errors.Is(err, ErrReportStore) || raw != nil || reader.Len() != 15 {
			t.Fatalf("overrun read past the sentinel byte or returned data: length=%d unread=%d err=%v", len(raw), reader.Len(), err)
		}
	}
}

func TestStoreRefusesOversizedSnapshotWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publications.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, publicationStoreMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenPublicationStore(path); store != nil || !errors.Is(err, ErrReportStore) {
		t.Fatalf("oversized snapshot opened: store=%v err=%v", store, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != publicationStoreMaxBytes+1 {
		t.Fatalf("oversized snapshot changed: info=%v err=%v", info, err)
	}
}

func TestStoreRejectsInvalidPublicationsAndClosedUse(t *testing.T) {
	store, err := OpenPublicationStore(filepath.Join(t.TempDir(), "publications.json"))
	if err != nil {
		t.Fatal(err)
	}
	pub := fixturePublication(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	badScope := pub
	badScope.Scope = cihealth.HistoryScope{Source: "fixture_source", RepositoryID: 10, WorkflowID: 12}
	if _, err := store.SavePublication(t.Context(), badScope); !errors.Is(err, ErrReportStore) {
		t.Fatalf("scope/attempt mismatch must refuse: %v", err)
	}
	badRow := pub
	badRow.Reports[0].EntrySHA256 = [32]byte{}
	if _, err := store.SavePublication(t.Context(), badRow); !errors.Is(err, ErrReportStore) {
		t.Fatalf("unpinned report must refuse: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePublication(t.Context(), pub); !errors.Is(err, ErrReportStore) {
		t.Fatalf("closed store save must refuse: %v", err)
	}
	if _, _, err := store.LoadPublication(t.Context(), fixtureAttemptKey); !errors.Is(err, ErrReportStore) {
		t.Fatalf("closed store load must refuse: %v", err)
	}
}

func TestStoreBoundsRetainedScopes(t *testing.T) {
	store, err := OpenPublicationStore(filepath.Join(t.TempDir(), "publications.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < publicationStoreMaxScopes; i++ {
		attempt := fixtureAttempt("fixture_source", int64(100+i), 11, 12, 1)
		pub := fixturePublication(t, attempt, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
		if _, err := store.SavePublication(t.Context(), pub); err != nil {
			t.Fatalf("scope %d must save: %v", i, err)
		}
	}
	overflow := fixturePublication(t, fixtureAttempt("fixture_source", 9999, 11, 12, 1), fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	if _, err := store.SavePublication(t.Context(), overflow); !errors.Is(err, ErrReportStore) {
		t.Fatalf("scope ceiling must refuse: %v", err)
	}
}

func TestStoreEnforcesRetentionAtReadTimeOnIdleStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publications.json")
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	store, err := openPublicationStore(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	pub := fixturePublication(t, fixtureAttemptKey, fixtureArtifact{id: 500, job: 9001, shard: "unit"})
	saved, err := store.SavePublication(t.Context(), pub)
	if err != nil {
		t.Fatal(err)
	}
	// One second before the cutoff the record is still readable.
	clock = saved.Record.ReceivedAt.Add(cihealth.HistoryRetentionWindow - time.Second)
	if _, found, err := store.LoadPublication(t.Context(), fixtureAttemptKey); err != nil || !found {
		t.Fatalf("in-window record must stay readable on an idle store: found=%v err=%v", found, err)
	}
	// Exactly at the cutoff it is excluded, with no save or restart in between.
	clock = saved.Record.ReceivedAt.Add(cihealth.HistoryRetentionWindow)
	if _, found, err := store.LoadPublication(t.Context(), fixtureAttemptKey); err != nil || found {
		t.Fatalf("record at the cutoff must be excluded at read time: found=%v err=%v", found, err)
	}
	// Scoped bulk reads enforce the same cutoff.
	scope := cihealth.HistoryScope{Source: "fixture_source", RepositoryID: 10, WorkflowID: 11}
	pubs, _, err := store.LoadScopePublications(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(pubs) != 0 {
		t.Fatalf("expired record leaked through scoped read: %+v", pubs)
	}
	clock = saved.Record.ReceivedAt.Add(cihealth.HistoryRetentionWindow - time.Second)
	pubs, _, err = store.LoadScopePublications(t.Context(), scope)
	if err != nil || len(pubs) != 1 {
		t.Fatalf("in-window scoped read must return the record: %+v %v", pubs, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadScopePublications(t.Context(), scope); err == nil {
		t.Fatal("closed store scoped read must refuse")
	}
}
