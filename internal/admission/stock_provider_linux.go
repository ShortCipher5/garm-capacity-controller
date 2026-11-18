package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrStockProviderOrigin = errors.New("exact authenticated stock-provider origin required")

// StockProviderAuthority is supplied only by the credential-owning server after
// verified mTLS and an exact controller/pool/provider/client-leaf allowlist check.
// Never decode it from provider JSON. It is NOT native sole-writer authority,
// original GARM preparation, a job fence, or permission to release capacity.
type StockProviderAuthority struct {
	Controller        string `json:"controller"`
	PoolID            string `json:"pool_id"`
	ProviderName      string `json:"provider_name"`
	ClientFingerprint string `json:"client_fingerprint"`
}

type stockProviderOrigin struct {
	Authority     StockProviderAuthority `json:"authority"`
	CommandDigest string                 `json:"command_digest"`
}

const sqliteStockProviderSchema = `
CREATE TABLE stock_provider_origins (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES execution_plans(request_key),
 payload TEXT NOT NULL CHECK(length(payload)<=4096),
 plan_digest TEXT NOT NULL CHECK(length(plan_digest)=64),
 host_binding_sequence INTEGER NOT NULL REFERENCES audit(sequence),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER stock_provider_origin_no_update BEFORE UPDATE ON stock_provider_origins
BEGIN SELECT RAISE(ABORT,'stock provider origins are immutable'); END;
CREATE TRIGGER stock_provider_origin_no_delete BEFORE DELETE ON stock_provider_origins
BEGIN SELECT RAISE(ABORT,'stock provider origins are permanent'); END;
CREATE TRIGGER stock_provider_origin_no_adoption BEFORE INSERT ON stock_provider_origins
WHEN EXISTS(SELECT 1 FROM original_preparations WHERE request_key=NEW.request_key)
 OR EXISTS(SELECT 1 FROM dispatches WHERE reservation_key=NEW.request_key)
 OR EXISTS(SELECT 1 FROM cancellations WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'existing execution cannot adopt stock origin'); END;
CREATE TRIGGER preparation_no_stock_origin BEFORE INSERT ON original_preparations
WHEN EXISTS(SELECT 1 FROM stock_provider_origins WHERE request_key=NEW.request_key)
BEGIN SELECT RAISE(ABORT,'stock execution cannot adopt original preparation'); END;
ALTER TABLE dispatches ADD COLUMN stock_provider_sequence INTEGER REFERENCES audit(sequence);
CREATE TRIGGER dispatch_stock_sequence_immutable BEFORE UPDATE OF stock_provider_sequence ON dispatches
WHEN OLD.stock_provider_sequence IS NOT NEW.stock_provider_sequence
BEGIN SELECT RAISE(ABORT,'stock provider provenance is immutable'); END;
DROP TRIGGER active_dispatch_requires_preparation;
CREATE TRIGGER active_dispatch_requires_preparation BEFORE INSERT ON dispatches
WHEN EXISTS(SELECT 1 FROM ledger_lineage WHERE id=1 AND runtime_kind='active')
 AND NEW.stock_provider_sequence IS NULL
BEGIN SELECT CASE WHEN NEW.original_preparation_sequence IS NULL OR NOT EXISTS(
 SELECT 1 FROM original_preparations p WHERE p.request_key=NEW.reservation_key
 AND p.record_sequence=NEW.original_preparation_sequence AND p.host_binding_sequence=NEW.host_binding_sequence)
 THEN RAISE(ABORT,'original preparation required') END; END;
CREATE TRIGGER dispatch_stock_origin_exact BEFORE INSERT ON dispatches
WHEN NEW.stock_provider_sequence IS NOT NULL OR EXISTS(SELECT 1 FROM stock_provider_origins WHERE request_key=NEW.reservation_key)
BEGIN SELECT CASE WHEN NEW.original_preparation_sequence IS NOT NULL OR NEW.stock_provider_sequence IS NULL OR NOT EXISTS(
 SELECT 1 FROM stock_provider_origins s WHERE s.request_key=NEW.reservation_key
 AND s.record_sequence=NEW.stock_provider_sequence AND s.host_binding_sequence=NEW.host_binding_sequence)
 THEN RAISE(ABORT,'exact stock provider origin required') END; END;
PRAGMA user_version=16;
`

func validStockOrigin(origin stockProviderOrigin, request PlacementRequest) bool {
	a := origin.Authority
	return bindingName(a.Controller) && bindingName(a.PoolID) && bindingName(a.ProviderName) && bindingFingerprint(a.ClientFingerprint) && bindingFingerprint(origin.CommandDigest) && a.Controller == request.Execution.Controller && a.PoolID == request.Pool
}

// AdmitStockProviderPlacement records stock-provider provenance and the immutable
// execution plan in the SAME budget/reservation transaction. It cannot adopt an
// existing legacy/prepared reservation. The digest must cover canonical stable,
// nonsecret command fields; tokens/cloud-init must never enter this ledger.
func (s *SQLiteStore) AdmitStockProviderPlacement(ctx context.Context, request PlacementRequest, authority StockProviderAuthority, commandDigest string) (ExecutionPlan, error) {
	origin := stockProviderOrigin{Authority: authority, CommandDigest: commandDigest}
	if !validStockOrigin(origin, request) {
		return ExecutionPlan{}, ErrStockProviderOrigin
	}
	return s.admitPlacement(ctx, request, &origin)
}

