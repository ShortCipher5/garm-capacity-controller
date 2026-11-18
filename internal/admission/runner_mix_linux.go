package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// Runner mixes are operator intent only. Counts neither reserve resources nor
// alter pool limits, immutable pool mappings, grants or physical accounting.
type RunnerMixEntry struct {
	Name  string `json:"name"`
	CPU   int64  `json:"cpu_millis"`
	RAM   int64  `json:"ram_mib"`
	Disk  int64  `json:"disk_mib"`
	Count int64  `json:"count"`
}
type RunnerMix struct {
	Host     string           `json:"host"`
	Revision int64            `json:"revision"`
	Entries  []RunnerMixEntry `json:"entries"`
}
type RunnerMixView struct {
	Sequence      int64     `json:"sequence"`
	Mix           RunnerMix `json:"mix"`
	ProfilesMatch bool      `json:"profiles_match"`
}

var ErrRunnerMixConflict = errors.New("runner mix or profile review changed; reload before applying")

const sqliteRunnerMixSchema = `CREATE TABLE IF NOT EXISTS runner_mixes(host TEXT PRIMARY KEY NOT NULL, revision INTEGER NOT NULL CHECK(revision>0), document BLOB NOT NULL, document_hash TEXT NOT NULL) STRICT; PRAGMA user_version=28;`

func ValidateRunnerMix(m RunnerMix) error {
	if !bindingName(m.Host) || m.Revision < 0 || m.Revision >= 9007199254740990 || len(m.Entries) == 0 || len(m.Entries) > 32 {
		return ErrDenied
	}
	names := map[string]bool{}
	var totals [4]int64
	for _, e := range m.Entries {
		if !profileName(e.Name) || names[e.Name] || e.CPU <= 0 || e.CPU%1000 != 0 || e.RAM <= 0 || e.Disk <= 0 || e.Count < 0 || e.Count > 1000000 {
			return ErrDenied
		}
		names[e.Name] = true
		for i, v := range []int64{e.CPU, e.RAM, e.Disk, 1} {
			if e.Count > 0 && v > (math.MaxInt64-totals[i])/e.Count {
				return ErrDenied
			}
			totals[i] += v * e.Count
		}
	}
	return nil
}
func sqliteRunnerMix(tx *sql.Tx, host string) (RunnerMix, error) {
	m := RunnerMix{Host: host, Entries: []RunnerMixEntry{}}
	var raw []byte
	var digest string
	var revisions int64
	if err := tx.QueryRow("SELECT count(*) FROM audit WHERE kind='advisory_runner_mix_applied' AND request_key=?", host).Scan(&revisions); err != nil {
		return m, err
	}
	err := tx.QueryRow("SELECT revision,document,document_hash FROM runner_mixes WHERE host=?", host).Scan(&m.Revision, &raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		if revisions != 0 {
			return RunnerMix{}, ErrDenied
		}
		return m, nil
	}
	if err != nil {
		return m, err
	}
	hash := sha256.Sum256(raw)
	if revisions != m.Revision || digest != hex.EncodeToString(hash[:]) {
		return RunnerMix{}, ErrDenied
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &m.Entries) != nil || ValidateRunnerMix(m) != nil {
		return RunnerMix{}, ErrDenied
	}
	return m, nil
}
func (s *SQLiteStore) ReadRunnerMix(ctx context.Context, host string) (RunnerMixView, error) {
	var out RunnerMixView
	if !bindingName(host) {
		return out, ErrDenied
	}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		out.Mix, err = sqliteRunnerMix(tx, host)
		if err != nil {
			return err
		}
		if err = tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&out.Sequence); err != nil {
			return err
		}
		out.ProfilesMatch = out.Mix.Revision > 0
		for _, e := range out.Mix.Entries {
			var cpu, ram, disk int64
			err = tx.QueryRow("SELECT cpu,ram,disk FROM profiles WHERE name=?", e.Name).Scan(&cpu, &ram, &disk)
			if errors.Is(err, sql.ErrNoRows) {
				out.ProfilesMatch = false
				continue
			}
			if err != nil {
				return err
			}
			if cpu != e.CPU || ram != e.RAM || disk != e.Disk {
				out.ProfilesMatch = false
			}
		}
		return nil
	})
	return out, err
}

// ApplyRunnerMix atomically applies all reviewed profile definitions and saves
// the desired counts. The audit-sequence CAS also detects intervening ordinary
// profile edits, including changes that restore the previous values. A lost
// response is reconciled by ReadRunnerMix, never automatically replayed.
func (s *SQLiteStore) ApplyRunnerMix(ctx context.Context, expectedSequence int64, m RunnerMix) error {
	if expectedSequence < 0 || ValidateRunnerMix(m) != nil {
		return ErrDenied
	}
	// Own caller-backed entries before validation and transactional use.
	raw, err := json.Marshal(m)
	if err != nil {
		return ErrDenied
	}
	var owned RunnerMix
	if json.Unmarshal(raw, &owned) != nil || ValidateRunnerMix(owned) != nil {
		return ErrDenied
	}
	m = owned
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var seq int64
		if err := tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&seq); err != nil {
			return err
		}
		if seq != expectedSequence {
			return ErrRunnerMixConflict
		}
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if _, ok := config.Hosts[m.Host]; !ok {
			return ErrDenied
		}
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM runner_mixes").Scan(&count); err != nil {
			return err
		}
		previous, err := sqliteRunnerMix(tx, m.Host)
		if err != nil {
			return err
		}
		if previous.Revision != m.Revision {
			return ErrRunnerMixConflict
		}
		if previous.Revision == 0 && count >= 1000 {
			return ErrDenied
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		for _, e := range m.Entries {
			if _, err = tx.Exec(`INSERT INTO profiles(name,cpu,ram,disk,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET cpu=excluded.cpu,ram=excluded.ram,disk=excluded.disk,updated_at=excluded.updated_at`, e.Name, e.CPU, e.RAM, e.Disk, now, now); err != nil {
				return err
			}
		}
		body, err := json.Marshal(m.Entries)
		if err != nil || len(body) > 16384 {
			return ErrDenied
		}
		hash := sha256.Sum256(body)
		if _, err = tx.Exec(`INSERT INTO runner_mixes(host,revision,document,document_hash) VALUES(?,?,?,?) ON CONFLICT(host) DO UPDATE SET revision=excluded.revision,document=excluded.document,document_hash=excluded.document_hash`, m.Host, m.Revision+1, body, hex.EncodeToString(hash[:])); err != nil {
			return err
		}
		return sqliteAudit(tx, "advisory_runner_mix_applied", m.Host, s.now())
	})
}

func (s *SQLiteStore) checkRunnerMixes(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var missing int
		if err := tx.QueryRow("SELECT count(*) FROM audit a LEFT JOIN runner_mixes m ON m.host=a.request_key WHERE a.kind='advisory_runner_mix_applied' AND m.host IS NULL").Scan(&missing); err != nil {
			return err
		}
		if missing != 0 {
			return ErrDenied
		}
		rows, err := tx.Query("SELECT host FROM runner_mixes ORDER BY host LIMIT 1001")
		if err != nil {
			return err
		}
		hosts := []string{}
		for rows.Next() {
			var host string
			if err = rows.Scan(&host); err != nil {
				rows.Close()
				return err
			}
			hosts = append(hosts, host)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(hosts) > 1000 {
			return ErrDenied
		}
		for _, host := range hosts {
			if _, err = sqliteRunnerMix(tx, host); err != nil {
				return err
			}
		}
		return nil
	})
}
