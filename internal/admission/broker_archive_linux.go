package admission

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
)

const sqliteBrokerArchiveSchema = `
CREATE TABLE broker_grant_archives (
 batch_id TEXT PRIMARY KEY NOT NULL REFERENCES broker_grant_batches(batch_id),
 payload BLOB NOT NULL CHECK(length(payload)>0 AND length(payload)<=2097152),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 original_bytes INTEGER NOT NULL CHECK(original_bytes>0 AND original_bytes<=1048576),
 grant_count INTEGER NOT NULL CHECK(grant_count>0 AND grant_count<=1000),
 sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER broker_archive_no_update BEFORE UPDATE ON broker_grant_archives BEGIN SELECT RAISE(ABORT,'archive immutable'); END;
CREATE TRIGGER broker_archive_no_delete BEFORE DELETE ON broker_grant_archives BEGIN SELECT RAISE(ABORT,'archive permanent'); END;
DROP TRIGGER broker_batch_no_update;
CREATE TRIGGER broker_batch_no_update BEFORE UPDATE ON broker_grant_batches
WHEN NOT(OLD.batch_id IS NEW.batch_id AND OLD.payload_hash IS NEW.payload_hash AND OLD.sequence IS NEW.sequence AND length(OLD.payload)>0 AND NEW.payload='' AND EXISTS(SELECT 1 FROM broker_grant_archives a WHERE a.batch_id=OLD.batch_id AND a.payload_hash=OLD.payload_hash))
BEGIN SELECT RAISE(ABORT,'batch identity immutable'); END;
DROP TRIGGER broker_grant_identity_immutable;
DROP TRIGGER broker_grant_terminal;
CREATE TRIGGER broker_grant_identity_immutable BEFORE UPDATE ON broker_grants
WHEN OLD.grant_id IS NOT NEW.grant_id OR OLD.batch_id IS NOT NEW.batch_id OR OLD.payload_hash IS NOT NEW.payload_hash OR
 (OLD.payload IS NOT NEW.payload AND NOT(length(OLD.payload)>0 AND NEW.payload='' AND OLD.state!='unused' AND OLD.state=NEW.state AND OLD.request_key IS NEW.request_key AND EXISTS(SELECT 1 FROM broker_grant_archives a WHERE a.batch_id=OLD.batch_id)))
BEGIN SELECT RAISE(ABORT,'grant identity immutable'); END;
CREATE TRIGGER broker_grant_terminal BEFORE UPDATE ON broker_grants
WHEN (OLD.state!=NEW.state OR OLD.request_key IS NOT NEW.request_key) AND (OLD.state!='unused' OR NEW.state='unused')
BEGIN SELECT RAISE(ABORT,'grant transition forbidden'); END;
CREATE INDEX broker_grants_by_batch ON broker_grants(batch_id);
CREATE INDEX broker_grants_hot ON broker_grants(grant_id) WHERE payload!='';
CREATE INDEX broker_batches_hot ON broker_grant_batches(sequence) WHERE payload!='';
CREATE INDEX broker_grant_audit_kind ON audit(kind,sequence,request_key) WHERE kind IN ('broker_grants_issued','broker_grant_batch_archived');
DROP TRIGGER reservation_current_accounting;
CREATE TRIGGER reservation_current_accounting BEFORE INSERT ON reservations WHEN NEW.accounting_version!=18 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
DROP TRIGGER dispatch_current_accounting;
CREATE TRIGGER dispatch_current_accounting BEFORE INSERT ON dispatches WHEN NEW.grant_accounting_version!=18 BEGIN SELECT RAISE(ABORT,'current shared grant accounting required'); END;
PRAGMA user_version=18;
`

// BrokerArchiveResult reports payload compaction only, never capacity release.
type BrokerArchiveResult struct {
	BatchID         string `json:"batch_id"`
	Grants          int    `json:"grants"`
	OriginalBytes   int    `json:"original_bytes"`
	CompressedBytes int    `json:"compressed_bytes"`
	Replay          bool   `json:"replay"`
}

