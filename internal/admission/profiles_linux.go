package admission

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrProfile identifies an unknown runner-size profile or an ineligible host
// for profile-based estimation. Invalid arguments fail with ErrDenied instead.
var ErrProfile = errors.New("runner-size profile unavailable")

// Schema10 adds runner-size profiles and host usage-sample history, plus the
// disk dimension on reservation, inventory and cancellation identity columns.
// Disk defaults to zero everywhere: v9 rows stay valid and unconstrained. The
// disk columns are added conditionally because qualified downgrades roll
// user_version back without dropping newer columns.
const sqliteProfileUsageSchema = `
CREATE TABLE IF NOT EXISTS profiles (
 name TEXT PRIMARY KEY NOT NULL,
 cpu INTEGER NOT NULL CHECK(cpu>0),
 ram INTEGER NOT NULL CHECK(ram>0),
 disk INTEGER NOT NULL CHECK(disk>=0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS usage_samples (
 host TEXT NOT NULL,
 sampled_at TEXT NOT NULL,
 cpu_used_millis INTEGER NOT NULL,
 ram_used_mib INTEGER NOT NULL,
 disk_used_mib INTEGER NOT NULL,
 source TEXT NOT NULL,
 PRIMARY KEY(host, sampled_at)
) STRICT;
PRAGMA user_version=10;
`

// Stored timestamps use canonical UTC RFC3339Nano. Its terminal Z sorts after
// decimal digits and the decimal point, so e.g. .100000001Z sorts before .1Z
// as plain text despite representing a later instant. A lower terminator in
// the indexed key orders all canonical timestamps without rewriting the
// original values or changing the primary key's retry/deduplication contract.
const sqliteUsageTimeKey = "replace(sampled_at,'Z','!')"
const sqliteUsageHistoryIndex = `
CREATE INDEX IF NOT EXISTS usage_samples_chronological ON usage_samples(host, ` + sqliteUsageTimeKey + `);
PRAGMA user_version=29;
`
const sqliteUsageHistorySelect = "SELECT host,sampled_at,cpu_used_millis,ram_used_mib,disk_used_mib,source FROM usage_samples WHERE host=?"
const sqliteUsageHistorySince = " AND " + sqliteUsageTimeKey + ">=replace(?,'Z','!')"
const sqliteUsageHistoryRecentOrder = " ORDER BY " + sqliteUsageTimeKey + " DESC LIMIT ?"
const sqliteUsageHistoryOrder = " ORDER BY " + sqliteUsageTimeKey + " ASC LIMIT ?"

const sqliteDiskColumn = "disk INTEGER NOT NULL DEFAULT 0 CHECK(disk>=0)"

func sqliteColumnExists(tx *sql.Tx, table, column string) (bool, error) {
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func sqliteAddDiskColumn(tx *sql.Tx, table string) error {
	exists, err := sqliteColumnExists(tx, table, "disk")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + sqliteDiskColumn)
	return err
}

