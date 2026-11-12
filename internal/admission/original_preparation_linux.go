package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
)

var ErrOriginalPreparation = errors.New("source-bound original preparation required")

// Schema8 persists original source metadata. Actual source authentication, Garm
// row probing and positive-agent reconciliation remain separate required gates.
const sqliteOriginalPreparationSchema = `
CREATE TABLE original_preparations (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES execution_plans(request_key),
 instance_id TEXT UNIQUE NOT NULL,
 source_scope TEXT NOT NULL CHECK(length(source_scope)=64),
 host_binding_sequence INTEGER NOT NULL REFERENCES audit(sequence),
 agent_scope TEXT NOT NULL CHECK(length(agent_scope)=64),
 agent_id INTEGER NOT NULL CHECK(agent_id>=0),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE UNIQUE INDEX original_preparation_agent_once ON original_preparations(agent_scope,agent_id) WHERE agent_id>0;
CREATE TABLE original_preparation_history_exclusions(request_key TEXT PRIMARY KEY NOT NULL) STRICT;
ALTER TABLE dispatches ADD COLUMN original_preparation_sequence INTEGER REFERENCES audit(sequence);
CREATE TRIGGER dispatch_preparation_sequence_immutable BEFORE UPDATE OF original_preparation_sequence ON dispatches
WHEN OLD.original_preparation_sequence IS NOT NEW.original_preparation_sequence
BEGIN SELECT RAISE(ABORT,'original preparation provenance is immutable'); END;
CREATE TRIGGER active_dispatch_requires_preparation BEFORE INSERT ON dispatches
WHEN EXISTS(SELECT 1 FROM ledger_lineage WHERE id=1 AND runtime_kind='active')
BEGIN SELECT CASE WHEN NEW.original_preparation_sequence IS NULL OR NOT EXISTS(
 SELECT 1 FROM original_preparations p WHERE p.request_key=NEW.reservation_key
 AND p.record_sequence=NEW.original_preparation_sequence AND p.host_binding_sequence=NEW.host_binding_sequence)
 THEN RAISE(ABORT,'original preparation required') END; END;

CREATE TRIGGER original_preparations_no_update BEFORE UPDATE ON original_preparations BEGIN SELECT RAISE(ABORT,'original preparations are immutable'); END;
CREATE TRIGGER original_preparations_no_delete BEFORE DELETE ON original_preparations BEGIN SELECT RAISE(ABORT,'original preparations are immutable'); END;
CREATE TRIGGER original_preparation_exclusions_no_update BEFORE UPDATE ON original_preparation_history_exclusions BEGIN SELECT RAISE(ABORT,'preparation history exclusions are immutable'); END;
CREATE TRIGGER original_preparation_exclusions_no_delete BEFORE DELETE ON original_preparation_history_exclusions BEGIN SELECT RAISE(ABORT,'preparation history exclusions are immutable'); END;
PRAGMA user_version=8;
`

// PreparationAuthority is supplied ONLY by the credential-owning server after
// exact route-scoped client-certificate authorization. It is not caller/operator
// JSON and is not hardware, GitHub attestation or external-writer fencing.
type PreparationAuthority struct {
	Controller             string `json:"controller"`
	SourceScopeFingerprint string `json:"source_scope"`
	ClientFingerprint      string `json:"client_fingerprint"`
	Role                   string `json:"role"`
	PoolID                 string `json:"pool_id"`
	ProviderName           string `json:"provider_name"`
	EntityType             string `json:"entity_type"`
	EntityID               string `json:"entity_id"`
	ForgeEndpointName      string `json:"forge_endpoint_name"`
	Generation             uint64 `json:"generation"`
	JITEnabled             bool   `json:"jit_enabled"`
}

