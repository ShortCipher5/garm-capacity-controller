package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// ErrReuseEvidence identifies teardown evidence that is missing, unverified,
// bound to another charge or delivered after a scope/profile/authority change.
// Every failure fails closed: the reservation keeps its full charge.
var ErrReuseEvidence = errors.New("verified teardown evidence unavailable, conflicting or out of scope")

const sqliteVerifiedReuseSchema = `
CREATE TABLE IF NOT EXISTS verified_reuse (
 request_key TEXT PRIMARY KEY NOT NULL REFERENCES reservations(request_key),
 cpu INTEGER NOT NULL CHECK(cpu IN (0,1)),
 ram INTEGER NOT NULL CHECK(ram IN (0,1)),
 runners INTEGER NOT NULL CHECK(runners IN (0,1)),
 disk INTEGER NOT NULL CHECK(disk IN (0,1)),
 evidence_digest TEXT NOT NULL CHECK(length(evidence_digest)=64),
 record BLOB NOT NULL CHECK(length(record)>0 AND length(record)<=16384),
 decided_at TEXT NOT NULL
) STRICT;
CREATE TRIGGER IF NOT EXISTS verified_reuse_immutable_update BEFORE UPDATE ON verified_reuse
BEGIN SELECT RAISE(ABORT,'verified reuse decisions are immutable'); END;
CREATE TRIGGER IF NOT EXISTS verified_reuse_immutable_delete BEFORE DELETE ON verified_reuse
BEGIN SELECT RAISE(ABORT,'verified reuse decisions are immutable'); END;
PRAGMA user_version=30;
`

// VerifiedReuse is a durable, dimension-scoped reuse decision. Releasable
// names the dimensions qualified by positive matching terminal evidence;
// Remaining is the still-charged footprint. It is not a lease and never
// re-authorizes a new claim for the same request identity.
type VerifiedReuse struct {
	Request        Request          `json:"request"`
	Releasable     reuse.Dimensions `json:"releasable"`
	Remaining      Resources        `json:"remaining"`
	EvidenceDigest string           `json:"evidence_digest"`
	DecidedAt      time.Time        `json:"decided_at"`
	Replay         bool             `json:"replay"`
}

func verifiedReuseRecord(tx *sql.Tx, key string) (VerifiedReuse, error) {
	var result VerifiedReuse
	var cpu, ram, runners, disk int
	var at string
	var record []byte
	err := tx.QueryRow("SELECT cpu,ram,runners,disk,evidence_digest,record,decided_at FROM verified_reuse WHERE request_key=?", key).Scan(&cpu, &ram, &runners, &disk, &result.EvidenceDigest, &record, &at)
	if err != nil {
		return VerifiedReuse{}, err
	}
	if len(record) == 0 || len(record) > 16384 || evidenceDigest(record) != result.EvidenceDigest {
		return VerifiedReuse{}, ErrReuseEvidence
	}
	result.Releasable = reuse.Dimensions{CPU: cpu == 1, RAM: ram == 1, Runners: runners == 1, Disk: disk == 1}
	result.DecidedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil || result.DecidedAt.IsZero() {
		return VerifiedReuse{}, ErrReuseEvidence
	}
	return result, nil
}

// sqliteReuseFreed returns the dimensions already qualified per reservation.
func sqliteReuseFreed(tx *sql.Tx) (map[string]reuse.Dimensions, error) {
	rows, err := tx.Query("SELECT request_key,cpu,ram,runners,disk FROM verified_reuse")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]reuse.Dimensions{}
	for rows.Next() {
		var key string
		var cpu, ram, runners, disk int
		if err := rows.Scan(&key, &cpu, &ram, &runners, &disk); err != nil {
			return nil, err
		}
		result[key] = reuse.Dimensions{CPU: cpu == 1, RAM: ram == 1, Runners: runners == 1, Disk: disk == 1}
	}
	return result, rows.Err()
}