// Profile is one runner-size profile. CPU is whole vCPUs expressed as millis;
// a zero Disk means the profile does not constrain disk.
type Profile struct {
	Name      string    `json:"name"`
	CPUMillis int64     `json:"cpu_millis"`
	RAMMiB    int64     `json:"ram_mib"`
	DiskMiB   int64     `json:"disk_mib"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UsageSample is one measured host utilization observation. Absence of samples
// means monitoring data is unavailable; zeros are never fabricated.
type UsageSample struct {
	Host          string    `json:"host"`
	SampledAt     time.Time `json:"sampled_at"`
	CPUUsedMillis int64     `json:"cpu_used_millis"`
	RAMUsedMiB    int64     `json:"ram_used_mib"`
	DiskUsedMiB   int64     `json:"disk_used_mib"`
	Source        string    `json:"source"`
}

func profileName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// ProfileUpsert inserts or replaces one runner-size profile in a single BEGIN
// IMMEDIATE transaction. CPU must be a positive whole vCPU count, RAM positive
// MiB and disk non-negative MiB (zero means the profile ignores disk).
func (s *SQLiteStore) ProfileUpsert(ctx context.Context, name string, cpuMillis, ramMiB, diskMiB int64) error {
	if !profileName(name) || cpuMillis <= 0 || cpuMillis%1000 != 0 || ramMiB <= 0 || diskMiB < 0 {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`INSERT INTO profiles(name,cpu,ram,disk,created_at,updated_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(name) DO UPDATE SET cpu=excluded.cpu,ram=excluded.ram,disk=excluded.disk,updated_at=excluded.updated_at`, name, cpuMillis, ramMiB, diskMiB, now, now); err != nil {
			return err
		}
		return sqliteAudit(tx, "profile_upserted", name, s.now())
	})
}

// ProfileDelete removes one runner-size profile. Unknown names fail closed.
func (s *SQLiteStore) ProfileDelete(ctx context.Context, name string) error {
	if !profileName(name) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.Exec("DELETE FROM profiles WHERE name=?", name)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrProfile
		}
		return sqliteAudit(tx, "profile_deleted", name, s.now())
	})
}

// Profiles lists every runner-size profile ordered by name.
func (s *SQLiteStore) Profiles(ctx context.Context) ([]Profile, error) {
	var result []Profile
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT name,cpu,ram,disk,created_at,updated_at FROM profiles ORDER BY name")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var profile Profile
			var created, updated string
			if err := rows.Scan(&profile.Name, &profile.CPUMillis, &profile.RAMMiB, &profile.DiskMiB, &created, &updated); err != nil {
				return err
			}
			if profile.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
				return errors.New("invalid stored profile")
			}
			if profile.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
				return errors.New("invalid stored profile")
			}
			if !profileName(profile.Name) || profile.CPUMillis <= 0 || profile.CPUMillis%1000 != 0 || profile.RAMMiB <= 0 || profile.DiskMiB < 0 {
				return errors.New("invalid stored profile")
			}
			result = append(result, profile)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RecordUsageSample appends one measured host utilization observation in a
// single BEGIN IMMEDIATE transaction. Duplicate (host, sampled_at) rows are
// ignored so exact collector retries stay idempotent. Samples are monitoring
// history, not charge mutations, so they are not individually audited.
func (s *SQLiteStore) RecordUsageSample(ctx context.Context, host string, at time.Time, cpuUsedMillis, ramUsedMiB, diskUsedMiB int64, source string) error {
	if !validAdminText(host, 256) || at.IsZero() || cpuUsedMillis < 0 || ramUsedMiB < 0 || diskUsedMiB < 0 || !validAdminText(source, 256) {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT OR IGNORE INTO usage_samples(host,sampled_at,cpu_used_millis,ram_used_mib,disk_used_mib,source) VALUES(?,?,?,?,?,?)", host, at.UTC().Format(time.RFC3339Nano), cpuUsedMillis, ramUsedMiB, diskUsedMiB, source)
		return err
	})
}

// UsageHistory returns recorded samples for one host ordered by sampled_at
// ascending. A zero limit uses the default of 500; limits outside 1..1000 are
// rejected. A host without samples yields an empty result: monitoring absence
// means unavailable, never a fabricated zero-filled series.
func (s *SQLiteStore) UsageHistory(ctx context.Context, host string, since time.Time, limit int) ([]UsageSample, error) {
	return s.usageHistory(ctx, host, since, limit, false)
}

// RecentUsageHistory selects the latest bounded samples at or after since,
// then returns them in chronological order for display.
func (s *SQLiteStore) RecentUsageHistory(ctx context.Context, host string, since time.Time, limit int) ([]UsageSample, error) {
	return s.usageHistory(ctx, host, since, limit, true)
}

func (s *SQLiteStore) usageHistory(ctx context.Context, host string, since time.Time, limit int, recent bool) ([]UsageSample, error) {
	if !validAdminText(host, 256) {
		return nil, ErrDenied
	}
	if limit == 0 {
		limit = 500
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrDenied
	}
	result := []UsageSample{}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		query := sqliteUsageHistorySelect
		args := []any{host}
		if !since.IsZero() {
			query += sqliteUsageHistorySince
			args = append(args, since.UTC().Format(time.RFC3339Nano))
		}
		if recent {
			query += sqliteUsageHistoryRecentOrder
		} else {
			query += sqliteUsageHistoryOrder
		}
		args = append(args, limit)
		rows, err := tx.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sample UsageSample
			var at string
			if err := rows.Scan(&sample.Host, &at, &sample.CPUUsedMillis, &sample.RAMUsedMiB, &sample.DiskUsedMiB, &sample.Source); err != nil {
				return err
			}
			if sample.SampledAt, err = time.Parse(time.RFC3339Nano, at); err != nil || sample.SampledAt.IsZero() || sample.CPUUsedMillis < 0 || sample.RAMUsedMiB < 0 || sample.DiskUsedMiB < 0 || !validAdminText(sample.Host, 256) || !validAdminText(sample.Source, 256) {
				return errors.New("invalid stored usage sample")
			}
			result = append(result, sample)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if recent {
		// Only the bounded page is reversed; SQLite does the indexed selection.
		for lo, hi := 0, len(result)-1; lo < hi; lo, hi = lo+1, hi-1 {
			result[lo], result[hi] = result[hi], result[lo]
		}
	}
	return result, nil
}

// ProfileEstimate reports how many runners of the given profile fit the host's
// current remaining host budget. It uses the same dimension math as fits(): a
// zero budget constrains nothing and a zero profile disk ignores the disk
// dimension. Unknown profiles and unknown or disabled hosts fail with
// ErrProfile.
func (s *SQLiteStore) ProfileEstimate(ctx context.Context, host, profile string) (int64, error) {
	if !profileName(profile) {
		return 0, ErrDenied
	}
	var estimate int64
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		hostPolicy, ok := config.Hosts[host]
		if !ok || hostPolicy.Mode != "enabled" {
			return ErrProfile
		}
		var perRunner Profile
		var created, updated string
		if err := tx.QueryRow("SELECT name,cpu,ram,disk,created_at,updated_at FROM profiles WHERE name=?", profile).Scan(&perRunner.Name, &perRunner.CPUMillis, &perRunner.RAMMiB, &perRunner.DiskMiB, &created, &updated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrProfile
			}
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		released, err := sqliteReleasedKeys(tx)
		if err != nil {
			return err
		}
		allocated, err := sqliteAllocationsWithGrants(tx, config, reservations, inventory, released)
		if err != nil {
			return err
		}
		estimate = profileFitEstimate(allocated.Hosts[host], Resources{CPU: perRunner.CPUMillis, RAM: perRunner.RAMMiB, Disk: perRunner.DiskMiB, Runners: 1}, hostPolicy.Budget)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return estimate, nil
}

// profileFitEstimate mirrors fits(): CPU, RAM and runner counts use the legacy
// bounded math (a zero budget fits nothing), while a zero disk budget is
// unconstrained and a zero per-runner disk ignores the disk dimension.
func profileFitEstimate(used, perRunner, budget Resources) int64 {
	estimate := int64(1<<63 - 1)
	limit := func(used, per, budget int64, zeroBudgetUnconstrained bool) {
		if per <= 0 || estimate == 0 {
			return
		}
		if budget == 0 {
			if zeroBudgetUnconstrained {
				return
			}
			estimate = 0
			return
		}
		remaining := budget - used
		if remaining < 0 {
			estimate = 0
			return
		}
		if remaining/per < estimate {
			estimate = remaining / per
		}
	}
	limit(used.CPU, perRunner.CPU, budget.CPU, false)
	limit(used.RAM, perRunner.RAM, budget.RAM, false)
	limit(used.Disk, perRunner.Disk, budget.Disk, true)
	limit(used.Runners, perRunner.Runners, budget.Runners, false)
	return estimate
}