func (a PreparationAuthority) matches(r garmprotocol.PreparationRequest) bool {
	return bindingName(a.Controller) && bindingFingerprint(a.SourceScopeFingerprint) && bindingFingerprint(a.ClientFingerprint) && a.Role == "source" && a.PoolID == r.PoolID && a.ProviderName == r.ProviderName && a.EntityType == r.EntityType && a.EntityID == r.EntityID && a.ForgeEndpointName == r.ForgeEndpointName && a.Generation == r.Generation && a.JITEnabled == r.JITEnabled
}
func (a PreparationAuthority) samePolicy(b PreparationAuthority) bool {
	// Authorized certificate rotation within the SAME configured route need not
	// rewrite historical issuer data. Actual current authorization is the server's
	// responsibility; changing its route scope invalidates this match.
	a.ClientFingerprint = ""
	b.ClientFingerprint = ""
	return a == b
}

type OriginalPreparationReceipt struct {
	Protocol            string                          `json:"protocol"`
	Original            garmprotocol.PreparationRequest `json:"original"`
	Request             Request                         `json:"request"`
	PlanDigest          string                          `json:"plan_digest"`
	HostBindingSequence int64                           `json:"host_binding_sequence"`
	Source              PreparationAuthority            `json:"source"`
	RecordSequence      int64                           `json:"record_sequence"`
	RecordedAt          time.Time                       `json:"recorded_at"`
	Replay              bool                            `json:"-"`
}

