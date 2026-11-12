package admission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrReconciliation = errors.New("complete bounded reconciliation required")
var ErrFence = errors.New("stale or inactive reconciliation fence")

// SQLiteStore is a local, single-controller-machine allocation ledger.
// It is an offline core, not a provider adapter or production enforcement mode.
type SQLiteStore struct {
	db *sql.DB
	poisoned atomic.Bool
	now func() time.Time
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS metadata (
 id INTEGER PRIMARY KEY CHECK(id=1), config BLOB NOT NULL,
 ready INTEGER NOT NULL CHECK(ready IN (0,1)),
 epoch INTEGER NOT NULL CHECK(typeof(epoch)='integer' AND epoch>=0),
 nonce TEXT NOT NULL, began_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS reservations (
 request_key TEXT PRIMARY KEY NOT NULL, pool TEXT NOT NULL, host TEXT NOT NULL,
 cpu INTEGER NOT NULL CHECK(cpu>0), ram INTEGER NOT NULL CHECK(ram>0),
 runners INTEGER NOT NULL CHECK(runners=1),
 state TEXT NOT NULL CHECK(state IN ('creating','idle','running','failed','unknown','deleting')),
 created_at TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS inventory (
 host TEXT NOT NULL, resource_id TEXT NOT NULL, pool TEXT NOT NULL,
 reservation_key TEXT UNIQUE REFERENCES reservations(request_key),
 cpu INTEGER NOT NULL CHECK(cpu>0), ram INTEGER NOT NULL CHECK(ram>0),
 runners INTEGER NOT NULL CHECK(runners=1),
 state TEXT NOT NULL CHECK(state IN ('creating','idle','running','failed','unknown','deleting')),
 PRIMARY KEY(host,resource_id)
) STRICT;
CREATE TABLE IF NOT EXISTS audit (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
 request_key TEXT NOT NULL, recorded_at TEXT NOT NULL
) STRICT;
PRAGMA user_version=1;
`

// OpenSQLite never imports or overwrites the original JSON ledger. Initial
// configuration applies only to an empty database. Inventory starts blocked.
func OpenSQLite(path string, initial Config) (*SQLiteStore, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, errors.New("a local database path is required")
	}
	f, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a private regular file")
	}
	location := url.URL{Scheme: "file", Path: absolute}
	query := url.Values{
		"mode": {"rw"}, "_busy_timeout": {"5000"}, "_foreign_keys": {"on"},
		"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_txlock": {"immediate"},
	}
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &SQLiteStore{db: db, now: time.Now}
	err = s.transaction(context.Background(), func(tx *sql.Tx) error {
		var version, syncMode, foreignKeys int
		var journal string
		for pragma, target := range map[string]any{
			"PRAGMA user_version": &version, "PRAGMA synchronous": &syncMode,
			"PRAGMA foreign_keys": &foreignKeys, "PRAGMA journal_mode": &journal,
		} {
			if err := tx.QueryRow(pragma).Scan(target); err != nil {
				return err
			}
		}
		if journal != "wal" || syncMode != 2 || foreignKeys != 1 {
			return errors.New("required SQLite durability settings unavailable")
		}
		if version != 0 && version != 1 {
			return errors.New("unsupported SQLite schema version")
		}
		if version == 0 {
			if err := validateConfig(initial); err != nil {
				return err
			}
			encoded, err := json.Marshal(initial)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(sqliteSchema); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO metadata(id,config,ready,epoch,nonce,began_at) VALUES(1,?,0,0,'','')", encoded); err != nil {
				return err
			}
		}
		_, _, _, _, err := sqliteMetadata(tx)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// Driver _txlock=immediate makes BeginTx acquire the write reservation before
// reading budgets. Separate database handles/processes cannot write-skew.
func (s *SQLiteStore) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.poisoned.Load() {
		return ErrUncertain
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			s.poisoned.Store(true)
		}
	}()
	if s.poisoned.Load() {
		return ErrUncertain
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		s.poisoned.Store(true)
		return fmt.Errorf("%w: SQLite commit outcome unavailable", ErrUncertain)
	}
	return nil
}

func sqliteMetadata(tx *sql.Tx) (Config, bool, int64, string, error) {
	var encoded []byte
	var ready bool
	var epoch int64
	var nonce string
	if err := tx.QueryRow("SELECT config,ready,epoch,nonce FROM metadata WHERE id=1").Scan(&encoded, &ready, &epoch, &nonce); err != nil {
		return Config{}, false, 0, "", err
	}
	var config Config
	if err := json.Unmarshal(encoded, &config); err != nil {
		return Config{}, false, 0, "", errors.New("invalid stored policy")
	}
	if err := validateConfig(config); err != nil {
		return Config{}, false, 0, "", err
	}
	return config, ready, epoch, nonce, nil
}

func sqliteAudit(tx *sql.Tx, kind, key string, now time.Time) error {
	_, err := tx.Exec("INSERT INTO audit(kind,request_key,recorded_at) VALUES(?,?,?)", kind, key, now.UTC().Format(time.RFC3339Nano))
	return err
}

func sqliteReservations(tx *sql.Tx) (map[string]Reservation, error) {
	rows, err := tx.Query("SELECT request_key,pool,host,cpu,ram,runners,state,created_at FROM reservations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]Reservation{}
	for rows.Next() {
		var r Reservation
		var created string
		if err := rows.Scan(&r.Request.Key, &r.Request.Pool, &r.Request.Host, &r.Request.Resources.CPU, &r.Request.Resources.RAM, &r.Request.Resources.Runners, &r.State, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil || !validRequest(r.Request) || !validState(r.State) {
			return nil, errors.New("invalid stored reservation")
		}
		result[r.Request.Key] = r
	}
	return result, rows.Err()
}

// InventoryResource is a bounded allocation, not observed utilization.
// ID must be a stable, project-qualified identity supplied by a trusted adapter.
// No such live adapter exists in this slice.
type InventoryResource struct {
	ID string `json:"id"`
	Host string `json:"host"`
	Pool string `json:"pool"`
	ReservationKey string `json:"reservation_key,omitempty"`
	Resources Resources `json:"resources"`
	State string `json:"state"`
}

func sqliteInventory(tx *sql.Tx) ([]InventoryResource, error) {
	rows, err := tx.Query("SELECT resource_id,host,pool,COALESCE(reservation_key,''),cpu,ram,runners,state FROM inventory ORDER BY host,resource_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []InventoryResource
	for rows.Next() {
		var resource InventoryResource
		if err := rows.Scan(&resource.ID, &resource.Host, &resource.Pool, &resource.ReservationKey, &resource.Resources.CPU, &resource.Resources.RAM, &resource.Resources.Runners, &resource.State); err != nil {
			return nil, err
		}
		if !validInventory(resource) {
			return nil, errors.New("invalid stored inventory")
		}
		result = append(result, resource)
	}
	return result, rows.Err()
}

type Allocations struct {
	Global Resources `json:"global"`
	Hosts map[string]Resources `json:"hosts"`
	Pools map[string]Resources `json:"pools"`
}

func sqliteAllocations(config Config, reservations map[string]Reservation, inventory []InventoryResource) (Allocations, error) {
	total := Allocations{Hosts: map[string]Resources{}, Pools: map[string]Resources{}}
	add := func(host, pool string, resources Resources) error {
		if _, ok := config.Hosts[host]; !ok {
			return errors.New("unknown allocation host")
		}
		if _, ok := config.Pools[pool]; !ok {
			return errors.New("unknown allocation pool")
		}
		h, p := total.Hosts[host], total.Pools[pool]
		if !accumulate(&total.Global, resources) || !accumulate(&h, resources) || !accumulate(&p, resources) {
			return errors.New("allocation accounting overflow")
		}
		total.Hosts[host], total.Pools[pool] = h, p
		return nil
	}
	for _, r := range reservations {
		if err := add(r.Request.Host, r.Request.Pool, r.Request.Resources); err != nil {
			return Allocations{}, err
		}
	}
	linked := map[string]bool{}
	for _, resource := range inventory {
		charge := resource.Resources
		if resource.ReservationKey != "" {
			r, ok := reservations[resource.ReservationKey]
			if !ok || linked[resource.ReservationKey] || r.Request.Host != resource.Host || r.Request.Pool != resource.Pool {
				return Allocations{}, errors.New("conflicting reservation binding")
			}
			linked[resource.ReservationKey] = true
			charge = Resources{
				CPU: max(0, resource.Resources.CPU-r.Request.Resources.CPU),
				RAM: max(0, resource.Resources.RAM-r.Request.Resources.RAM),
				Runners: 0,
			}
		}
		if err := add(resource.Host, resource.Pool, charge); err != nil {
			return Allocations{}, err
		}
	}
	return total, nil
}

// Admit persists a reservation and audit event in the same transaction.
// Replay is only a lookup, including when reconciliation currently blocks new
// admissions. It must never be interpreted as permission to repeat a create.
func (s *SQLiteStore) Admit(ctx context.Context, req Request) (Decision, error) {
	var result Decision
	var outcome error
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		deny := func(reason string, denied error) error {
			outcome = denied
			return sqliteAudit(tx, reason, "", s.now())
		}
		if !validRequest(req) {
			return deny("denied_invalid_request", ErrDenied)
		}
		if existing, ok := reservations[req.Key]; ok {
			if existing.Request != req {
				return deny("denied_identity_conflict", ErrConflict)
			}
			result = Decision{Simulation: true, Replay: true, Reservation: existing}
			return sqliteAudit(tx, "replayed", req.Key, s.now())
		}
		if !ready || epoch == int64(1<<63-1) {
			return deny("denied_reconciliation", ErrReconciliation)
		}
		// Global accounting depends on every host/source view remaining fresh.
		// Expiry consumes readiness until a new barrier and snapshot succeed.
		for _, observedHost := range config.Hosts {
			age := s.now().Sub(observedHost.ObservedAt)
			if age < 0 || age > config.MaxObservationAge {
				if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
					return err
				}
				return deny("denied_global_freshness", ErrReconciliation)
			}
		}
		host, hostOK := config.Hosts[req.Host]
		pool, poolOK := config.Pools[req.Pool]
		if !hostOK || !poolOK || !contains(pool.Hosts, req.Host) {
			return deny("denied_mapping", ErrDenied)
		}
		now := s.now()
		age := now.Sub(host.ObservedAt)
		if host.Mode != "enabled" || !host.Healthy || age < 0 || age > config.MaxObservationAge {
			return deny("denied_host_health", ErrDenied)
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		allocated, err := sqliteAllocations(config, reservations, inventory)
		if err != nil {
			return err
		}
		if !fits(allocated.Global, req.Resources, config.Global) || !fits(allocated.Hosts[req.Host], req.Resources, host.Budget) || !fits(allocated.Pools[req.Pool], req.Resources, pool.Budget) {
			return deny("denied_capacity", ErrDenied)
		}
		_, err = tx.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,runners,state,created_at) VALUES(?,?,?,?,?,?,'creating',?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Runners, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		result = Decision{Simulation: true, Reservation: Reservation{Request: req, State: "creating", CreatedAt: now}}
		return sqliteAudit(tx, "reserved", req.Key, now)
	})
	if err != nil {
		return Decision{}, err
	}
	return result, outcome
}

// RecordState never releases an allocation, regardless of state or age.
func (s *SQLiteStore) RecordState(ctx context.Context, key, state string) error {
	if !validState(state) {
		return errors.New("unsupported state; release is not implemented")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE reservations SET state=? WHERE request_key=?", state, key)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("unknown reservation")
		}
		return sqliteAudit(tx, "state_recorded", key, s.now())
	})
}

// Fence is a durable generation plus a random nonce to avoid epoch ABA reuse.
// It is not authentication and does not fence any external provider yet.
type Fence struct {
	Epoch int64
	Nonce string
}

// BeginReconciliation durably blocks ALL new admissions before collection.
// A crash, incomplete snapshot or invalid response leaves that barrier closed.
func (s *SQLiteStore) BeginReconciliation(ctx context.Context) (Fence, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return Fence{}, err
	}
	var result Fence
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		_, _, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if epoch == int64(1<<63-1) {
			return errors.New("reconciliation epoch exhausted")
		}
		result = Fence{Epoch: epoch + 1, Nonce: hex.EncodeToString(entropy[:])}
		if _, err := tx.Exec("UPDATE metadata SET ready=0,epoch=?,nonce=?,began_at=? WHERE id=1", result.Epoch, result.Nonce, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return sqliteAudit(tx, "reconciliation_started", "", s.now())
	})
	if err != nil {
		return Fence{}, err
	}
	return result, nil
}

// HostObservation is supplied by a trusted reconciler, not by a browser/job.
// Completeness assertions are an adapter contract, not independently verified
// proof. There is no live adapter or enforcement mode in this repository.
type HostObservation struct {
	Host string
	ObservedAt time.Time
	Healthy bool
	GarmComplete bool
	IncusComplete bool
	OperationsComplete bool
	Bounded bool
}

type InventorySnapshot struct {
	Hosts []HostObservation
	Resources []InventoryResource
}

func validInventory(r InventoryResource) bool {
	return r.ID != "" && len(r.ID) <= 256 && validRequest(Request{Key: r.ID, Pool: r.Pool, Host: r.Host, Resources: r.Resources}) && validState(r.State)
}

// CompleteReconciliation imports bounded resources conservatively. Missing
// previous resources become unknown but remain charged. There is no retirement.
// Identity/binding changes and duplicate links fail closed, requiring a future
// explicit identity-resolution workflow rather than freeing capacity implicitly.
func (s *SQLiteStore) CompleteReconciliation(ctx context.Context, fence Fence, snapshot InventorySnapshot) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, nonce, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if ready || epoch == 0 || fence.Epoch != epoch || fence.Nonce != nonce || nonce == "" {
			return ErrFence
		}
		var beganAt string
		if err := tx.QueryRow("SELECT began_at FROM metadata WHERE id=1").Scan(&beganAt); err != nil {
			return err
		}
		began, err := time.Parse(time.RFC3339Nano, beganAt)
		if err != nil {
			return ErrReconciliation
		}
		now := s.now()
		seenHosts := map[string]bool{}
		for _, observation := range snapshot.Hosts {
			host, exists := config.Hosts[observation.Host]
			age := now.Sub(observation.ObservedAt)
			if !exists || seenHosts[observation.Host] || !observation.GarmComplete || !observation.IncusComplete || !observation.OperationsComplete || !observation.Bounded || observation.ObservedAt.Before(began) || age < 0 || age > config.MaxObservationAge {
				return ErrReconciliation
			}
			seenHosts[observation.Host] = true
			host.Healthy, host.ObservedAt = observation.Healthy, observation.ObservedAt
			config.Hosts[observation.Host] = host
		}
		if len(seenHosts) != len(config.Hosts) {
			return ErrReconciliation
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		previous, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		existing := map[[2]string]InventoryResource{}
		for _, resource := range previous {
			existing[[2]string{resource.Host, resource.ID}] = resource
		}
		seen := map[[2]string]bool{}
		if _, err := tx.Exec("UPDATE inventory SET state='unknown'"); err != nil {
			return err
		}
		for _, resource := range snapshot.Resources {
			identity := [2]string{resource.Host, resource.ID}
			pool, poolOK := config.Pools[resource.Pool]
			if !validInventory(resource) || !seenHosts[resource.Host] || !poolOK || !contains(pool.Hosts, resource.Host) || seen[identity] {
				return ErrReconciliation
			}
			seen[identity] = true
			if old, ok := existing[identity]; ok && (old.Pool != resource.Pool || old.ReservationKey != resource.ReservationKey) {
				return ErrReconciliation
			}
			var link any
			if resource.ReservationKey != "" {
				r, ok := reservations[resource.ReservationKey]
				if !ok || r.Request.Host != resource.Host || r.Request.Pool != resource.Pool {
					return ErrReconciliation
				}
				link = resource.ReservationKey
			}
			_, err := tx.Exec(`INSERT INTO inventory(host,resource_id,pool,reservation_key,cpu,ram,runners,state) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(host,resource_id) DO UPDATE SET cpu=MAX(inventory.cpu,excluded.cpu),ram=MAX(inventory.ram,excluded.ram),state=excluded.state`, resource.Host, resource.ID, resource.Pool, link, resource.Resources.CPU, resource.Resources.RAM, resource.Resources.Runners, resource.State)
			if err != nil {
				return err
			}
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		if _, err := sqliteAllocations(config, reservations, inventory); err != nil {
			return err
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE metadata SET config=?,ready=1,nonce='' WHERE id=1", encoded); err != nil {
			return err
		}
		return sqliteAudit(tx, "reconciliation_completed", "", now)
	})
}

type AuditEvent struct {
	Sequence int64
	Kind string
	RequestKey string
	RecordedAt time.Time
}

type SQLiteSnapshot struct {
	Ready bool
	Epoch int64
	Config Config
	Reservations map[string]Reservation
	Inventory []InventoryResource
	Allocated Allocations
	Audit []AuditEvent
}

// Snapshot returns one transactionally consistent view and the last 100 events.
func (s *SQLiteStore) Snapshot(ctx context.Context) (SQLiteSnapshot, error) {
	var result SQLiteSnapshot
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		result.Config, result.Ready, result.Epoch, _, err = sqliteMetadata(tx)
		if err != nil {
			return err
		}
		result.Reservations, err = sqliteReservations(tx)
		if err != nil {
			return err
		}
		result.Inventory, err = sqliteInventory(tx)
		if err != nil {
			return err
		}
		result.Allocated, err = sqliteAllocations(result.Config, result.Reservations, result.Inventory)
		if err != nil {
			return err
		}
		rows, err := tx.Query("SELECT sequence,kind,request_key,recorded_at FROM audit ORDER BY sequence DESC LIMIT 100")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event AuditEvent
			var at string
			if err := rows.Scan(&event.Sequence, &event.Kind, &event.RequestKey, &at); err != nil {
				return err
			}
			event.RecordedAt, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return err
			}
			result.Audit = append(result.Audit, event)
		}
		return rows.Err()
	})
	if err != nil {
		return SQLiteSnapshot{}, err
	}
	return result, nil
}
