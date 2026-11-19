package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRunnerMixAtomicApplicationAndStaleReview(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	v, e := s.ReadRunnerMix(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	m := RunnerMix{Host: "a", Entries: []RunnerMixEntry{{Name: "small", CPU: 1000, RAM: 1024, Disk: 2048, Count: 2}, {Name: "large", CPU: 4000, RAM: 8192, Disk: 16384, Count: 1}}}
	if e = s.ApplyRunnerMix(ctx, v.Sequence, m); e != nil {
		t.Fatal(e)
	}
	got, e := s.ReadRunnerMix(ctx, "a")
	if e != nil || got.Mix.Revision != 1 || !got.ProfilesMatch || len(got.Mix.Entries) != 2 {
		t.Fatal(got, e)
	}
	if e = s.ApplyRunnerMix(ctx, v.Sequence, m); !errors.Is(e, ErrRunnerMixConflict) {
		t.Fatal("replayed", e)
	}
	if e = s.ProfileUpsert(ctx, "small", 2000, 1024, 2048); e != nil {
		t.Fatal(e)
	}
	m.Revision = 1
	m.Entries[1].RAM = 9000
	if e = s.ApplyRunnerMix(ctx, got.Sequence, m); !errors.Is(e, ErrRunnerMixConflict) {
		t.Fatal("stale", e)
	}
	after, e := s.ReadRunnerMix(ctx, "a")
	if e != nil || after.ProfilesMatch || after.Mix.Entries[1].RAM != 8192 {
		t.Fatal(after, e)
	}
	profiles, e := s.Profiles(ctx)
	if e != nil || profiles[0].RAMMiB != 8192 {
		t.Fatal(profiles, e)
	}
}
func TestRunnerMixValidation(t *testing.T) {
	for _, entries := range [][]RunnerMixEntry{nil, {{Name: "a", CPU: 1000, RAM: 1, Disk: 0, Count: 1}}, {{Name: "a", CPU: 1000, RAM: 1 << 62, Disk: 1, Count: 3}}, {{Name: "a", CPU: 1000, RAM: 1, Disk: 1, Count: 1}, {Name: "a", CPU: 1000, RAM: 1, Disk: 1, Count: 1}}} {
		if ValidateRunnerMix(RunnerMix{Host: "a", Entries: entries}) == nil {
			t.Fatal("invalid accepted", entries)
		}
	}
}
func TestRunnerMixConcurrentCASAndNoCapacityMutation(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	view, e := s.ReadRunnerMix(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.ReadCapacityStates(ctx)
	if e != nil {
		t.Fatal(e)
	}
	m := RunnerMix{Host: "a", Entries: []RunnerMixEntry{{Name: "small", CPU: 1000, RAM: 1024, Disk: 2048, Count: 2}}}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.ApplyRunnerMix(ctx, view.Sequence, m) }()
	}
	good, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			good++
		} else if errors.Is(e, ErrRunnerMixConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if good != 1 || conflicts != 1 {
		t.Fatal(good, conflicts)
	}
	after, e := s.ReadCapacityStates(ctx)
	if e != nil {
		t.Fatal(e)
	}
	before.ObservedAt = after.ObservedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata changed capacity", before, after)
	}
}
func TestRunnerMixBackupRestoreAndAtomicFailure(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	v, e := s.ReadRunnerMix(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	m := RunnerMix{Host: "a", Entries: []RunnerMixEntry{{Name: "small", CPU: 1000, RAM: 1024, Disk: 2048, Count: 2}}}
	if _, e = s.db.Exec("CREATE TRIGGER mix_fail BEFORE INSERT ON runner_mixes BEGIN SELECT RAISE(ABORT,'fault');END"); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyRunnerMix(ctx, v.Sequence, m); e == nil {
		t.Fatal("fault accepted")
	}
	p, e := s.Profiles(ctx)
	if e != nil || len(p) != 0 {
		t.Fatal("partial profile write", p, e)
	}
	if _, e = s.db.Exec("DROP TRIGGER mix_fail"); e != nil {
		t.Fatal(e)
	}
	if e = s.ApplyRunnerMix(ctx, v.Sequence, m); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(sqliteBackupDir(t), "backup.db")
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if e = s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	if e = RestoreSQLite(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restored, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	got, e := copy.ReadRunnerMix(ctx, "a")
	if e != nil || got.Mix.Revision != 1 || !got.ProfilesMatch {
		t.Fatal(got, e)
	}
	if _, e = copy.db.Exec("UPDATE runner_mixes SET document='garbage'"); e == nil {
		if e = copy.checkRunnerMixes(ctx); e == nil {
			t.Fatal("corruption accepted")
		}
	}
}
func TestRunnerMixMigration27PreservesUnknownCharges(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	if _, e := s.Admit(ctx, request("mix-migration")); e != nil {
		t.Fatal(e)
	}
	if e := s.RecordState(ctx, "mix-migration", "unknown"); e != nil {
		t.Fatal(e)
	}
	before, e := s.ReadCapacityStates(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("DROP TABLE runner_mixes; PRAGMA user_version=27"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	s.Close()
	copy, e := OpenSQLite(path, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	after, e := copy.ReadCapacityStates(ctx)
	if e != nil {
		t.Fatal(e)
	}
	before.ObservedAt = after.ObservedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed debt", before, after)
	}
	v, e := copy.ReadRunnerMix(ctx, "a")
	if e != nil || v.Mix.Revision != 0 || v.ProfilesMatch {
		t.Fatal(v, e)
	}
}