func executionPlanDigest(plan ExecutionPlan) (string, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", ErrExecutionPlan
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func originalAgentScope(a PreparationAuthority) string {
	raw, _ := json.Marshal(struct{ Controller, EntityType, EntityID, ForgeEndpointName string }{a.Controller, a.EntityType, a.EntityID, a.ForgeEndpointName})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func originalPreparationRecord(tx *sql.Tx, key string) (OriginalPreparationReceipt, error) {
	var raw, hash, id, scope, agentScope string
	var agentID int64
	var sequence, bindingSequence int64
	if err := tx.QueryRow("SELECT payload,payload_hash,instance_id,source_scope,record_sequence,agent_scope,agent_id,host_binding_sequence FROM original_preparations WHERE request_key=?", key).Scan(&raw, &hash, &id, &scope, &sequence, &agentScope, &agentID, &bindingSequence); err != nil {
		return OriginalPreparationReceipt{}, err
	}
	sum := sha256.Sum256([]byte(raw))
	if len(raw) > 16384 || hex.EncodeToString(sum[:]) != hash {
		return OriginalPreparationReceipt{}, ErrOriginalPreparation
	}
	var r OriginalPreparationReceipt
	if json.Unmarshal([]byte(raw), &r) != nil || r.Protocol != garmprotocol.PreparationVersion || r.Original.Validate() != nil || !r.Source.matches(r.Original) || !validRequest(r.Request) || r.Request.Key != key || r.Request.Pool != r.Original.PoolID || r.Request.Key != executionKey(ExecutionImage{Controller: r.Source.Controller, Name: r.Original.Name}, r.Original.PoolID) || r.Original.InstanceID != id || r.Original.AgentID != agentID || originalAgentScope(r.Source) != agentScope || r.Source.SourceScopeFingerprint != scope || !bindingFingerprint(r.PlanDigest) || r.RecordSequence <= 0 || r.RecordSequence != sequence || r.HostBindingSequence <= 0 || r.HostBindingSequence != bindingSequence || r.RecordedAt.IsZero() {
		return OriginalPreparationReceipt{}, ErrOriginalPreparation
	}
	canonical, err := json.Marshal(r)
	if err != nil || string(canonical) != raw {
		return OriginalPreparationReceipt{}, ErrOriginalPreparation
	}
	var kind, subject string
	if tx.QueryRow("SELECT kind,request_key FROM audit WHERE sequence=?", sequence).Scan(&kind, &subject) != nil || kind != "original_preparation_recorded" || subject != key {
		return OriginalPreparationReceipt{}, ErrOriginalPreparation
	}
	return r, nil
}
func preparationUncancelled(tx *sql.Tx, key string) error {
	if _, err := cancellationRecord(tx, key); err == nil {
		return ErrCancelled
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var excluded bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM original_preparation_history_exclusions WHERE request_key=?)", key).Scan(&excluded); err != nil {
		return err
	}
	if excluded {
		return ErrOriginalPreparation
	}
	return nil
}
func originalPreparationPlan(tx *sql.Tx, r garmprotocol.PreparationRequest, a PreparationAuthority) (ExecutionPlan, string, error) {
	if r.Validate() != nil || !a.matches(r) {
		return ExecutionPlan{}, "", ErrOriginalPreparation
	}
	key := executionKey(ExecutionImage{Controller: a.Controller, Name: r.Name}, r.PoolID)
	if err := preparationUncancelled(tx, key); err != nil {
		return ExecutionPlan{}, "", err
	}
	plan, err := executionPlanRecord(tx, key)
	if err != nil || plan.Execution.Controller != a.Controller || plan.Execution.Name != r.Name || plan.Request.Pool != r.PoolID {
		return ExecutionPlan{}, "", ErrOriginalPreparation
	}
	reserved := Request{Key: key}
	var state string
	err = tx.QueryRow("SELECT pool,host,cpu,ram,runners,state FROM reservations WHERE request_key=?", key).Scan(&reserved.Pool, &reserved.Host, &reserved.Resources.CPU, &reserved.Resources.RAM, &reserved.Resources.Runners, &state)
	if err != nil || !validRequest(reserved) || !validState(state) || reserved != plan.Request {
		return ExecutionPlan{}, "", ErrOriginalPreparation
	}
	binding, err := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", plan.Request.Host).Scan)
	if err != nil || binding.Sequence != plan.HostBindingSequence {
		return ExecutionPlan{}, "", ErrOriginalPreparation
	}
	digest, err := executionPlanDigest(plan)
	return plan, digest, err
}

// RecordOriginalPreparation records NONSECRET original source metadata before DB
// publication. It grants no physical authority. No existing dispatch/cancellation
// or inherited history may be retroactively blessed. Commit ambiguity retains all
// charge and must prevent source row publication until an exact known ACK exists.
func (s *SQLiteStore) RecordOriginalPreparation(ctx context.Context, r garmprotocol.PreparationRequest, authority PreparationAuthority) (OriginalPreparationReceipt, error) {
	var receipt OriginalPreparationReceipt
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		plan, digest, err := originalPreparationPlan(tx, r, authority)
		if err != nil {
			return err
		}
		if _, _, err := dispatchRecord(tx, plan.Request.Key); !errors.Is(err, sql.ErrNoRows) {
			return ErrOriginalPreparation
		}
		inventory, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		for _, resource := range inventory {
			if resource.ReservationKey == plan.Request.Key || resource.ID == r.Name {
				return ErrOriginalPreparation
			}
		}
		if old, err := originalPreparationRecord(tx, plan.Request.Key); err == nil {
			if old.Original != r || old.Request != plan.Request || old.PlanDigest != digest || old.HostBindingSequence != plan.HostBindingSequence || !old.Source.samePolicy(authority) {
				return ErrConflict
			}
			old.Replay = true
			receipt = old
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var state string
		if tx.QueryRow("SELECT state FROM reservations WHERE request_key=?", plan.Request.Key).Scan(&state) != nil || state != "creating" {
			return ErrOriginalPreparation
		}
		var reused bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM original_preparations WHERE instance_id=? OR (agent_scope=? AND agent_id=? AND agent_id>0))", r.InstanceID, originalAgentScope(authority), r.AgentID).Scan(&reused); err != nil {
			return err
		}
		if reused {
			return ErrConflict
		}
		now := s.now().UTC()
		if err := sqliteAudit(tx, "original_preparation_recorded", plan.Request.Key, now); err != nil {
			return err
		}
		var sequence int64
		if err := tx.QueryRow("SELECT last_insert_rowid()").Scan(&sequence); err != nil || sequence <= 0 {
			if err != nil {
				return err
			}
			return ErrOriginalPreparation
		}
		receipt = OriginalPreparationReceipt{Protocol: garmprotocol.PreparationVersion, Original: r, Request: plan.Request, PlanDigest: digest, HostBindingSequence: plan.HostBindingSequence, Source: authority, RecordSequence: sequence, RecordedAt: now}
		raw, err := json.Marshal(receipt)
		if err != nil || len(raw) > 16384 {
			return ErrOriginalPreparation
		}
		sum := sha256.Sum256(raw)
		_, err = tx.Exec("INSERT INTO original_preparations(request_key,instance_id,source_scope,host_binding_sequence,agent_scope,agent_id,payload,payload_hash,record_sequence) VALUES(?,?,?,?,?,?,?,?,?)", plan.Request.Key, r.InstanceID, authority.SourceScopeFingerprint, plan.HostBindingSequence, originalAgentScope(authority), r.AgentID, string(raw), hex.EncodeToString(sum[:]), sequence)
		return err
	})
	if err != nil {
		return OriginalPreparationReceipt{}, err
	}
	return receipt, nil
}
func (s *SQLiteStore) LookupOriginalPreparation(ctx context.Context, key string) (OriginalPreparationReceipt, error) {
	var r OriginalPreparationReceipt
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { var err error; r, err = originalPreparationRecord(tx, key); return err })
	return r, err
}

