package admission

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"path/filepath"
	"strings"
	"testing"
)

func artifactObservation(t *testing.T, f nativeFixture, quota int64, root string) nativeartifacts.Observation {
	t.Helper()
	d := nativeartifacts.Descriptor{Version: nativeartifacts.Version, Owner: f.observation.Record(), ArtifactID: strings.Repeat("a", 61) + root, PoolGUID: "101", PoolLoadGUID: "102", RootGUID: root, Dataset: "pool/control-" + strings.Repeat("a", 61) + root, QuotaBytes: quota}
	f.setEvidence(nativeartifacts.Path, d)
	o, e := nativeartifacts.Observe(context.Background(), nativeartifacts.Configuration{Endpoint: f.evidenceConfig.Endpoint, TLS: f.evidenceConfig.TLS, Expected: d.Owner})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestNativeArtifactOverheadActualCanonicalAccounting(t *testing.T) {
	ctx := context.Background()
	f := nativeExecutionFixture(t)
	s := f.store
	if _, e := s.Admit(ctx, request("artifact-live")); e != nil {
		t.Fatal(e)
	}
	if v, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, nativeartifacts.Observation{}); e == nil || v.Sequence != 0 {
		t.Fatal("caller empty observation accepted")
	}
	o := artifactObservation(t, f, (1<<20)+1, "103")
	if _, e := s.db.Exec("CREATE TRIGGER artifact_fail BEFORE INSERT ON audit WHEN NEW.kind='native_artifact_overhead' BEGIN SELECT RAISE(ABORT,'fault');END"); e != nil {
		t.Fatal(e)
	}
	if v, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, o); e == nil || v.Sequence != 0 {
		t.Fatal("detached failed record", e)
	}
	if _, e := s.db.Exec("DROP TRIGGER artifact_fail"); e != nil {
		t.Fatal(e)
	}
	v, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, o)
	if e != nil || v.Resources != (Resources{Disk: 2}) {
		t.Fatal(v, e)
	}
	state, charges, e := s.ReadBrokerPlanningState(ctx)
	if e != nil || len(charges) != 1 || len(state.ArtifactOverheads) != 1 || state.Allocated.ArtifactOverhead != v.Resources || state.Allocated.Global.Disk != state.Allocated.Pools["app"].Disk+2 || state.Allocated.Global != state.Allocated.Hosts["a"] {
		t.Fatal("artifact accounting mismatch", state.Allocated, e)
	}
	old, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, artifactObservation(t, f, 1<<20, "103"))
	if e != nil || old.Sequence != v.Sequence {
		t.Fatal("lower ceiling released charge", e)
	}
	next, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, artifactObservation(t, f, 3<<20, "103"))
	if e != nil || next.Resources.Disk != 3 || next.Sequence <= v.Sequence {
		t.Fatal("larger ceiling", e)
	}
	other, e := s.RecordNativeArtifactOverhead(ctx, f.binding.Sequence, artifactObservation(t, f, 1<<20, "104"))
	if e != nil || other.Sequence <= next.Sequence {
		t.Fatal("new root not charged", e)
	}
	snap, e := s.Snapshot(ctx)
	if e != nil || snap.Allocated.ArtifactOverhead.Disk != 4 {
		t.Fatal("new root erased old debt", e)
	}
	backup := filepath.Join(sqliteBackupDir(t), "artifact.db")
	if e = s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restore := filepath.Join(sqliteBackupDir(t), "artifact-restored.db")
	if e = RestoreSQLite(ctx, backup, restore); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restore, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	restored, e := copy.Snapshot(ctx)
	if e != nil || restored.Ready || restored.Allocated.ArtifactOverhead.Disk != 4 || restored.Allocated.Global.Runners != 1 {
		t.Fatal("restore lost debt", e)
	}
	if _, e = copy.db.Exec("DROP TRIGGER native_artifact_overhead_permanent;DELETE FROM native_artifact_overheads"); e != nil {
		t.Fatal(e)
	}
	if e = copy.checkNativeArtifactOverheads(ctx); e == nil {
		t.Fatal("missing reverse audit accepted")
	}
	if _, e = s.LookupRelease(ctx, "artifact-live"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("artifact observation released guest", e)
	}
}
func TestNativeArtifactOverheadMigration26(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "artifact-migration")
	if _, e := s.db.Exec(nativeArtifactOverheadDowngradeSQL + "PRAGMA user_version=26"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	copy, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	state := sqliteSnapshot(t, copy)
	if state.Ready || state.Allocated.Global.Runners != 1 || state.Allocated.ArtifactOverhead != (Resources{}) {
		t.Fatal("migration changed debt")
	}
	if _, e = copy.db.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,disk,runners,state,created_at,accounting_version) VALUES('old26','app','a',1,1,0,1,'creating','2026-01-01T00:00:00Z',26)"); e == nil || !strings.Contains(e.Error(), "current artifact overhead accounting required") {
		t.Fatal("old26 bypassed artifact debt", e)
	}
}
