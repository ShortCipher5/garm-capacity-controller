package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
)

var ErrNativeJob = errors.New("native job lineage unavailable; retain charge")

// NativeRunnerBinding records authentic stock identity and original payload
// against the exact native incarnation. It grants no acceptance/retirement right.
type NativeRunnerBinding struct {
	ProfileSequence                                              int64
	PermitID, EnvelopeDigest, ResourceID, DomainID, PolicyDigest string
	Runner                                                       stockjit.Record
	Instance                                                     stockidentity.InstanceFacts
	StockSourceIdentity                                          string
	BoundAt                                                      time.Time
}
type NativeJobRecord struct {
	PermitID    string
	Observation demandrepair.JobLifecycle
}

const sqliteNativeJobSchema = `
CREATE TABLE native_runner_bindings (
 permit_id TEXT PRIMARY KEY NOT NULL REFERENCES native_bootstrap_bindings(permit_id),
 runner_key TEXT UNIQUE NOT NULL,
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence)
) STRICT;
CREATE TABLE native_job_observations (
 permit_id TEXT NOT NULL REFERENCES native_runner_bindings(permit_id),
 state TEXT NOT NULL CHECK(state IN ('in_progress','completed')),
 payload TEXT NOT NULL CHECK(length(payload)<=16384),payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),record_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),PRIMARY KEY(permit_id,state)
) STRICT;
CREATE TRIGGER native_runner_binding_immutable BEFORE UPDATE ON native_runner_bindings BEGIN SELECT RAISE(ABORT,'runner binding immutable'); END;
CREATE TRIGGER native_runner_binding_permanent BEFORE DELETE ON native_runner_bindings BEGIN SELECT RAISE(ABORT,'runner binding permanent'); END;
CREATE TRIGGER native_job_observation_immutable BEFORE UPDATE ON native_job_observations BEGIN SELECT RAISE(ABORT,'job observation immutable'); END;
CREATE TRIGGER native_job_observation_permanent BEFORE DELETE ON native_job_observations BEGIN SELECT RAISE(ABORT,'job observation permanent'); END;
PRAGMA user_version=23;
`

func runnerKey(r stockjit.Record) string {
	raw, _ := json.Marshal([]any{r.GitHubURL, r.RunnerID})
	return protectedboot.Hash(raw)
}
func nativeRunnerBinding(tx *sql.Tx, permit string) (NativeRunnerBinding, error) {
	var v NativeRunnerBinding
	var raw, hash, key string
	var seq int64
	if e := tx.QueryRow("SELECT payload,payload_hash,runner_key,record_sequence FROM native_runner_bindings WHERE permit_id=?", permit).Scan(&raw, &hash, &key, &seq); e != nil {
		return v, e
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &v) != nil {
		return v, ErrNativeJob
	}
	encoded, digest, e := nativeJSON(v)
	if e != nil || encoded != raw || digest != hash || v.PermitID != permit || key != runnerKey(v.Runner) || !nativeAuditMatches(tx, seq, "native_runner_bound", permit) {
		return v, ErrNativeJob
	}
	env, eh, e := nativeEnvelopeRecord(tx, permit)
	if e != nil {
		return v, e
	}
	boot, _, e := nativeBootstrapPlan(tx, permit)
	if e != nil {
		return v, e
	}
	if v.EnvelopeDigest != eh || v.ResourceID != boot.ResourceID || v.DomainID != env.DomainID || v.PolicyDigest != env.PolicyDigest || v.Runner.ControllerID != env.Controller || v.Runner.PoolID != env.Pool || v.Runner.Name != env.NativeName || v.Runner.RunnerID <= 0 || v.Instance.AgentID != v.Runner.RunnerID || v.Instance.Name != env.NativeName || v.Instance.Pool != env.Pool || v.Instance.ID == "" || v.Instance.Repository == "" || !bindingFingerprint(v.StockSourceIdentity) || v.BoundAt.IsZero() || v.Runner.ObservedAt.IsZero() || v.BoundAt.Before(v.Runner.ObservedAt) {
		return v, ErrNativeJob
	}

	plan, e := executionPlanRecord(tx, env.RequestKey)
	if e != nil {
		return v, e
	}
	var originRaw string
	if e = tx.QueryRow("SELECT payload FROM stock_provider_origins WHERE request_key=?", env.RequestKey).Scan(&originRaw); e != nil {
		return v, e
	}
	var origin stockProviderOrigin
	if json.Unmarshal([]byte(originRaw), &origin) != nil || checkStockProviderOrigin(tx, plan, &origin) != nil || origin.Authority.ProviderName != v.Instance.Provider {
		return v, ErrNativeJob
	}
	if _, e := sqliteBrokerPoolProfiles(tx); e != nil {
		return v, e
	}
	var profileRaw string
	if e = tx.QueryRow("SELECT payload FROM broker_pool_profiles WHERE record_sequence=?", v.ProfileSequence).Scan(&profileRaw); e != nil {
		return v, e
	}
	var profile BrokerPoolProfile
	if json.Unmarshal([]byte(profileRaw), &profile) != nil || !runnerProfileMatches(profile, v, env, origin.Authority) {
		return v, ErrNativeJob
	}
	return v, nil
}
func runnerProfileMatches(p BrokerPoolProfile, b NativeRunnerBinding, env NativeExecutionEnvelope, authority StockProviderAuthority) bool {
	execution := env.Execution
	execution.Name = ""
	if p.Sequence != b.ProfileSequence || p.Pool != env.Pool || p.SourceIdentity != b.StockSourceIdentity || p.Repository != b.Instance.Repository || p.Authority != authority || p.Resources != env.Resources || !reflect.DeepEqual(p.Execution, execution) {
		return false
	}
	for _, d := range p.Domains {
		if d.Sequence == env.DomainBindingSequence && d.HostBindingSequence == env.HostBindingSequence && d.Host == env.Host && d.DomainID == env.DomainID && d.PolicyDigest == env.PolicyDigest {
			return true
		}
	}
	return false
}