// ValidateOriginalPreparation is a metadata match only, not source-row existence,
// no-dispatch, permission to replay a physical create or a release proof. The
// network gateway must additionally read/check the actual authenticated Garm row.
func (s *SQLiteStore) ValidateOriginalPreparation(ctx context.Context, r garmprotocol.PreparationRequest, authority PreparationAuthority) (OriginalPreparationReceipt, error) {
	var receipt OriginalPreparationReceipt
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		plan, digest, err := originalPreparationPlan(tx, r, authority)
		if err != nil {
			return err
		}
		receipt, err = originalPreparationRecord(tx, plan.Request.Key)
		if err != nil {
			return err
		}
		if receipt.Original != r || receipt.Request != plan.Request || receipt.PlanDigest != digest || receipt.HostBindingSequence != plan.HostBindingSequence || !receipt.Source.samePolicy(authority) {
			return ErrConflict
		}
		receipt.Replay = true
		return nil
	})
	if err != nil {
		return OriginalPreparationReceipt{}, err
	}
	return receipt, nil
}

// sqliteCheckOriginalPreparations validates immutable source history on a closed
// export. Cancellation does not erase that history; old receipt ACKs remain
// non-authoritative forever once the independent tombstone exists.
func sqliteCheckOriginalPreparations(ctx context.Context, s *SQLiteStore) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT request_key FROM original_preparations")
		if err != nil {
			return err
		}
		var keys []string
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
		for _, key := range keys {
			r, err := originalPreparationRecord(tx, key)
			if err != nil {
				return err
			}
			p, err := executionPlanRecord(tx, key)
			if err != nil {
				return err
			}
			digest, err := executionPlanDigest(p)
			if err != nil || r.Request != p.Request || r.PlanDigest != digest || r.HostBindingSequence != p.HostBindingSequence {
				return ErrOriginalPreparation
			}
			var excluded bool
			if tx.QueryRow("SELECT EXISTS(SELECT 1 FROM original_preparation_history_exclusions WHERE request_key=?)", key).Scan(&excluded) != nil || excluded {
				return ErrOriginalPreparation
			}
		}
		rows, err = tx.Query("SELECT reservation_key,original_preparation_sequence FROM dispatches WHERE original_preparation_sequence IS NOT NULL")
		if err != nil {
			return err
		}
		type reference struct {
			key      string
			sequence int64
		}
		var references []reference
		for rows.Next() {
			var r reference
			if err := rows.Scan(&r.key, &r.sequence); err != nil {
				rows.Close()
				return err
			}
			references = append(references, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, ref := range references {
			r, err := originalPreparationRecord(tx, ref.key)
			if err != nil || r.RecordSequence != ref.sequence {
				return ErrOriginalPreparation
			}
		}
		return nil
	})
}
