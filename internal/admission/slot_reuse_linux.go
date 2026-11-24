package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Safe runner-slot reuse: evidence contract, classifier and the guarded
// accounting transition for one exact retired runner. A slot is released
// once, only when independently sourced evidence proves the accepted work is
// settled, the runner and its charged storage are gone, and no pending work
// or old writer can recreate it. Missing or contradictory evidence keeps the
// charge. Nothing here authorizes a STOP/DELETE/deregister call, forks the
// runner lifecycle, or treats a matching readback as proof.
const SlotReuseEvidenceVersion = "slot-reuse-evidence/v1"

// SlotReuseMaxEvidenceAge bounds the age of every fact group at commit. Old
// evidence is not refreshed by replay, restart or re-presentation.
const SlotReuseMaxEvidenceAge = 10 * time.Minute

var ErrSlotReuseEvidence = errors.New("slot reuse evidence incomplete, stale or conflicting")

// SlotReuseStatus is the sanitized hold category. It is diagnostic and never
// by itself permission to release; only the guarded transactional transition
// moves accounting, and only on candidate_evidence_complete.
type SlotReuseStatus string

const (
	SlotReuseUnsupportedSource          SlotReuseStatus = "unsupported_source"
	SlotReuseAwaitingJobSettlement      SlotReuseStatus = "awaiting_job_settlement"
	SlotReuseAwaitingTeardownEvidence   SlotReuseStatus = "awaiting_teardown_evidence"
	SlotReuseAwaitingStorageEvidence    SlotReuseStatus = "awaiting_storage_evidence"
	SlotReuseFenceUnproven              SlotReuseStatus = "fence_unproven"
	SlotReuseStaleOrConflictingEvidence SlotReuseStatus = "stale_or_conflicting_evidence"
	SlotReuseCandidateComplete          SlotReuseStatus = "candidate_evidence_complete"
)

// SlotReuseSourceClass names one independently qualified evidence producer.
// The production registry is deliberately empty: no pinned stock GARM,
// provider or Incus interface currently proves original teardown identity or
// independent closure, so every caller-constructed assertion classifies as
// unsupported_source and the stock scope stays charged. Tests register
// fixture classes to exercise the contract offline.
type SlotReuseSourceClass string

var qualifiedSlotReuseSources = map[SlotReuseSourceClass]bool{}

// SlotReuseIdentity fixes the exact original allocation: enrolled source and
// controller, original provider route, immutable daemon/project/instance
// incarnation, the actual allocated resource scope and the ledger epoch of
// the hold. A current object with the same display name is never a match.
type SlotReuseIdentity struct {
	Source     SlotReuseSourceClass `json:"source"`
	RequestKey string               `json:"request_key"`
	Pool       string               `json:"pool"`
	Host       string               `json:"host"`
	CPU        int64                `json:"cpu_millis"`
	RAM        int64                `json:"ram_mib"`
	Disk       int64                `json:"disk_mib"`
	Controller string               `json:"controller"`
	SourceID   string               `json:"source_id"`
	Route      string               `json:"route_digest"`
	Daemon     string               `json:"daemon_fingerprint"`
	Project    string               `json:"project_fingerprint"`
	Instance   string               `json:"instance_uuid"`
	Generation string               `json:"instance_generation"`
	ResourceID string               `json:"resource_id"`
	Epoch      int64                `json:"epoch"`
	ObservedAt time.Time            `json:"observed_at"`
}

// SlotReuseSettlement proves trustworthy terminal state for the exact
// accepted work and independent closure of new assignment and old
// start/retry/reconnect work. Idle, offline, stop-requested, a terminal
// webhook alone or a missing registration never satisfy it. The first
// candidate scope is naturally exhausted one-job ephemeral runners only.
type SlotReuseSettlement struct {
	Source           SlotReuseSourceClass `json:"source"`
	RequestKey       string               `json:"request_key"`
	JobTerminal      bool                 `json:"job_terminal"`
	PostJobFinished  bool                 `json:"post_job_finished"`
	AssignmentClosed bool                 `json:"assignment_closed"`
	StartRetryClosed bool                 `json:"start_retry_closed"`
	OneJobEphemeral  bool                 `json:"one_job_ephemeral"`
	ObservedAt       time.Time            `json:"observed_at"`
}

