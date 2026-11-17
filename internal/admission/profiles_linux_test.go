package admission

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileUpsertValidatesAndListsOrdered(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	for name, change := range map[string]func() error{
		"empty name":    func() error { return s.ProfileUpsert(ctx, "", 2000, 2048, 0) },
		"uppercase":     func() error { return s.ProfileUpsert(ctx, "Large", 2000, 2048, 0) },
		"too long":      func() error { return s.ProfileUpsert(ctx, string(make([]byte, 65)), 2000, 2048, 0) },
		"special char":  func() error { return s.ProfileUpsert(ctx, "bad_name", 2000, 2048, 0) },
		"zero cpu":      func() error { return s.ProfileUpsert(ctx, "p1", 0, 2048, 0) },
		"fraction cpu":  func() error { return s.ProfileUpsert(ctx, "p1", 1500, 2048, 0) },
		"negative cpu":  func() error { return s.ProfileUpsert(ctx, "p1", -2000, 2048, 0) },
		"zero ram":      func() error { return s.ProfileUpsert(ctx, "p1", 2000, 0, 0) },
		"negative disk": func() error { return s.ProfileUpsert(ctx, "p1", 2000, 2048, -1) },
	} {
		if err := change(); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if err := s.ProfileUpsert(ctx, "large", 4000, 8192, 20480); err != nil {
		t.Fatal(err)
	}
	if err := s.ProfileUpsert(ctx, "small", 2000, 2048, 0); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Name != "large" || profiles[1].Name != "small" {
		t.Fatalf("profiles not ordered by name: %+v", profiles)
	}
	if profiles[0].CPUMillis != 4000 || profiles[0].RAMMiB != 8192 || profiles[0].DiskMiB != 20480 || profiles[1].DiskMiB != 0 {
		t.Fatalf("unexpected profile values: %+v", profiles)
	}
	if profiles[0].CreatedAt.IsZero() || profiles[0].UpdatedAt.IsZero() {
		t.Fatal("profile timestamps missing")
	}
	// Replace keeps created_at and refreshes updated_at.
	before := profiles[0]
	if err := s.ProfileUpsert(ctx, "large", 2000, 4096, 10240); err != nil {
		t.Fatal(err)
	}
	profiles, err = s.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].CPUMillis != 2000 || profiles[0].RAMMiB != 4096 || profiles[0].DiskMiB != 10240 {
		t.Fatalf("replace did not apply: %+v", profiles)
	}
	if !profiles[0].CreatedAt.Equal(before.CreatedAt) {
		t.Fatal("replace rewrote created_at")
	}
	state := sqliteSnapshot(t, s)
	var kinds []string
	for _, event := range state.Audit {
		if event.RequestKey == "large" {
			kinds = append(kinds, event.Kind)
		}
	}
	if len(kinds) != 2 || kinds[0] != "profile_upserted" || kinds[1] != "profile_upserted" {
		t.Fatalf("missing profile audit: %+v", kinds)
	}
}

func TestProfileDeleteFailsClosed(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	if err := s.ProfileDelete(ctx, "missing"); !errors.Is(err, ErrProfile) {
		t.Fatalf("unknown profile deleted: %v", err)
	}
	if err := s.ProfileDelete(ctx, "bad_name"); !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid name accepted: %v", err)
	}
	if err := s.ProfileUpsert(ctx, "temp", 2000, 2048, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ProfileDelete(ctx, "temp"); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profile survived delete: %+v %v", profiles, err)
	}
	if err := s.ProfileDelete(ctx, "temp"); !errors.Is(err, ErrProfile) {
		t.Fatalf("double delete accepted: %v", err)
	}
	if sqliteSnapshot(t, s).Audit[0].Kind != "profile_deleted" {
		t.Fatal("missing profile_deleted audit")
	}
}