func insertStockProviderOrigin(tx *sql.Tx, plan ExecutionPlan, origin stockProviderOrigin, now time.Time) error {
	if !validStockOrigin(origin, PlacementRequest{Execution: plan.Execution, Pool: plan.Request.Pool}) {
		return ErrStockProviderOrigin
	}
	raw, err := json.Marshal(origin)
	if err != nil || len(raw) > 4096 {
		return ErrStockProviderOrigin
	}
	digest, err := executionPlanDigest(plan)
	if err != nil {
		return err
	}
	if err := sqliteAudit(tx, "stock_provider_origin_recorded", plan.Request.Key, now); err != nil {
		return err
	}
	var sequence int64
	if err := tx.QueryRow("SELECT max(sequence) FROM audit").Scan(&sequence); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO stock_provider_origins(request_key,payload,plan_digest,host_binding_sequence,record_sequence) VALUES(?,?,?,?,?)", plan.Request.Key, string(raw), digest, plan.HostBindingSequence, sequence)
	return err
}

func checkStockProviderOrigin(tx *sql.Tx, plan ExecutionPlan, expected *stockProviderOrigin) error {
	var raw, digest string
	var binding, sequence int64
	err := tx.QueryRow("SELECT payload,plan_digest,host_binding_sequence,record_sequence FROM stock_provider_origins WHERE request_key=?", plan.Request.Key).Scan(&raw, &digest, &binding, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		var history int
		if err := tx.QueryRow("SELECT count(*) FROM audit WHERE kind='stock_provider_origin_recorded' AND request_key=?", plan.Request.Key).Scan(&history); err != nil {
			return err
		}
		if expected == nil && history == 0 {
			return nil
		}
		return ErrStockProviderOrigin
	}
	if err != nil {
		return err
	}
	if expected == nil {
		return ErrStockProviderOrigin
	}
	var stored stockProviderOrigin
	if json.Unmarshal([]byte(raw), &stored) != nil || !validStockOrigin(stored, PlacementRequest{Execution: plan.Execution, Pool: plan.Request.Pool}) || stored != *expected || binding != plan.HostBindingSequence || sequence <= 0 {
		return ErrStockProviderOrigin
	}
	normalized, err := json.Marshal(stored)
	if err != nil || string(normalized) != raw {
		return ErrStockProviderOrigin
	}
	want, err := executionPlanDigest(plan)
	if err != nil || want != digest {
		return ErrStockProviderOrigin
	}
	var kind, key string
	if tx.QueryRow("SELECT kind,request_key FROM audit WHERE sequence=?", sequence).Scan(&kind, &key) != nil || kind != "stock_provider_origin_recorded" || key != plan.Request.Key {
		return ErrStockProviderOrigin
	}
	return checkStockGrantLink(tx, plan, *expected)
}

// ExecuteStockProviderDispatch is a distinct provenance path, not a fallback for
// missing original preparation. The canonical intent blocks cancellation before
// the callback can run. Callback error/panic/cancellation or uncertain receipt
// retains charge and never permits a second submission, including after restart.
// The trusted caller must independently enforce host-native exclusive authority.
func (s *SQLiteStore) ExecuteStockProviderDispatch(ctx context.Context, plan ExecutionPlan, binding HostBinding, authority StockProviderAuthority, commandDigest string, execute func(context.Context, DispatchPermit) (DispatchReceipt, error)) (DispatchRecord, error) {
	origin := stockProviderOrigin{Authority: authority, CommandDigest: commandDigest}
	if !validStockOrigin(origin, PlacementRequest{Execution: plan.Execution, Pool: plan.Request.Pool}) {
		return DispatchRecord{}, ErrStockProviderOrigin
	}
	if !validHostBinding(binding) || binding.Sequence <= 0 || binding.Host != plan.Request.Host || plan.HostBindingSequence != binding.Sequence {
		return DispatchRecord{}, ErrHostBinding
	}
	return s.executeDispatch(ctx, plan.Request, &binding, &plan, nil, &origin, execute)
}

func sqliteCheckStockProviderOrigins(ctx context.Context, s *SQLiteStore) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var missing int
		if err := tx.QueryRow("SELECT count(*) FROM audit a LEFT JOIN stock_provider_origins s ON s.record_sequence=a.sequence AND s.request_key=a.request_key WHERE a.kind='stock_provider_origin_recorded' AND s.request_key IS NULL").Scan(&missing); err != nil {
			return err
		}
		if missing != 0 {
			return ErrStockProviderOrigin
		}
		rows, err := tx.Query("SELECT request_key,payload FROM stock_provider_origins")
		if err != nil {
			return err
		}
		type entry struct{ key, raw string }
		var entries []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.key, &e.raw); err != nil {
				rows.Close()
				return err
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			plan, err := executionPlanRecord(tx, e.key)
			if err != nil {
				return err
			}
			var origin stockProviderOrigin
			if json.Unmarshal([]byte(e.raw), &origin) != nil {
				return ErrStockProviderOrigin
			}
			if err := checkStockProviderOrigin(tx, plan, &origin); err != nil {
				return err
			}
			var conflicts int
			if err := tx.QueryRow("SELECT count(*) FROM original_preparations WHERE request_key=?", e.key).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts != 0 {
				return ErrStockProviderOrigin
			}
		}
		var invalid int
		err = tx.QueryRow(`SELECT count(*) FROM dispatches d LEFT JOIN stock_provider_origins s ON s.request_key=d.reservation_key WHERE
   (s.request_key IS NOT NULL AND (d.stock_provider_sequence IS NULL OR d.stock_provider_sequence!=s.record_sequence OR d.host_binding_sequence!=s.host_binding_sequence OR d.original_preparation_sequence IS NOT NULL))
   OR (s.request_key IS NULL AND d.stock_provider_sequence IS NOT NULL)`).Scan(&invalid)
		if err != nil {
			return err
		}
		if invalid != 0 {
			return ErrStockProviderOrigin
		}
		return nil
	})
}