func decodeBrokerBatch(raw, hash, id string) (BrokerGrantBatch, error) {
	var b BrokerGrantBatch
	if len(raw) == 0 || len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &b) != nil || b.ID != id || !bindingFingerprint(id) || len(b.Grants) == 0 || len(b.Grants) > 1000 {
		return b, ErrBrokerGrant
	}
	canonical, digest, e := grantHash(b)
	if e != nil || canonical != raw || digest != hash {
		return b, ErrBrokerGrant
	}
	for _, g := range b.Grants {
		if !validGrantSpec(g) {
			return b, ErrBrokerGrant
		}
	}
	return b, nil
}
func archivedBrokerBatch(tx *sql.Tx, id string) (BrokerGrantBatch, BrokerArchiveResult, error) {
	var data []byte
	var hash string
	var result BrokerArchiveResult
	result.BatchID = id
	var batchHash, batchPayload, issueKind, issueID, archiveKind, archiveID string
	e := tx.QueryRow(`SELECT x.payload,x.payload_hash,x.original_bytes,x.grant_count,b.payload_hash,b.payload,i.kind,i.request_key,a.kind,a.request_key FROM broker_grant_archives x JOIN broker_grant_batches b ON b.batch_id=x.batch_id JOIN audit i ON i.sequence=b.sequence JOIN audit a ON a.sequence=x.sequence WHERE x.batch_id=?`, id).Scan(&data, &hash, &result.OriginalBytes, &result.Grants, &batchHash, &batchPayload, &issueKind, &issueID, &archiveKind, &archiveID)
	if e == nil && (hash != batchHash || batchPayload != "" || issueKind != "broker_grants_issued" || issueID != id || archiveKind != "broker_grant_batch_archived" || archiveID != id) {
		e = ErrBrokerGrant
	}
	if e != nil {
		return BrokerGrantBatch{}, result, e
	}
	result.CompressedBytes = len(data)
	if len(data) == 0 || len(data) > 2<<20 || result.OriginalBytes < 1 || result.OriginalBytes > 1<<20 {
		return BrokerGrantBatch{}, result, ErrBrokerGrant
	}
	source := bytes.NewReader(data)
	reader, e := gzip.NewReader(source)
	if e != nil {
		return BrokerGrantBatch{}, result, ErrBrokerGrant
	}
	reader.Multistream(false)
	raw, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	closeErr := reader.Close()
	if e != nil || closeErr != nil || len(raw) != result.OriginalBytes || source.Len() != 0 {
		return BrokerGrantBatch{}, result, ErrBrokerGrant
	}
	b, e := decodeBrokerBatch(string(raw), hash, id)
	if e != nil || len(b.Grants) != result.Grants {
		return BrokerGrantBatch{}, result, ErrBrokerGrant
	}
	return b, result, nil
}
func lookupBrokerGrant(tx *sql.Tx, id string) (BrokerGrant, error) {
	var g BrokerGrant
	var raw, hash, batch string
	e := tx.QueryRow("SELECT grant_id,batch_id,payload,payload_hash,state,COALESCE(request_key,'') FROM broker_grants WHERE grant_id=?", id).Scan(&g.ID, &batch, &raw, &hash, &g.State, &g.RequestKey)
	if e != nil {
		return g, e
	}
	if raw != "" {
		if len(raw) > 16384 || json.Unmarshal([]byte(raw), &g.Spec) != nil {
			return g, ErrBrokerGrant
		}
	} else {
		b, _, e := archivedBrokerBatch(tx, batch)
		if e != nil {
			return g, e
		}
		found := false
		for i, spec := range b.Grants {
			if brokerGrantID(batch, i) == id {
				g.Spec = spec
				found = true
				break
			}
		}
		if !found || g.State == "unused" {
			return g, ErrBrokerGrant
		}
	}
	canonical, digest, e := grantHash(g.Spec)
	if e != nil || !validGrantSpec(g.Spec) || hash != digest || (raw != "" && canonical != raw) || (g.State == "consumed") != (g.RequestKey != "") {
		return g, ErrBrokerGrant
	}
	return g, nil
}