// BindNativeRunner accepts only the opaque trusted producer. It revalidates the
// original compiled native body and exact derived file hashes; name equality is
// never sufficient. This optional correlation does not modify START authority.
func (s *SQLiteStore) BindNativeRunner(ctx context.Context, permit string, source stockjit.ProtectedBootstrap, policy nativeapi.Policy) (NativeRunnerBinding, error) {
	if !source.Valid() {
		return NativeRunnerBinding{}, ErrNativeJob
	}
	payload, e := protectedboot.Decode([]byte(source.Payload()))
	if e != nil {
		return NativeRunnerBinding{}, ErrNativeJob
	}
	var out NativeRunnerBinding
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		env, eh, e := nativeEnvelopeRecord(tx, permit)
		if e != nil {
			return e
		}
		boot, _, e := nativeBootstrapPlan(tx, permit)
		if e != nil {
			return e
		}
		_, ready, epoch, _, e := sqliteMetadata(tx)
		if e != nil || !ready || epoch != env.ReconciliationEpoch {
			return ErrFence
		}
		image, ok := policy.ProtectedImage(env.Pool)
		if !ok || policy.Digest() != env.PolicyDigest || !reflect.DeepEqual(image, payload.Image) {
			return ErrNativeJob
		}
		a := env.Execution
		r := env.Resources
		compiled, e := policy.Compile(nativeapi.Allocation{Controller: env.Controller, Host: env.Host, Pool: env.Pool, Name: env.NativeName, RequestKey: env.RequestKey, CPU: r.CPU, RAM: r.RAM, Disk: r.Disk, Runners: r.Runners, Flavor: a.Flavor, Image: a.Image, Fingerprint: a.Fingerprint, Type: a.Type, Arch: a.Arch, Profiles: a.Profiles}, source.Payload())
		if e != nil || compiled.Digest() != env.BodyDigest {
			return ErrNativeJob
		}
		files, e := protectedboot.Materialize(payload, protectedboot.Binding{DomainID: env.DomainID, PolicyDigest: env.PolicyDigest, ResourceID: boot.ResourceID, ImageFingerprint: env.Execution.Fingerprint})
		if e != nil || files[0].SHA256 != boot.LaunchSHA256 || files[1].SHA256 != boot.JITSHA256 {
			return ErrNativeJob
		}
		runner := source.Observation().Record()
		instance := source.Instance().Facts()
		if runner.ControllerID != env.Controller || runner.PoolID != env.Pool || runner.Name != env.NativeName || instance.Name != env.NativeName || instance.Pool != env.Pool || instance.AgentID != runner.RunnerID {
			return ErrNativeJob
		}
		profiles, e := sqliteBrokerPoolProfiles(tx)
		if e != nil {
			return e
		}
		profile, ok := profiles[env.Pool]
		if !ok {
			return ErrNativeJob
		}
		out = NativeRunnerBinding{profile.Sequence, permit, eh, boot.ResourceID, env.DomainID, env.PolicyDigest, runner, instance, source.Instance().SourceIdentity(), s.now().UTC()}
		old, e := nativeRunnerBinding(tx, permit)
		if e == nil {
			out.BoundAt = old.BoundAt
			if !reflect.DeepEqual(out, old) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var historical bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind='native_runner_bound' AND request_key=?)", permit).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeJob
		}
		raw, hash, e := nativeJSON(out)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_runner_bound", permit, s.now())
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO native_runner_bindings(permit_id,runner_key,payload,payload_hash,record_sequence)VALUES(?,?,?,?,?)", permit, runnerKey(runner), raw, hash, seq)
		if e != nil {
			return e
		}
		_, e = nativeRunnerBinding(tx, permit)
		return e
	})
	if e != nil {
		return NativeRunnerBinding{}, e
	}
	return out, nil
}
func sameJob(a, b demandrepair.JobLifecycle) bool {
	return a.Repository == b.Repository && a.SourceIdentity == b.SourceIdentity && a.RunnerID == b.RunnerID && a.RunnerName == b.RunnerName && a.JobID == b.JobID && a.RunID == b.RunID && a.RunAttempt == b.RunAttempt && a.StartedAt.Equal(b.StartedAt)
}
func nativeJobRecord(tx *sql.Tx, permit, state string) (NativeJobRecord, error) {
	var v NativeJobRecord
	var raw, hash string
	var seq int64
	if e := tx.QueryRow("SELECT payload,payload_hash,record_sequence FROM native_job_observations WHERE permit_id=? AND state=?", permit, state).Scan(&raw, &hash, &seq); e != nil {
		return v, e
	}
	if json.Unmarshal([]byte(raw), &v) != nil || len(raw) > 16384 {
		return v, ErrNativeJob
	}
	encoded, digest, e := nativeJSON(v)
	if e != nil || encoded != raw || digest != hash || v.PermitID != permit || v.Observation.Status != state || !nativeAuditMatches(tx, seq, "native_job_"+state, permit) {
		return v, ErrNativeJob
	}
	binding, e := nativeRunnerBinding(tx, permit)
	if e != nil {
		return v, e
	}
	o := v.Observation
	if o.JobID <= 0 || o.RunID <= 0 || o.RunAttempt <= 0 || o.RunnerID != binding.Runner.RunnerID || o.RunnerName != binding.Runner.Name || o.Repository.GarmID != binding.Instance.Repository || "https://"+o.Repository.Host+"/"+o.Repository.Owner+"/"+o.Repository.Name != binding.Runner.GitHubURL || !bindingFingerprint(o.SourceIdentity) || o.ObservedAt.IsZero() || o.StartedAt.IsZero() || o.ObservedAt.Before(o.StartedAt) {
		return v, ErrNativeJob
	}
	start, e := nativeOperationByStep(tx, permit, NativeStart)
	if e != nil {
		return v, e
	}
	receipt, e := nativeReceiptRecord(tx, start.ID, "succeeded")
	if e != nil || receipt.Record.ResourceID != binding.ResourceID || o.StartedAt.Before(start.ClaimedAt) {
		return v, ErrNativeJob
	}
	if state == "completed" {
		if o.CompletedAt == nil || o.CompletedAt.Before(o.StartedAt) || o.CompletedAt.After(o.ObservedAt) || o.Conclusion == "" {
			return v, ErrNativeJob
		}
	} else if state != "in_progress" || o.CompletedAt != nil || o.Conclusion != "" {
		return v, ErrNativeJob
	}
	return v, nil
}

