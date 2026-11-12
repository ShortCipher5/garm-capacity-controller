package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

var ErrExecutionPlan = errors.New("immutable admitted execution plan required")

// The execution plan is permanent, including after a never-dispatched cancellation.
const sqlitePlacementSchema = `
CREATE TABLE execution_plans (
 request_key TEXT PRIMARY KEY NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 host_binding_sequence INTEGER NOT NULL REFERENCES audit(sequence),
 created_at TEXT NOT NULL
) STRICT;
CREATE TRIGGER execution_plans_immutable_update BEFORE UPDATE ON execution_plans
BEGIN SELECT RAISE(ABORT,'execution plans are immutable'); END;
CREATE TRIGGER execution_plans_immutable_delete BEFORE DELETE ON execution_plans
BEGIN SELECT RAISE(ABORT,'execution plans are immutable'); END;
PRAGMA user_version=7;
`

// ExecutionImage contains only trusted, nonsecret immutable execution settings.
// Bootstrap tokens, cloud-init, user data and credentials must NEVER be added.
type ExecutionImage struct {
	Controller  string   `json:"controller"`
	Name        string   `json:"name"`
	Flavor      string   `json:"flavor"`
	Image       string   `json:"image"`
	Fingerprint string   `json:"fingerprint"`
	Profiles    []string `json:"profiles"`
	Type        string   `json:"type"`
	Arch        string   `json:"arch"`
}

type PlacementRequest struct {
	Key       string
	Pool      string
	Resources Resources
	Execution ExecutionImage
	Hosts     []string
	Strategy  string // first_fit or least_runners, not a fairness guarantee
}

type ExecutionPlan struct {
	Request             Request        `json:"request"`
	Execution           ExecutionImage `json:"execution"`
	HostBindingSequence int64          `json:"host_binding_sequence"`
	CreatedAt           time.Time      `json:"created_at"`
	Replay              bool           `json:"-"`
}

func executionKey(image ExecutionImage, pool string) string {
	sum := sha256.Sum256([]byte(image.Controller + "\x00" + pool + "\x00" + image.Name))
	return hex.EncodeToString(sum[:])
}
func validExecution(image ExecutionImage, req Request) bool {
	if image.Flavor == "" || len(image.Flavor) > 256 || image.Image == "" || len(image.Image) > 256 || len(image.Profiles) > 16 || (image.Arch != "amd64" && image.Arch != "arm64") || executionKey(image, req.Pool) != req.Key {
		return false
	}
	return incusapi.ValidateSpec(incusapi.InstanceSpec{Name: image.Name, Type: image.Type, Profiles: image.Profiles, ImageFingerprint: image.Fingerprint, CPUMillis: req.Resources.CPU, RAMMiB: req.Resources.RAM, UserData: "validation", ControllerID: image.Controller, PoolID: req.Pool, ReservationKey: req.Key}) == nil
}
func executionPlanRecord(tx *sql.Tx, key string) (ExecutionPlan, error) {
	var raw, hash, at string
	var seq int64
	if err := tx.QueryRow("SELECT payload,payload_hash,host_binding_sequence,created_at FROM execution_plans WHERE request_key=?", key).Scan(&raw, &hash, &seq, &at); err != nil {
		return ExecutionPlan{}, err
	}
	sum := sha256.Sum256([]byte(raw))
	if len(raw) > 16384 || hex.EncodeToString(sum[:]) != hash {
		return ExecutionPlan{}, ErrExecutionPlan
	}
	var p ExecutionPlan
	if json.Unmarshal([]byte(raw), &p) != nil || p.Request.Key != key || !validRequest(p.Request) || !validExecution(p.Execution, p.Request) || p.HostBindingSequence <= 0 || p.HostBindingSequence != seq || p.CreatedAt.IsZero() || p.CreatedAt.UTC().Format(time.RFC3339Nano) != at {
		return ExecutionPlan{}, ErrExecutionPlan
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw {
		return ExecutionPlan{}, ErrExecutionPlan
	}
	return p, nil
}
func (s *SQLiteStore) LookupExecutionPlan(ctx context.Context, key string) (ExecutionPlan, error) {
	var p ExecutionPlan
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; p, err = executionPlanRecord(tx, key); return err })
	return p, err
}

