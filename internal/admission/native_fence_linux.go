package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"reflect"
	"time"
)

const sqliteNativeFenceSchema = `
CREATE TABLE native_root_fences (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_runner_bindings(permit_id),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TRIGGER native_root_fence_immutable BEFORE UPDATE ON native_root_fences BEGIN SELECT RAISE(ABORT,'root fence immutable'); END;
CREATE TRIGGER native_root_fence_permanent BEFORE DELETE ON native_root_fences BEGIN SELECT RAISE(ABORT,'root fence permanent'); END;
PRAGMA user_version=24;
`

// NativeRootFence is historical root readback joined to an authentic terminal
// job, not a permission to delete, an assertion of containment or a lease.
type NativeRootFence struct {
	Journal                  nativeidentity.ProtectedJournalRecord
	OwnerServerLeaf          string
	ObservedAt               time.Time
	JobID, RunID, RunAttempt int64
	JobConclusion            string
	Qualification            string
}

func nativeRootFence(tx *sql.Tx, permit string) (NativeRootFence, error) {
	var v NativeRootFence
	var raw, hash string
	var sequence int64
	if e := tx.QueryRow("SELECT payload,payload_hash,record_sequence FROM native_root_fences WHERE permit_id=?", permit).Scan(&raw, &hash, &sequence); e != nil {
		return v, e
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &v) != nil {
		return v, ErrNativeJob
	}
	canonical, digest, e := nativeJSON(v)
	if e != nil || canonical != raw || digest != hash || !nativeAuditMatches(tx, sequence, "native_root_fence_observed", permit) {
		return v, ErrNativeJob
	}
	binding, e := nativeRunnerBinding(tx, permit)
	if e != nil {
		return v, e
	}
	env, eh, e := nativeEnvelopeRecord(tx, permit)
	if e != nil {
		return v, e
	}
	domain, e := nativeDomainRecord(tx, env.DomainBindingSequence)
	if e != nil {
		return v, e
	}
	start, e := nativeOperationByStep(tx, permit, NativeStart)
	if e != nil {
		return v, e
	}
	job, e := nativeJobRecord(tx, permit, "completed")
	if e != nil {
		return v, e
	}
	r := v.Journal
	if r.PermitID != permit || r.EnvelopeDigest != eh || r.ResourceID != binding.ResourceID || r.DomainID != env.DomainID || r.PolicyDigest != env.PolicyDigest || r.NativeName != env.NativeName || r.ImageFingerprint != env.Execution.Fingerprint || !bindingFingerprint(r.InitSHA256) || r.State != "process_tree_exited_unqualified" || !r.GuestNetworkClosed || r.ExitCode < 0 || r.ExitCode > 255 || r.RecordAt.IsZero() || r.RecordAt.Before(start.ClaimedAt) || v.ObservedAt.Before(r.RecordAt) || v.OwnerServerLeaf != domain.Spec.OwnerServerLeaf || v.Qualification != "local_network_closed_containment_unqualified" || v.JobID != job.Observation.JobID || v.RunID != job.Observation.RunID || v.RunAttempt != job.Observation.RunAttempt || v.JobConclusion != job.Observation.Conclusion {
		return v, ErrNativeJob
	}
	return v, nil
}
func (s *SQLiteStore) RecordNativeRootFence(ctx context.Context, observation nativeidentity.ProtectedJournalObservation, policy nativeapi.Policy) error {
	if !observation.Valid() || !observation.Record().GuestNetworkClosed || observation.Record().State != "process_tree_exited_unqualified" {
		return ErrNativeJob
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		joined, e := s.correlateProtectedJournal(tx, observation, policy)
		if e != nil {
			return e
		}
		v := NativeRootFence{Journal: observation.Record(), OwnerServerLeaf: observation.OwnerServerLeaf(), ObservedAt: observation.ObservedAt(), JobID: joined.JobID, RunID: joined.RunID, RunAttempt: joined.RunAttempt, JobConclusion: joined.JobConclusion, Qualification: "local_network_closed_containment_unqualified"}
		old, e := nativeRootFence(tx, v.Journal.PermitID)
		if e == nil {
			copy := v
			copy.ObservedAt = old.ObservedAt
			if !reflect.DeepEqual(copy, old) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var previous bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_root_fence_observed' AND request_key=?)", v.Journal.PermitID).Scan(&previous); e != nil {
			return e
		}
		if previous {
			return ErrNativeJob
		}
		raw, hash, e := nativeJSON(v)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_root_fence_observed", v.Journal.PermitID, s.now())
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO native_root_fences(permit_id,payload,payload_hash,record_sequence)VALUES(?,?,?,?)", v.Journal.PermitID, raw, hash, seq); e != nil {
			return e
		}
		_, e = nativeRootFence(tx, v.Journal.PermitID)
		return e
	})
}
func (s *SQLiteStore) LookupNativeRootFence(ctx context.Context, permit string) (NativeRootFence, error) {
	var v NativeRootFence
	e := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; v, e = nativeRootFence(tx, permit); return e })
	return v, e
}
func (s *SQLiteStore) checkNativeRootFences(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var bad int
		if e := tx.QueryRow("SELECT count(*) FROM audit a WHERE a.kind='native_root_fence_observed' AND NOT EXISTS(SELECT 1 FROM native_root_fences f WHERE f.record_sequence=a.sequence)").Scan(&bad); e != nil {
			return e
		}
		if bad != 0 {
			return ErrNativeJob
		}
		rows, e := tx.Query("SELECT permit_id FROM native_root_fences")
		if e != nil {
			return e
		}
		var ids []string
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
		for _, id := range ids {
			if _, e = nativeRootFence(tx, id); e != nil {
				return e
			}
		}
		return nil
	})
}