// anchoredCharge rebuilds the original charge from durable ledger history:
// the exact reservation and the authenticated original-create incarnation.
// The stock registration and the applicable exact volumes arrive with the
// evidence and are cross-checked into the incarnation and request.
func anchoredCharge(req Request, creation CreateEvidence, stock reuse.StockBinding, volumes []reuse.VolumeCharge) reuse.ChargeIdentity {
	subject := creation.Observation.Identity.Subject
	return reuse.ChargeIdentity{
		Ref: reuse.ChargeRef{
			Key:     req.Key,
			Pool:    req.Pool,
			Host:    req.Host,
			CPU:     req.Resources.CPU,
			RAM:     req.Resources.RAM,
			Disk:    req.Resources.Disk,
			Runners: req.Resources.Runners,
		},
		Stock: stock,
		Provider: reuse.ProviderBinding{
			BackendKeyFingerprint: subject.BackendKeyFingerprint,
			OriginFingerprint:     subject.OriginFingerprint,
			ScopeFingerprint:      subject.ScopeFingerprint,
			ServerName:            subject.ServerName,
			ServerVersion:         subject.ServerVersion,
			Project:               subject.Project,
			Member:                "",
		},
		Incarnation: reuse.IncarnationBinding{
			Name:       subject.Name,
			UUID:       subject.UUID,
			Generation: subject.Generation,
			CreatedAt:  subject.CreatedAt,
		},
		Volumes: volumes,
	}
}

// ChargeFromCreation validates the original-charge anchor: the exact
// reservation, the authenticated original-create incarnation and the stock
// registration must all bind. Volumes are the applicable exact charged
// allocation: today the ledger independently records exactly the instance
// root identity, so each volume must be that recorded root volume and its
// binding digest must recompute from the enrolled scope and exact name.
// Allocation beyond the recorded root identity is a documented follow-up and
// never validates by convenience.
func ChargeFromCreation(req Request, creation CreateEvidence, stock reuse.StockBinding, volumes []reuse.VolumeCharge) (reuse.ChargeIdentity, error) {
	subject := creation.Observation.Identity.Subject
	if creation.Request != req || subject.ReservationKey != req.Key || !validCreateEvidence(creation) {
		return reuse.ChargeIdentity{}, ErrReuseEvidence
	}
	if stock.ControllerID != subject.ControllerID || stock.PoolID != subject.PoolID || stock.RunnerName != subject.Name {
		return reuse.ChargeIdentity{}, ErrReuseEvidence
	}
	for _, volume := range volumes {
		if volume.Name != subject.Name || volume.BindingDigest != reuse.VolumeBindingDigest(subject.ScopeFingerprint, subject.Name) {
			return reuse.ChargeIdentity{}, ErrReuseEvidence
		}
	}
	charge := anchoredCharge(req, creation, stock, volumes)
	if !charge.Valid() {
		return reuse.ChargeIdentity{}, ErrReuseEvidence
	}
	return charge, nil
}