// AdmitPlacement atomically selects and permanently records one host and its full
// execution plan. Replays return that original plan even after config changes.
// A plan is a reservation, never permission to repeat a physical create.
// Legacy reservations lacking plans cannot be reinterpreted or auto-imported.
func (s *SQLiteStore) AdmitPlacement(ctx context.Context, request PlacementRequest) (ExecutionPlan, error) {
	var result ExecutionPlan
	var outcome error
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		deny := func(reason string, denied error) error {
			outcome = denied
			return sqliteAudit(tx, reason, request.Key, s.now())
		}
		// Validate stable identity separately from mutable policy: exact identity replay
		// is allowed to retrieve its old plan, never replace it with current defaults.
		if request.Key != executionKey(request.Execution, request.Pool) || !bindingName(request.Execution.Controller) || !bindingName(request.Execution.Name) || !bindingName(request.Pool) {
			return deny("placement_denied_identity", ErrExecutionPlan)
		}
		if _, err := cancellationRecord(tx, request.Key); err == nil {
			return deny("placement_denied_cancelled", ErrCancelled)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		if p, err := executionPlanRecord(tx, request.Key); err == nil {
			if p.Execution.Controller != request.Execution.Controller || p.Execution.Name != request.Execution.Name || p.Request.Pool != request.Pool {
				return deny("placement_denied_identity_conflict", ErrConflict)
			}
			r, exists := reservations[request.Key]
			if !exists || r.Request != p.Request {
				return deny("placement_denied_missing_charge", ErrExecutionPlan)
			}
			p.Replay = true
			result = p
			return sqliteAudit(tx, "placement_replayed", request.Key, s.now())
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, exists := reservations[request.Key]; exists {
			return deny("placement_denied_legacy_plan", ErrExecutionPlan)
		}
		if len(request.Hosts) == 0 || len(request.Hosts) > 128 || (request.Strategy != "first_fit" && request.Strategy != "least_runners") {
			return deny("placement_denied_policy", ErrExecutionPlan)
		}
		prototype := Request{Key: request.Key, Pool: request.Pool, Host: request.Hosts[0], Resources: request.Resources}
		if !validRequest(prototype) || !validExecution(request.Execution, prototype) {
			return deny("placement_denied_policy", ErrExecutionPlan)
		}
		if !ready || epoch == int64(1<<63-1) {
			return deny("placement_denied_reconciliation", ErrReconciliation)
		}
		now := s.now()
		if !sqliteObservationsFresh(config, now) {
			if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
				return err
			}
			return deny("placement_denied_freshness", ErrReconciliation)
		}
		pool, ok := config.Pools[request.Pool]
		if !ok {
			return deny("placement_denied_pool", ErrDenied)
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		for _, resource := range inventory {
			if resource.ID == request.Execution.Name {
				return deny("placement_denied_existing_name", ErrConflict)
			}
		}
		allocated, err := sqliteAllocations(config, reservations, inventory)
		if err != nil {
			return err
		}
		if !fits(allocated.Global, request.Resources, config.Global) || !fits(allocated.Pools[request.Pool], request.Resources, pool.Budget) {
			return deny("placement_denied_capacity", ErrDenied)
		}
		ids := append([]string(nil), request.Hosts...)
		sort.Strings(ids)
		bindings, err := sqliteHostBindings(tx, config)
		if err != nil {
			return err
		}
		selected := ""
		for i, id := range ids {
			if i > 0 && ids[i-1] == id {
				return deny("placement_denied_duplicate_host", ErrExecutionPlan)
			}
			host, exists := config.Hosts[id]
			if !exists {
				return deny("placement_denied_unknown_host", ErrExecutionPlan)
			}
			if !contains(pool.Hosts, id) || host.Mode != "enabled" || !host.Healthy || !fits(allocated.Hosts[id], request.Resources, host.Budget) {
				continue
			}
			if binding, ok := bindings[id]; !ok || binding.Sequence <= 0 {
				continue
			}
			if selected == "" || (request.Strategy == "least_runners" && allocated.Hosts[id].Runners < allocated.Hosts[selected].Runners) {
				selected = id
			}
		}
		if selected == "" {
			return deny("placement_denied_no_host", ErrDenied)
		}
		req := prototype
		req.Host = selected
		result = ExecutionPlan{Request: req, Execution: request.Execution, HostBindingSequence: bindings[selected].Sequence, CreatedAt: now.UTC()}
		result.Execution.Profiles = append([]string(nil), request.Execution.Profiles...)
		raw, err := json.Marshal(result)
		if err != nil || len(raw) > 16384 {
			return ErrExecutionPlan
		}
		sum := sha256.Sum256(raw)
		if _, err := tx.Exec("INSERT INTO reservations(request_key,pool,host,cpu,ram,runners,state,created_at) VALUES(?,?,?,?,?,?,? ,?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Runners, "creating", result.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO execution_plans(request_key,payload,payload_hash,host_binding_sequence,created_at) VALUES(?,?,?,?,?)", req.Key, string(raw), hex.EncodeToString(sum[:]), result.HostBindingSequence, result.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return sqliteAudit(tx, "placement_reserved", req.Key, now)
	})
	if err != nil {
		return ExecutionPlan{}, err
	}
	if outcome != nil {
		return ExecutionPlan{}, outcome
	}
	return result, nil
}

// sqliteCheckExecutionPlans validates whitelisted immutable plans on an isolated
// export. Every plan must retain its original charge OR exact cancellation,
// binding sequence and (when present) matching dispatch provenance.
func sqliteCheckExecutionPlans(ctx context.Context, s *SQLiteStore) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT request_key FROM execution_plans")
		if err != nil {
			return err
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		bindings, err := sqliteHostBindings(tx, config)
		if err != nil {
			return err
		}
		for _, key := range keys {
			p, err := executionPlanRecord(tx, key)
			if err != nil {
				return err
			}
			binding, ok := bindings[p.Request.Host]
			if !ok || binding.Sequence != p.HostBindingSequence {
				return ErrExecutionPlan
			}
			if r, exists := reservations[key]; exists {
				if r.Request != p.Request {
					return ErrExecutionPlan
				}
			} else {
				cancelled, err := cancellationRecord(tx, key)
				if err != nil || cancelled.Request != p.Request {
					return ErrExecutionPlan
				}
			}
			if d, _, err := dispatchRecord(tx, key); err == nil {
				if d.HostBindingSequence != p.HostBindingSequence {
					return ErrExecutionPlan
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		return nil
	})
}

// Counts are presentation only. Authorization always uses the complete indexed
// immutable plan/request/binding comparison, never these totals.
func sqliteExecutionPlanCounts(tx *sql.Tx) (int64, int64, error) {
	var total, unplanned int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM execution_plans").Scan(&total); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM reservations r LEFT JOIN execution_plans p ON p.request_key=r.request_key WHERE p.request_key IS NULL").Scan(&unplanned); err != nil {
		return 0, 0, err
	}
	return total, unplanned, nil
}