func TestRecordUsageSampleValidatesAndIgnoresDuplicates(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Millisecond)
	for name, sample := range map[string]func() error{
		"empty host":    func() error { return s.RecordUsageSample(ctx, "", at, 1, 1, 1, "probe") },
		"zero time":     func() error { return s.RecordUsageSample(ctx, "a", time.Time{}, 1, 1, 1, "probe") },
		"negative cpu":  func() error { return s.RecordUsageSample(ctx, "a", at, -1, 1, 1, "probe") },
		"negative ram":  func() error { return s.RecordUsageSample(ctx, "a", at, 1, -1, 1, "probe") },
		"negative disk": func() error { return s.RecordUsageSample(ctx, "a", at, 1, 1, -1, "probe") },
		"empty source":  func() error { return s.RecordUsageSample(ctx, "a", at, 1, 1, 1, "") },
		"control host":  func() error { return s.RecordUsageSample(ctx, "a\x01b", at, 1, 1, 1, "probe") },
	} {
		if err := sample(); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if err := s.RecordUsageSample(ctx, "a", at, 500, 1024, 2048, "probe"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageSample(ctx, "a", at, 999, 9999, 99999, "probe"); err != nil {
		t.Fatal(err)
	}
	history, err := s.UsageHistory(ctx, "a", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].CPUUsedMillis != 500 || history[0].RAMUsedMiB != 1024 || history[0].DiskUsedMiB != 2048 || history[0].Source != "probe" || !history[0].SampledAt.Equal(at) {
		t.Fatalf("duplicate sample not ignored: %+v", history)
	}
	if err := s.RecordUsageSample(ctx, "a", at.Add(time.Second), 0, 0, 0, "probe"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageSample(ctx, "b", at, 1, 1, 1, "other-probe"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UsageHistory(ctx, "a", time.Time{}, 0); err != nil || len(got) != 2 {
		t.Fatalf("per-host history wrong: %+v %v", got, err)
	}
}

func TestUsageHistoryOrdersFiltersAndNeverFabricates(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 600; i++ {
		if err := s.RecordUsageSample(ctx, "a", base.Add(time.Duration(i)*time.Second), int64(i), int64(i), int64(i), "probe"); err != nil {
			t.Fatal(err)
		}
	}
	missing, err := s.UsageHistory(ctx, "never-observed", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if missing == nil || len(missing) != 0 {
		t.Fatalf("missing host fabricated samples: %+v", missing)
	}
	history, err := s.UsageHistory(ctx, "a", time.Time{}, 0)
	if err != nil || len(history) != 500 {
		t.Fatalf("default limit wrong: %d %v", len(history), err)
	}
	for i := 1; i < len(history); i++ {
		if !history[i-1].SampledAt.Before(history[i].SampledAt) {
			t.Fatal("history not ascending")
		}
	}
	history, err = s.UsageHistory(ctx, "a", time.Time{}, 1000)
	if err != nil || len(history) != 600 {
		t.Fatalf("max limit wrong: %d %v", len(history), err)
	}
	if history[599].CPUUsedMillis != 599 {
		t.Fatalf("newest sample missing: %+v", history[599])
	}
	history, err = s.UsageHistory(ctx, "a", base.Add(599*time.Second), 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("since filter wrong: %+v %v", history, err)
	}
	for _, limit := range []int{-1, 1001} {
		if _, err := s.UsageHistory(ctx, "a", time.Time{}, limit); !errors.Is(err, ErrDenied) {
			t.Fatalf("limit %d accepted: %v", limit, err)
		}
	}
	if _, err := s.UsageHistory(ctx, "bad\x02host", time.Time{}, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid host accepted: %v", err)
	}
}

func TestDiskBudgetConstrainsAdmissionOnlyWhenBudgeted(t *testing.T) {
	config := fixture(time.Now())
	a := config.Hosts["a"]
	a.Budget.Disk = 5120
	config.Hosts["a"] = a
	s := sqliteFixtureStore(t, config, true)
	ctx := context.Background()
	withDisk := request("disk-1")
	withDisk.Resources.Disk = 2048
	if _, err := s.Admit(ctx, withDisk); err != nil {
		t.Fatal(err)
	}
	second := request("disk-2")
	second.Resources.Disk = 3073
	if _, err := s.Admit(ctx, second); !errors.Is(err, ErrDenied) {
		t.Fatalf("disk budget bypassed: %v", err)
	}
	third := request("disk-3")
	third.Resources.Disk = 3072
	third.Host, third.Pool = "b", "api"
	if _, err := s.Admit(ctx, third); err != nil {
		t.Fatalf("unbudgeted disk dimension constrained: %v", err)
	}
	if got := sqliteSnapshot(t, s).Allocated.Hosts["a"].Disk; got != 2048 {
		t.Fatalf("disk allocation wrong: %d", got)
	}
}

func TestProfileEstimateUsesRemainingHostBudget(t *testing.T) {
	config := fixture(time.Now())
	a := config.Hosts["a"]
	a.Budget.Disk = 10240
	config.Hosts["a"] = a
	s := sqliteFixtureStore(t, config, true)
	ctx := context.Background()
	if err := s.ProfileUpsert(ctx, "large", 2000, 4096, 5120); err != nil {
		t.Fatal(err)
	}
	if err := s.ProfileUpsert(ctx, "nodisk", 1000, 2048, 0); err != nil {
		t.Fatal(err)
	}
	estimate, err := s.ProfileEstimate(ctx, "a", "large")
	if err != nil || estimate != 2 {
		t.Fatalf("estimate: %d %v", estimate, err)
	}
	// Disk-free profile ignores the disk budget dimension entirely.
	estimate, err = s.ProfileEstimate(ctx, "a", "nodisk")
	if err != nil || estimate != 4 {
		t.Fatalf("nodisk estimate: %d %v", estimate, err)
	}
	withDisk := request("charged")
	withDisk.Resources.Disk = 5120
	if _, err := s.Admit(ctx, withDisk); err != nil {
		t.Fatal(err)
	}
	if estimate, err = s.ProfileEstimate(ctx, "a", "large"); err != nil || estimate != 1 {
		t.Fatalf("estimate after charge: %d %v", estimate, err)
	}
	if estimate, err = s.ProfileEstimate(ctx, "a", "nodisk"); err != nil || estimate != 3 {
		t.Fatalf("nodisk estimate after charge: %d %v", estimate, err)
	}
	for name, call := range map[string]func() (int64, error){
		"unknown profile": func() (int64, error) { return s.ProfileEstimate(ctx, "a", "missing") },
		"unknown host":    func() (int64, error) { return s.ProfileEstimate(ctx, "missing", "large") },
		"invalid profile": func() (int64, error) { return s.ProfileEstimate(ctx, "a", "Bad_Name") },
	} {
		if _, err := call(); err == nil {
			t.Fatalf("%s succeeded", name)
		}
	}
	disabled := config
	h := disabled.Hosts["b"]
	h.Mode = "disabled"
	disabled.Hosts["b"] = h
	other := sqliteFixtureStore(t, disabled, true)
	if err := other.ProfileUpsert(ctx, "large", 2000, 4096, 5120); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ProfileEstimate(ctx, "b", "large"); !errors.Is(err, ErrProfile) {
		t.Fatalf("disabled host estimated: %v", err)
	}
	// Unbudgeted host dimensions constrain nothing, so the estimate is bounded
	// only by the runners dimension.
	unbudgeted := fixture(time.Now())
	for id, host := range unbudgeted.Hosts {
		host.Budget = Resources{}
		unbudgeted.Hosts[id] = host
	}
	free := sqliteFixtureStore(t, unbudgeted, true)
	if err := free.ProfileUpsert(ctx, "nodisk", 1000, 2048, 0); err != nil {
		t.Fatal(err)
	}
	if estimate, err := free.ProfileEstimate(ctx, "a", "nodisk"); err != nil || estimate != 0 {
		t.Fatalf("legacy zero budgets must fit nothing: %d %v", estimate, err)
	}
}

func TestHostCPUCapPolicyChange(t *testing.T) {
	s := sqliteFixtureStore(t, fixture(time.Now()), true)
	ctx := context.Background()
	change := func(c PolicyChange) error { return s.ApplyPolicyChange(ctx, c) }
	revision := PolicyRevision(sqliteSnapshot(t, s).Config)
	cap := int64(2000)
	if err := change(PolicyChange{Revision: revision, Action: "host_budget", Target: "a", Budget: &Resources{CPU: 4000, RAM: 8192, Runners: 4, Disk: 10240}, CPUCapMillis: &cap, Reason: "fair-use cap"}); err != nil {
		t.Fatal(err)
	}
	if got := sqliteSnapshot(t, s).Config.Hosts["a"].CPUCapMillis; got != 2000 {
		t.Fatalf("cap not persisted: %d", got)
	}
	for name, mutate := range map[string]func(*PolicyChange){
		"above budget": func(c *PolicyChange) { v := int64(4001); c.CPUCapMillis = &v },
		"negative":     func(c *PolicyChange) { v := int64(-1); c.CPUCapMillis = &v },
		"mode action": func(c *PolicyChange) {
			c.Action = "host_mode"
			c.Mode = "disabled"
			c.Budget = nil
			v := int64(1000)
			c.CPUCapMillis = &v
		},
		"global action": func(c *PolicyChange) {
			c.Action = "global_budget"
			c.Target = ""
			v := int64(1000)
			c.CPUCapMillis = &v
		},
	} {
		mutated := PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: "host_budget", Target: "a", Budget: &Resources{CPU: 4000, RAM: 8192, Runners: 4, Disk: 10240}, Reason: "retry"}
		mutate(&mutated)
		if err := change(mutated); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// Zero clears the cap; lowering the budget below an existing cap stays
	// allowed because the cap is display-only.
	zero := int64(0)
	if err := change(PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: "host_budget", Target: "a", Budget: &Resources{CPU: 4000, RAM: 8192, Runners: 4, Disk: 10240}, CPUCapMillis: &zero, Reason: "clear cap"}); err != nil {
		t.Fatal(err)
	}
	if got := sqliteSnapshot(t, s).Config.Hosts["a"].CPUCapMillis; got != 0 {
		t.Fatalf("cap not cleared: %d", got)
	}
	if err := change(PolicyChange{Revision: PolicyRevision(sqliteSnapshot(t, s).Config), Action: "host_budget", Target: "a", Budget: &Resources{CPU: 1000, RAM: 8192, Runners: 4}, Reason: "lower budget"}); err != nil {
		t.Fatalf("lowering budget below legacy cap rejected: %v", err)
	}
}

func TestSQLiteMigratesV9RowsPreservingChargesAndEnablingProfiles(t *testing.T) {
	config := fixture(time.Now())
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := OpenSQLite(path, config)
	if err != nil {
		t.Fatal(err)
	}
	reconcileSQLite(t, s, nil)
	if _, err := s.Admit(context.Background(), request("v9-reservation")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE profiles; DROP TABLE usage_samples;
ALTER TABLE reservations DROP COLUMN disk; ALTER TABLE inventory DROP COLUMN disk; ALTER TABLE cancellations DROP COLUMN disk;
PRAGMA user_version=9`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	upgraded, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatalf("v9 migration refused: %v", err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != sqliteCurrentVersion {
		t.Fatalf("migration version: %d %v", version, err)
	}
	for _, table := range []string{"reservations", "inventory", "cancellations"} {
		var columns int
		if err := upgraded.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='disk'", table).Scan(&columns); err != nil || columns != 1 {
			t.Fatalf("%s missing disk column: %d %v", table, columns, err)
		}
	}
	state := sqliteSnapshot(t, upgraded)
	if len(state.Reservations) != 1 || state.Allocated.Global != (Resources{CPU: 1000, RAM: 2048, Disk: 0, Runners: 1}) {
		t.Fatalf("v9 rows not preserved: %+v", state.Allocated.Global)
	}
	// v9 data keeps identical admission behavior: disk is unconstrained.
	reconcileSQLite(t, upgraded, nil)
	followUp := request("post-migration")
	followUp.Resources.Disk = 4096
	if _, err := upgraded.Admit(context.Background(), followUp); err != nil {
		t.Fatalf("disk constrained after migration: %v", err)
	}
	// New v10 features work on the migrated ledger.
	if err := upgraded.ProfileUpsert(context.Background(), "migrated", 2000, 2048, 10240); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.RecordUsageSample(context.Background(), "a", time.Now().UTC(), 100, 256, 512, "probe"); err != nil {
		t.Fatal(err)
	}
	if estimate, err := upgraded.ProfileEstimate(context.Background(), "a", "migrated"); err != nil || estimate != 1 {
		t.Fatalf("migrated estimate: %d %v", estimate, err)
	}
	// Fresh creation steps through the migration chain like every version
	// before it, so the first lifetime already carries one audit; the v9
	// upgrade of existing data is the second.
	var kinds []string
	for _, event := range sqliteSnapshot(t, upgraded).Audit {
		if event.Kind == "schema_migrated_v10" {
			kinds = append(kinds, event.Kind)
		}
	}
	if len(kinds) != 2 {
		t.Fatalf("migration audit events = %+v, want fresh-create plus upgrade", kinds)
	}
}