// validateRecordedAllocations binds delivered evidence to the independently
// recorded identities before any dimension is credited: the runner identity
// recorded at dispatch, the exact volume identity derived from the enrolled
// scope, and the immutable incarnation fingerprint pinned by the durable
// retirement intent. Every mismatch keeps the full charge.
func validateRecordedAllocations(tx *sql.Tx, req Request, creation CreateEvidence, intent RetirementIntent, charge reuse.ChargeIdentity, evidence reuse.TerminalEvidence) error {
	subject := creation.Observation.Identity.Subject
	if intent.Creation.Observation.Identity.SubjectFingerprint != creation.Observation.Identity.SubjectFingerprint {
		return ErrReuseEvidence
	}
	dispatch, _, err := dispatchRecord(tx, req.Key)
	if err != nil {
		return err
	}
	if dispatch.ResourceID == "" || dispatch.ResourceID != subject.Name || dispatch.ResourceID != charge.Incarnation.Name || dispatch.ResourceID != evidence.Charge.Incarnation.Name {
		return ErrReuseEvidence
	}
	if charge.Incarnation.Name != subject.Name || charge.Incarnation.UUID != subject.UUID ||
		charge.Incarnation.Generation != subject.Generation || !charge.Incarnation.CreatedAt.Equal(subject.CreatedAt) ||
		intent.Creation.Observation.Identity.Subject != subject {
		return ErrReuseEvidence
	}
	if evidence.Charge.Ref.Key != req.Key || evidence.Charge.Ref.Pool != req.Pool || evidence.Charge.Ref.Host != req.Host {
		return ErrReuseEvidence
	}
	if evidence.Teardown != nil && evidence.Teardown.Valid() &&
		evidence.Teardown.Identity.SubjectFingerprint != intent.Creation.Observation.Identity.SubjectFingerprint {
		return ErrReuseEvidence
	}
	for _, volume := range charge.Volumes {
		if volume.Name != dispatch.ResourceID || volume.BindingDigest != reuse.VolumeBindingDigest(subject.ScopeFingerprint, volume.Name) {
			return ErrReuseEvidence
		}
	}
	return nil
}

// remainingCharge subtracts qualified dimensions from a charge. An
// unverified dimension always keeps its full charge.
func remainingCharge(resources Resources, freed reuse.Dimensions) Resources {
	remaining := resources
	if freed.CPU {
		remaining.CPU = 0
	}
	if freed.RAM {
		remaining.RAM = 0
	}
	if freed.Runners {
		remaining.Runners = 0
	}
	if freed.Disk {
		remaining.Disk = 0
	}
	return remaining
}