// RecordNativeJob observes terminal GitHub state, not job-safe drain. Missing or
// contradictory observations never delete history or change capacity accounting.
func (s *SQLiteStore) RecordNativeJob(ctx context.Context, permit string, observation demandrepair.JobLifecycleObservation) error {
	if !observation.Valid() {
		return ErrNativeJob
	}
	o := observation.Record()
	o.Runner = stockjit.Record{} // source identity lives in immutable binding, not caller adornment
	return s.transaction(ctx, func(tx *sql.Tx) error {
		binding, e := nativeRunnerBinding(tx, permit)
		if e != nil {
			return e
		}
		env, _, e := nativeEnvelopeRecord(tx, permit)
		if e != nil {
			return e
		}
		_, ready, epoch, _, e := sqliteMetadata(tx)
		if e != nil || !ready || epoch != env.ReconciliationEpoch {
			return ErrFence
		}
		if o.ObservedAt.After(s.now()) || s.now().Sub(o.ObservedAt) > time.Minute || o.RunnerID != binding.Runner.RunnerID || o.RunnerName != binding.Runner.Name {
			return ErrNativeJob
		}
		for _, state := range []string{"in_progress", "completed"} {
			old, e := nativeJobRecord(tx, permit, state)
			if e == nil {
				if !sameJob(old.Observation, o) {
					return ErrConflict
				}
				if state == o.Status {
					copy := o
					copy.ObservedAt = old.Observation.ObservedAt
					if !reflect.DeepEqual(copy, old.Observation) {
						return ErrConflict
					}
					return nil
				}
				if state == "completed" {
					return ErrConflict
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		var historical bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM audit WHERE kind=? AND request_key=?)", "native_job_"+o.Status, permit).Scan(&historical); e != nil {
			return e
		}
		if historical {
			return ErrNativeJob
		}
		v := NativeJobRecord{permit, o}
		raw, hash, e := nativeJSON(v)
		if e != nil {
			return e
		}
		seq, e := nativeAuditSequence(tx, "native_job_"+o.Status, permit, s.now())
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO native_job_observations(permit_id,state,payload,payload_hash,record_sequence)VALUES(?,?,?,?,?)", permit, o.Status, raw, hash, seq); e != nil {
			return e
		}
		_, e = nativeJobRecord(tx, permit, o.Status)
		return e
	})
}
func (s *SQLiteStore) checkNativeJobs(ctx context.Context) error {
	return s.readTransaction(ctx, func(tx *sql.Tx) error {
		var bad int
		if e := tx.QueryRow(`SELECT count(*) FROM audit a WHERE (a.kind='native_runner_bound' AND NOT EXISTS(SELECT 1 FROM native_runner_bindings b WHERE b.record_sequence=a.sequence)) OR (a.kind IN ('native_job_in_progress','native_job_completed') AND NOT EXISTS(SELECT 1 FROM native_job_observations j WHERE j.record_sequence=a.sequence))`).Scan(&bad); e != nil {
			return e
		}
		if bad != 0 {
			return ErrNativeJob
		}
		if e := tx.QueryRow("SELECT count(*) FROM native_job_observations j LEFT JOIN native_runner_bindings b ON b.permit_id=j.permit_id WHERE b.permit_id IS NULL").Scan(&bad); e != nil {
			return e
		}
		if bad != 0 {
			return ErrNativeJob
		}
		rows, e := tx.Query("SELECT permit_id FROM native_runner_bindings")
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
			if _, e = nativeRunnerBinding(tx, id); e != nil {
				return e
			}
			var previous *demandrepair.JobLifecycle
			for _, state := range []string{"in_progress", "completed"} {
				v, e := nativeJobRecord(tx, id, state)
				if errors.Is(e, sql.ErrNoRows) {
					continue
				}
				if e != nil {
					return e
				}
				if previous != nil && !sameJob(*previous, v.Observation) {
					return ErrNativeJob
				}
				copy := v.Observation
				previous = &copy
			}
		}
		return nil
	})
}

// LookupNativeRunnerBinding retrieves immutable authenticated lineage for a
// restart-safe scoped observer. It does not recreate credentials or authority.
func (s *SQLiteStore) LookupNativeRunnerBinding(ctx context.Context, permit string) (NativeRunnerBinding, error) {
	var out NativeRunnerBinding
	e := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; out, e = nativeRunnerBinding(tx, permit); return e })
	return out, e
}
func (s *SQLiteStore) NativeJobHistory(ctx context.Context, permit string) ([]NativeJobRecord, error) {
	out := []NativeJobRecord{}
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		if _, e := nativeRunnerBinding(tx, permit); e != nil {
			return e
		}
		for _, state := range []string{"in_progress", "completed"} {
			v, e := nativeJobRecord(tx, permit, state)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