// SlotReuseTeardown proves the original physical incarnation and its owned
// storage are gone and held processes settled. Unknown storage or visibility
// stays unknown; host-free bytes are never proof of reclamation.
type SlotReuseTeardown struct {
	Source           SlotReuseSourceClass `json:"source"`
	RequestKey       string               `json:"request_key"`
	InstanceAbsent   bool                 `json:"instance_absent"`
	StorageReleased  bool                 `json:"storage_released"`
	ProcessesSettled bool                 `json:"processes_settled"`
	ObservedAt       time.Time            `json:"observed_at"`
}

// SlotReuseFence is the independent fence: outstanding accepted work and old
// writers can no longer change the conclusion. It is bound to the exact
// identity digest and epoch; a current match, matching readback or process
// restart never supplies it.
type SlotReuseFence struct {
	Source         SlotReuseSourceClass `json:"source"`
	RequestKey     string               `json:"request_key"`
	IdentityDigest string               `json:"identity_digest"`
	Epoch          int64                `json:"epoch"`
	WriterFenced   bool                 `json:"writer_fenced"`
	ObservedAt     time.Time            `json:"observed_at"`
}

// SlotReuseEvidence is the source-versioned contract offered for one exact
// retired runner. Production never accepts a caller-constructed assertion:
// every fact group must come from a qualified source class.
type SlotReuseEvidence struct {
	Version    string              `json:"version"`
	Identity   SlotReuseIdentity   `json:"identity"`
	Settlement SlotReuseSettlement `json:"settlement"`
	Teardown   SlotReuseTeardown   `json:"teardown"`
	Fence      SlotReuseFence      `json:"fence"`
}

// SlotReuseVerdict is the classifier result: a sanitized category plus a
// fixed detail phrase, never caller-provided free text.
type SlotReuseVerdict struct {
	Status SlotReuseStatus `json:"status"`
	Detail string          `json:"detail,omitempty"`
}