// validateBrokerArchiveMetadata never decompresses unbounded lifetime history on
// the allocation hot path. Permanent IDs/counts/hashes/audit linkage remain live.
// Exact replay and full open/backup checks verify compressed bytes as well.
func validateBrokerArchiveMetadata(tx *sql.Tx) error {
	var invalid bool
	e := tx.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM broker_grant_batches b LEFT JOIN audit a ON a.sequence=b.sequence AND a.request_key=b.batch_id AND a.kind='broker_grants_issued' WHERE a.sequence IS NULL
 UNION ALL
 SELECT 1 FROM broker_grant_archives x LEFT JOIN broker_grant_batches b ON b.batch_id=x.batch_id LEFT JOIN audit a ON a.sequence=x.sequence AND a.request_key=x.batch_id AND a.kind='broker_grant_batch_archived' WHERE b.batch_id IS NULL OR b.payload!='' OR b.payload_hash!=x.payload_hash OR a.sequence IS NULL OR x.grant_count!=(SELECT count(*) FROM broker_grants g WHERE g.batch_id=x.batch_id)
 UNION ALL
 SELECT 1 FROM broker_grant_batches b LEFT JOIN broker_grant_archives x ON x.batch_id=b.batch_id WHERE b.payload='' AND x.batch_id IS NULL
 UNION ALL
 SELECT 1 FROM audit a LEFT JOIN broker_grant_archives x ON x.sequence=a.sequence AND x.batch_id=a.request_key WHERE a.kind='broker_grant_batch_archived' AND x.batch_id IS NULL
 UNION ALL
 SELECT 1 FROM broker_grants g JOIN broker_grant_batches b ON b.batch_id=g.batch_id WHERE (g.payload='')!=(b.payload='') OR (g.payload='' AND g.state='unused')
 LIMIT 1)`).Scan(&invalid)
	if e != nil {
		return e
	}
	if invalid {
		return ErrBrokerGrant
	}
	return nil
}
func brokerGrantTerminal(tx *sql.Tx, g BrokerGrant) bool {
	if g.State == "revoked" || g.State == "expired" {
		return true
	}
	if g.State != "consumed" {
		return false
	}
	p, e := executionPlanRecord(tx, g.RequestKey)
	if e != nil || p.Request.Host != g.Spec.Host || p.Request.Pool != g.Spec.Authority.PoolID || p.Request.Resources != g.Spec.Resources {
		return false
	}
	if c, e := cancellationRecord(tx, g.RequestKey); e == nil && c.Request == p.Request {
		var conflict bool
		e = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM dispatches WHERE reservation_key=? UNION ALL SELECT 1 FROM inventory WHERE reservation_key=? UNION ALL SELECT 1 FROM reservations WHERE request_key=? LIMIT 1)`, g.RequestKey, g.RequestKey, g.RequestKey).Scan(&conflict)
		return e == nil && !conflict
	}
	release, e := releaseRecord(tx, g.RequestKey)
	if e != nil || release.Request != p.Request {
		return false
	}
	var uncertain bool
	e = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM dispatches WHERE reservation_key=? AND state IN ('intent','submitted','unknown') UNION ALL SELECT 1 FROM inventory WHERE reservation_key=? LIMIT 1)`, g.RequestKey, g.RequestKey).Scan(&uncertain)
	return e == nil && !uncertain
}

// CompactBrokerGrantBatch retains every identity, hash, terminal state and audit
// entry. Only canonical payload duplication is compressed, atomically. A batch
// containing unused, active, ambiguous or merely old work is never compacted.
func (s *SQLiteStore) CompactBrokerGrantBatch(ctx context.Context, id string) (BrokerArchiveResult, error) {
	if !bindingFingerprint(id) {
		return BrokerArchiveResult{}, ErrBrokerGrant
	}
	var result BrokerArchiveResult
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if e := validateBrokerArchiveMetadata(tx); e != nil {
			return e
		}
		var raw, hash string
		if e := tx.QueryRow("SELECT payload,payload_hash FROM broker_grant_batches WHERE batch_id=?", id).Scan(&raw, &hash); e != nil {
			return e
		}
		if raw == "" {
			_, r, e := archivedBrokerBatch(tx, id)
			result = r
			result.Replay = true
			return e
		}
		b, e := decodeBrokerBatch(raw, hash, id)
		if e != nil {
			return e
		}
		// All normal accounting/history checks run before changing payload storage.
		if _, e := sqliteBrokerGrants(tx); e != nil {
			return e
		}
		for i := range b.Grants {
			g, e := lookupBrokerGrant(tx, brokerGrantID(id, i))
			if e != nil || !brokerGrantTerminal(tx, g) {
				return ErrBrokerGrant
			}
		}
		var compressed bytes.Buffer
		z, e := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
		if e != nil {
			return e
		}
		if _, e = z.Write([]byte(raw)); e != nil {
			return e
		}
		if e = z.Close(); e != nil {
			return e
		}
		if compressed.Len() > 2<<20 {
			return ErrBrokerGrant
		}
		if e := sqliteAudit(tx, "broker_grant_batch_archived", id, s.now()); e != nil {
			return e
		}
		var sequence int64
		if e := tx.QueryRow("SELECT max(sequence) FROM audit").Scan(&sequence); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO broker_grant_archives VALUES(?,?,?,?,?,?)", id, compressed.Bytes(), hash, len(raw), len(b.Grants), sequence); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE broker_grants SET payload='' WHERE batch_id=?", id); e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE broker_grant_batches SET payload='' WHERE batch_id=?", id); e != nil {
			return e
		}
		_, result, e = archivedBrokerBatch(tx, id)
		return e
	})
	if e != nil {
		return BrokerArchiveResult{}, e
	}
	return result, nil
}

// CompactBrokerGrantHistory handles a bounded page of eligible batches. Eligibility
// is re-proven inside each immediate transaction; concurrent redemption wins or
// blocks compaction, never permits a charge to disappear.
func (s *SQLiteStore) CompactBrokerGrantHistory(ctx context.Context, limit int) ([]BrokerArchiveResult, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrBrokerGrant
	}
	var ids []string
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		if e := validateBrokerArchiveMetadata(tx); e != nil {
			return e
		}
		rows, e := tx.Query(`SELECT b.batch_id FROM broker_grant_batches b WHERE b.payload!='' AND NOT EXISTS(SELECT 1 FROM broker_grants g WHERE g.batch_id=b.batch_id AND (g.state='unused' OR (g.state='consumed' AND NOT EXISTS(SELECT 1 FROM cancellations c WHERE c.request_key=g.request_key) AND NOT EXISTS(SELECT 1 FROM releases r WHERE r.request_key=g.request_key)))) AND NOT EXISTS(SELECT 1 FROM broker_grants g WHERE g.batch_id=b.batch_id AND (EXISTS(SELECT 1 FROM dispatches d WHERE d.reservation_key=g.request_key AND d.state IN ('intent','submitted','unknown')) OR EXISTS(SELECT 1 FROM inventory i WHERE i.reservation_key=g.request_key))) ORDER BY b.sequence LIMIT ?`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return nil, e
	}
	out := []BrokerArchiveResult{}
	for _, id := range ids {
		r, e := s.CompactBrokerGrantBatch(ctx, id)
		if e != nil {
			return out, e
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *SQLiteStore) checkBrokerGrantArchives(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		if e := validateBrokerArchiveMetadata(tx); e != nil {
			return e
		}
		// Page identities to bound memory even when lifetime history is very large.
		after := ""
		for {
			rows, e := tx.Query("SELECT batch_id FROM broker_grant_archives WHERE batch_id>? ORDER BY batch_id LIMIT 100", after)
			if e != nil {
				return e
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				ids = append(ids, id)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if len(ids) == 0 {
				return nil
			}
			for _, id := range ids {
				b, _, e := archivedBrokerBatch(tx, id)
				if e != nil {
					return e
				}
				grants, e := brokerBatchGrantRows(tx, b)
				if e != nil {
					return e
				}
				for _, g := range grants {
					if !brokerGrantTerminal(tx, g) {
						return ErrBrokerGrant
					}
				}
				after = id
			}
		}
	})
}

func loadBrokerBatch(tx *sql.Tx, id string) (BrokerGrantBatch, error) {
	var raw, hash string
	if e := tx.QueryRow("SELECT payload,payload_hash FROM broker_grant_batches WHERE batch_id=?", id).Scan(&raw, &hash); e != nil {
		return BrokerGrantBatch{}, e
	}
	if raw != "" {
		return decodeBrokerBatch(raw, hash, id)
	}
	b, _, e := archivedBrokerBatch(tx, id)
	return b, e
}

// Reconstruct at most one manifest page without repeated decompression per grant.
func brokerBatchGrantRows(tx *sql.Tx, b BrokerGrantBatch) ([]BrokerGrant, error) {
	out := make([]BrokerGrant, len(b.Grants))
	indices := map[string]int{}
	for i, g := range b.Grants {
		out[i] = BrokerGrant{ID: brokerGrantID(b.ID, i), Spec: g}
		indices[out[i].ID] = i
	}
	rows, e := tx.Query("SELECT grant_id,payload,payload_hash,state,COALESCE(request_key,'') FROM broker_grants WHERE batch_id=? ORDER BY grant_id LIMIT 1001", b.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, raw, hash, state, key string
		if e = rows.Scan(&id, &raw, &hash, &state, &key); e != nil {
			return nil, e
		}
		i, ok := indices[id]
		if !ok {
			return nil, ErrBrokerGrant
		}
		g := &out[i]
		canonical, digest, e := grantHash(g.Spec)
		if e != nil || digest != hash || (raw != "" && raw != canonical) || (state == "consumed") != (key != "") || (raw == "" && state == "unused") {
			return nil, ErrBrokerGrant
		}
		g.State = state
		g.RequestKey = key
		count++
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if count != len(out) {
		return nil, ErrBrokerGrant
	}
	return out, nil
}