// ReleaseVerified retires exactly the dimensions a delivered terminal
// evidence bundle qualifies, atomically with the ledger in ONE BEGIN
// IMMEDIATE transaction. The charge is anchored in durable history: the
// reservation, the original-create incarnation and the recorded retirement
// intent. The durable deleting mark must precede evidence of the external
// delete. Qualified CPU/RAM/count without verified disk reuse keeps the disk
// charged; any missing, conflicting, distinct-charge or post-change evidence
// keeps the whole charge. Duplicate delivery and restart replay the first
// immutable decision; a different decision for the same identity is refused.
func (s *SQLiteStore) ReleaseVerified(ctx context.Context, req Request, evidence reuse.TerminalEvidence) (VerifiedReuse, error) {
	if !validRequest(req) || !evidence.Valid() {
		return VerifiedReuse{}, ErrReuseEvidence
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) == 0 || len(encoded) > 16384 {
		return VerifiedReuse{}, ErrReuseEvidence
	}
	evidenceDigest := evidenceDigest(encoded)
	var result VerifiedReuse
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if !ready || epoch == int64(1<<63-1) {
			return ErrReconciliation
		}
		if !sqliteObservationsFresh(config, s.now()) {
			if _, err := tx.Exec("UPDATE metadata SET ready=0,nonce='' WHERE id=1"); err != nil {
				return err
			}
			return ErrReconciliation
		}
		if release, err := releaseRecord(tx, req.Key); err == nil {
			if release.AbsenceDigest != evidenceDigest {
				return ErrDenied
			}
			result = VerifiedReuse{Request: release.Request, Releasable: reuse.Dimensions{CPU: true, RAM: true, Runners: true, Disk: true}, Remaining: Resources{}, EvidenceDigest: release.AbsenceDigest, DecidedAt: release.ReleasedAt, Replay: true}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if decision, err := verifiedReuseRecord(tx, req.Key); err == nil {
			if decision.EvidenceDigest != evidenceDigest {
				return ErrDenied
			}
			decision.Request = req
			decision.Replay = true
			result = decision
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[req.Key]
		if !exists {
			return ErrDenied
		}
		if reservation.Request != req {
			return ErrConflict
		}
		if reservation.State != "deleting" {
			return ErrDenied
		}
		intent, err := retirementIntent(tx, req.Key)
		if err != nil {
			return err
		}
		if intent.Request != req {
			return ErrConflict
		}
		creation, err := lookupCreateEvidence(tx, req.Key)
		if err != nil {
			return err
		}
		charge := anchoredCharge(req, creation, evidence.Charge.Stock, evidence.Charge.Volumes)
		if err := validateRecordedAllocations(tx, req, creation, intent, charge, evidence); err != nil {
			return err
		}
		verdict := reuse.Assess(charge, []reuse.TerminalEvidence{evidence})
		if !verdict.Releasable.Any() {
			// Every missing, conflicting, distinct-charge or unverified
			// dimension keeps the full charge; nothing is written.
			return ErrReuseEvidence
		}
		var linked int
		if err := tx.QueryRow("SELECT COUNT(*) FROM inventory WHERE reservation_key=?", req.Key).Scan(&linked); err != nil {
			return err
		}
		if linked != 1 {
			return ErrConflict
		}
		if err := verifyReuseAuthority(tx, config, req, charge); err != nil {
			return err
		}
		removed, err := tx.Exec("DELETE FROM inventory WHERE reservation_key=?", req.Key)
		if err != nil {
			return err
		}
		count, err := removed.RowsAffected()
		if err != nil || count != 1 {
			return ErrConflict
		}
		at := s.now().UTC()
		if at.IsZero() {
			return ErrConflict
		}
		if verdict.Releasable.All() {
			if _, err := tx.Exec("INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)", req.Key, req.Pool, req.Host, req.Resources.CPU, req.Resources.RAM, req.Resources.Disk, req.Resources.Runners, evidenceDigest, encoded, at.Format(time.RFC3339Nano)); err != nil {
				return err
			}
			if err := sqliteAudit(tx, "released_verified_teardown", req.Key, at); err != nil {
				return err
			}
			result = VerifiedReuse{Request: req, Releasable: verdict.Releasable, Remaining: Resources{}, EvidenceDigest: evidenceDigest, DecidedAt: at}
			return nil
		}
		freed := verdict.Releasable
		if _, err := tx.Exec("INSERT INTO verified_reuse(request_key,cpu,ram,runners,disk,evidence_digest,record,decided_at) VALUES(?,?,?,?,?,?,?,?)", req.Key, boolInt(freed.CPU), boolInt(freed.RAM), boolInt(freed.Runners), boolInt(freed.Disk), evidenceDigest, encoded, at.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "verified_reuse_partial", req.Key, at); err != nil {
			return err
		}
		result = VerifiedReuse{Request: req, Releasable: freed, Remaining: remainingCharge(req.Resources, freed), EvidenceDigest: evidenceDigest, DecidedAt: at}
		return nil
	})
	if err != nil {
		return VerifiedReuse{}, err
	}
	return result, nil
}

// verifyReuseAuthority refuses release after a scope, profile or authority
// change: the pool must still exist with this host in scope, and the host's
// durable backend binding must still be the exact enrolled daemon the
// incarnation was observed on. A rebound or unbound host invalidates the
// evidence context instead of freeing capacity by convenience.
func verifyReuseAuthority(tx *sql.Tx, config Config, req Request, charge reuse.ChargeIdentity) error {
	pool, ok := config.Pools[req.Pool]
	if !ok || !contains(pool.Hosts, req.Host) {
		return ErrReuseEvidence
	}
	var serverKey, origin, scope, version string
	err := tx.QueryRow("SELECT server_key,origin,scope,server_version FROM host_bindings WHERE host=?", req.Host).Scan(&serverKey, &origin, &scope, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReuseEvidence
	}
	if err != nil {
		return err
	}
	if serverKey != charge.Provider.BackendKeyFingerprint || origin != charge.Provider.OriginFingerprint ||
		scope != charge.Provider.ScopeFingerprint || version != charge.Provider.ServerVersion {
		return ErrReuseEvidence
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
