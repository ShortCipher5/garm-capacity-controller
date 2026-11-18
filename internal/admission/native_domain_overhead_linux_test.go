package admission

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func overheadObservation(t *testing.T, f nativeFixture, cpu, ram, disk uint64) nativeenforcement.Observation {
	t.Helper()
	d := nativeenforcement.Descriptor{Version: nativeenforcement.Version, Owner: f.observation.Record(), CapsuleBirthID: f.binding.Spec.DomainID, RootImageSHA256: strings.Repeat("1", 64), DeviceProgramSHA256: strings.Repeat("2", 64), EgressPolicySHA256: strings.Repeat("3", 64), Limits: nativeenforcement.Limits{CPUMilli: cpu, MemoryBytes: ram, DiskBytes: disk, Tasks: 128}}
	var e error
	d.LimitsDigest, e = nativeenforcement.DigestLimits(d.Limits)
	if e != nil {
		t.Fatal(e)
	}
	f.setEvidence(nativeenforcement.Path, d)
	o, e := nativeenforcement.Observe(context.Background(), nativeenforcement.Configuration{Endpoint: f.evidenceConfig.Endpoint, TLS: f.evidenceConfig.TLS, Expected: d.Owner})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func TestNativeDomainOverheadCanonicalMonotonicAccounting(t *testing.T) {
	ctx := context.Background()
	f := nativeExecutionFixture(t)
	s := f.store
	if _, e := s.Admit(ctx, request("before-overhead")); e != nil {
		t.Fatal(e)
	}
	o := overheadObservation(t, f, 3500, (1<<20)+1, (1<<20)+1)
	if _, e := s.db.Exec("CREATE TRIGGER test_overhead_audit BEFORE INSERT ON audit WHEN NEW.kind='native_domain_overhead' BEGIN SELECT RAISE(ABORT,'fault'); END"); e != nil {
		t.Fatal(e)
	}
	if v, e := s.RecordNativeDomainOverhead(ctx, f.binding.Sequence, o); e == nil || v.Sequence != 0 {
		t.Fatal("rolled-back overhead escaped", e)
	}
	if _, e := s.db.Exec("DROP TRIGGER test_overhead_audit"); e != nil {
		t.Fatal(e)
	}
	v, e := s.RecordNativeDomainOverhead(ctx, f.binding.Sequence, o)
	if e != nil {
		t.Fatal(e)
	}
	if v.Resources != (Resources{CPU: 3500, RAM: 2, Disk: 2}) {
		t.Fatal("ceil or runner count", v.Resources)
	}
	for _, mutate := range []func(*NativeDomainOverhead){
		func(x *NativeDomainOverhead) { x.DomainBindingSequence = x.Sequence },
		func(x *NativeDomainOverhead) { x.Evidence.Owner.PolicyDigest = "" },
		func(x *NativeDomainOverhead) { x.Evidence.Owner.NativeServerKey = "" },
		func(x *NativeDomainOverhead) { x.Evidence.Owner.OwnerClientLeaf = x.Evidence.Owner.OwnerServerLeaf },
		func(x *NativeDomainOverhead) { x.Evidence.Owner.NativeScope = "" },
	} {
		invalid := v
		mutate(&invalid)
		if ValidateNativeDomainOverhead(invalid) == nil {
			t.Fatal("invalid detached owner projection accepted")
		}
	}
	if _, e = s.Admit(ctx, request("after-overhead")); e == nil {
		t.Fatal("overhead ignored by admission")
	}
	state, charges, e := s.ReadBrokerPlanningState(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(charges) != 1 || len(state.DomainOverheads) != 1 || state.Allocated.DomainOverhead != v.Resources || state.Allocated.Global.CPU != 4500 || state.Allocated.Hosts["a"].CPU != 4500 || state.Allocated.Pools["app"].CPU != 1000 {
		t.Fatal("global/host/pool accounting", state.Allocated, charges)
	}
	lower := overheadObservation(t, f, 1000, 1<<20, 1<<20)
	old, e := s.RecordNativeDomainOverhead(ctx, f.binding.Sequence, lower)
	if e != nil || old.Sequence != v.Sequence {
		t.Fatal("decrease removed durable ceiling", e)
	}
	larger := overheadObservation(t, f, 3600, 3<<20, 1<<20)
	next, e := s.RecordNativeDomainOverhead(ctx, f.binding.Sequence, larger)
	if e != nil || next.Resources != (Resources{CPU: 3600, RAM: 3, Disk: 2}) || next.Sequence <= v.Sequence {
		t.Fatal("monotonic enlargement", next, e)
	}
	overflow := overheadObservation(t, f, math.MaxInt64, 3<<20, 1<<20)
	if failed, err := s.RecordNativeDomainOverhead(ctx, f.binding.Sequence, overflow); err == nil || failed.Sequence != 0 {
		t.Fatal("overflow committed or detached record returned", failed, err)
	}
	safe, err := s.ReadBrokerGrantState(ctx)
	if err != nil || safe.Allocated.DomainOverhead != next.Resources {
		t.Fatal("overflow poisoned canonical accounting", err)
	}
	backup := filepath.Join(sqliteBackupDir(t), "overhead.db")
	if e = s.BackupSQLite(ctx, backup); e != nil {
		t.Fatal(e)
	}
	restore := filepath.Join(sqliteBackupDir(t), "overhead-restore.db")
	if e = RestoreSQLite(ctx, backup, restore); e != nil {
		t.Fatal(e)
	}
	copy, e := OpenSQLite(restore, Config{})
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	snap, e := copy.Snapshot(ctx)
	if e != nil || snap.Ready || snap.Allocated.DomainOverhead != next.Resources || snap.Allocated.Global.Runners != 1 {
		t.Fatal("restore lost charge", snap, e)
	}
	if _, e = copy.db.Exec("DROP TRIGGER native_domain_overhead_permanent;DELETE FROM native_domain_overheads"); e != nil {
		t.Fatal(e)
	}
	if e = copy.checkNativeDomainOverheads(ctx); e == nil {
		t.Fatal("missing audited overhead accepted")
	}
	if _, e = s.LookupRelease(ctx, "before-overhead"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("overhead released guest", e)
	}
}
func TestNativeDomainOverheadMigration25(t *testing.T) {
	s := placementFixture(t)
	stockPlan(t, s, "overhead-migrate")
	if _, e := s.db.Exec(nativeDomainOverheadDowngradeSQL + "PRAGMA user_version=25"); e != nil {
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
	if state.Ready || state.Allocated.Global.Runners != 1 || state.Allocated.DomainOverhead != (Resources{}) {
		t.Fatal("migration altered charge")
	}
}

func TestNativeDomainOverheadRejectsPre26Writer(t *testing.T) {
	s := placementFixture(t)
	old, e := sql.Open("sqlite3", "file:"+s.path+"?_foreign_keys=true")
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if _, e = old.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,disk,runners,state,created_at,accounting_version) VALUES('old25','app','a',1,1,0,1,'creating','2026-01-01T00:00:00Z',18)"); e == nil || !strings.Contains(e.Error(), "current artifact overhead accounting required") {
		t.Fatal("pre26 accounting bypassed overhead", e)
	}
	if _, e = s.db.Exec("PRAGMA user_version=25"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Admit(context.Background(), request("rolled-back-version")); e == nil {
		t.Fatal("stale schema version accepted")
	}
	if _, e = s.db.Exec("PRAGMA user_version=27"); e != nil {
		t.Fatal(e)
	}
}