func slotReuseIdentityDigest(identity SlotReuseIdentity) string {
	identity.ObservedAt = time.Time{}
	raw, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validSlotReuseTime(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= SlotReuseMaxEvidenceAge
}

func validSlotReuseIdentity(v SlotReuseIdentity, hold Request) bool {
	return bindingFingerprint(v.RequestKey) && v.RequestKey == hold.Key && v.Pool == hold.Pool && v.Host == hold.Host &&
		v.CPU == hold.Resources.CPU && v.RAM == hold.Resources.RAM && v.Disk == hold.Resources.Disk &&
		bindingName(v.Controller) && bindingName(v.SourceID) && bindingFingerprint(v.Route) &&
		bindingFingerprint(v.Daemon) && bindingFingerprint(v.Project) &&
		validOperationID(v.Instance) && validOperationID(v.Generation) && bindingName(v.ResourceID) &&
		v.Epoch >= 0 && !v.ObservedAt.IsZero()
}

// ClassifySlotReuse checks the four evidence boundaries in a fixed order and
// returns the sanitized hold category. It is pure: it changes no accounting
// and its complete verdict is a candidate, not a release.
func ClassifySlotReuse(hold Request, evidence SlotReuseEvidence, now time.Time) SlotReuseVerdict {
	verdict := func(status SlotReuseStatus, detail string) SlotReuseVerdict {
		return SlotReuseVerdict{Status: status, Detail: detail}
	}
	if now.IsZero() || !validRequest(hold) {
		return verdict(SlotReuseStaleOrConflictingEvidence, "hold_or_clock_invalid")
	}
	if evidence.Version != SlotReuseEvidenceVersion {
		return verdict(SlotReuseUnsupportedSource, "evidence_version")
	}
	for _, source := range []SlotReuseSourceClass{evidence.Identity.Source, evidence.Settlement.Source, evidence.Teardown.Source, evidence.Fence.Source} {
		if !qualifiedSlotReuseSources[source] {
			return verdict(SlotReuseUnsupportedSource, "source_class_unqualified")
		}
	}
	identity := evidence.Identity
	if !validSlotReuseIdentity(identity, hold) {
		return verdict(SlotReuseStaleOrConflictingEvidence, "original_identity_mismatch")
	}
	settlement, teardown, fence := evidence.Settlement, evidence.Teardown, evidence.Fence
	if settlement.RequestKey != hold.Key || teardown.RequestKey != hold.Key || fence.RequestKey != hold.Key {
		return verdict(SlotReuseStaleOrConflictingEvidence, "fact_group_scope_mismatch")
	}
	if !validSlotReuseTime(identity.ObservedAt, now) || !validSlotReuseTime(settlement.ObservedAt, now) ||
		!validSlotReuseTime(teardown.ObservedAt, now) || !validSlotReuseTime(fence.ObservedAt, now) {
		return verdict(SlotReuseStaleOrConflictingEvidence, "evidence_stale_or_future")
	}
	// The incarnation must be identified before its settlement, teardown and
	// fence are observed, and the fence must be established last: it closes
	// the conclusion the other groups support.
	if identity.ObservedAt.After(settlement.ObservedAt) || identity.ObservedAt.After(teardown.ObservedAt) ||
		fence.ObservedAt.Before(settlement.ObservedAt) || fence.ObservedAt.Before(teardown.ObservedAt) ||
		fence.ObservedAt.Before(identity.ObservedAt) {
		return verdict(SlotReuseStaleOrConflictingEvidence, "evidence_out_of_order")
	}
	if !settlement.OneJobEphemeral {
		return verdict(SlotReuseUnsupportedSource, "runner_class_out_of_scope")
	}
	if !settlement.JobTerminal || !settlement.PostJobFinished || !settlement.AssignmentClosed || !settlement.StartRetryClosed {
		return verdict(SlotReuseAwaitingJobSettlement, "accepted_work_not_settled")
	}
	if !teardown.InstanceAbsent || !teardown.ProcessesSettled {
		return verdict(SlotReuseAwaitingTeardownEvidence, "original_teardown_unproven")
	}
	if !teardown.StorageReleased {
		return verdict(SlotReuseAwaitingStorageEvidence, "charged_storage_unproven")
	}
	if !fence.WriterFenced || fence.IdentityDigest == "" {
		return verdict(SlotReuseFenceUnproven, "independent_fence_missing")
	}
	// A fence bound to a different identity or epoch contradicts the evidence
	// instead of merely missing it.
	if fence.IdentityDigest != slotReuseIdentityDigest(identity) || fence.Epoch != identity.Epoch {
		return verdict(SlotReuseStaleOrConflictingEvidence, "fence_binding_mismatch")
	}
	return verdict(SlotReuseCandidateComplete, "")
}

// SlotReuseDecision reports the accounting outcome for one exact hold. A
// replay of identical evidence returns the prior decision with Replay set
// and zero additional credit; it never releases twice.
type SlotReuseDecision struct {
	Request        Request         `json:"request"`
	Status         SlotReuseStatus `json:"status"`
	EvidenceDigest string          `json:"evidence_digest,omitempty"`
	ReleasedAt     time.Time       `json:"released_at,omitempty"`
	Replay         bool            `json:"replay"`
}

// ReleaseRetiredSlot is the guarded accounting transition for the exact
// retired runner. One BEGIN IMMEDIATE transaction rechecks readiness,
// observation freshness, the reconciliation epoch, the exact hold in state
// 'deleting', the linked charged inventory row and any prior tombstone, then
// removes the charge and writes the immutable release tombstone plus audit.
// Identical evidence replay returns the prior decision with zero additional
// credit; a mismatched replay, a reappearing or conflicting inventory row,
// or any missing or contradictory evidence preserves the charge. The
// all-or-nothing scope releases exactly the resources the hold charged.
func (s *SQLiteStore) ReleaseRetiredSlot(ctx context.Context, hold Request, evidence SlotReuseEvidence) (SlotReuseDecision, error) {
	decision := SlotReuseDecision{Request: hold}
	verdict := ClassifySlotReuse(hold, evidence, s.now())
	if verdict.Status != SlotReuseCandidateComplete {
		decision.Status = verdict.Status
		return decision, ErrSlotReuseEvidence
	}
	record := evidence
	record.Identity.ObservedAt = record.Identity.ObservedAt.UTC()
	record.Settlement.ObservedAt = record.Settlement.ObservedAt.UTC()
	record.Teardown.ObservedAt = record.Teardown.ObservedAt.UTC()
	record.Fence.ObservedAt = record.Fence.ObservedAt.UTC()
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) == 0 || len(encoded) > 16384 {
		decision.Status = SlotReuseStaleOrConflictingEvidence
		return decision, ErrSlotReuseEvidence
	}
	digest := evidenceDigest(encoded)
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
		// The durable tombstone answers replay first: an identical replay
		// returns the prior decision with zero additional credit even after a
		// later reconciliation advanced the epoch. A mismatched replay is
		// contradictory: the tombstone stands and the conflict is reported,
		// never merged.
		if prior, err := releaseRecord(tx, hold.Key); err == nil {
			if prior.AbsenceDigest != digest {
				decision.Status = SlotReuseStaleOrConflictingEvidence
				return ErrConflict
			}
			decision.Status = SlotReuseCandidateComplete
			decision.EvidenceDigest = prior.AbsenceDigest
			decision.ReleasedAt = prior.ReleasedAt
			decision.Replay = true
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Recheck the evidence scope and reconciliation epoch at commit: a
		// prevalidated recommendation is not authority.
		if evidence.Identity.Epoch != epoch || evidence.Fence.Epoch != epoch {
			decision.Status = SlotReuseStaleOrConflictingEvidence
			return ErrSlotReuseEvidence
		}
		reservations, err := sqliteReservations(tx)
		if err != nil {
			return err
		}
		reservation, exists := reservations[hold.Key]
		if !exists {
			decision.Status = SlotReuseStaleOrConflictingEvidence
			return ErrSlotReuseEvidence
		}
		if reservation.Request != hold {
			decision.Status = SlotReuseStaleOrConflictingEvidence
			return ErrConflict
		}
		if reservation.State != "deleting" {
			decision.Status = SlotReuseAwaitingTeardownEvidence
			return ErrSlotReuseEvidence
		}
		// The charged inventory row must still be the exact original
		// incarnation. A missing row is not absence proof, and a reappearing
		// or renamed resource under the same key is a conflict: both keep
		// the charge.
		rows, err := sqliteInventory(tx)
		if err != nil {
			return err
		}
		linked := 0
		for _, item := range rows {
			if item.ReservationKey == hold.Key {
				linked++
				if item.ID != evidence.Identity.ResourceID || item.Host != hold.Host || item.Pool != hold.Pool ||
					item.Resources.CPU != hold.Resources.CPU || item.Resources.RAM != hold.Resources.RAM {
					decision.Status = SlotReuseStaleOrConflictingEvidence
					return ErrConflict
				}
			}
		}
		if linked != 1 {
			decision.Status = SlotReuseAwaitingStorageEvidence
			return ErrSlotReuseEvidence
		}
		removed, err := tx.Exec("DELETE FROM inventory WHERE reservation_key=?", hold.Key)
		if err != nil {
			return err
		}
		if count, err := removed.RowsAffected(); err != nil || count != 1 {
			decision.Status = SlotReuseStaleOrConflictingEvidence
			return ErrConflict
		}
		at := s.now().UTC()
		if at.IsZero() {
			return ErrConflict
		}
		if _, err := tx.Exec("INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
			hold.Key, hold.Pool, hold.Host, hold.Resources.CPU, hold.Resources.RAM, hold.Resources.Disk, hold.Resources.Runners, digest, encoded, at.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if err := sqliteAudit(tx, "slot_reuse_released", hold.Key, at); err != nil {
			return err
		}
		decision.Status = SlotReuseCandidateComplete
		decision.EvidenceDigest = digest
		decision.ReleasedAt = at
		return nil
	})
	if err != nil {
		return decision, err
	}
	return decision, nil
}
