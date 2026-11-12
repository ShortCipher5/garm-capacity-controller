package garmmanagement

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

var ErrStagedPoolObservation = errors.New("confirmed disabled pool observation unavailable")

// DisabledPoolFacts contains only a validated confirmed creation record plus a
// fresh exact scoped source read. It has no activation/registration authority;
// callers must still resolve fixed private catalogs and perform an inactive CAS.
// Do not populate it from caller StagedPool JSON or infer JIT mode from API rows.
type DisabledPoolFacts struct {
	Protocol, ManagementIdentity, JournalLineage, RequestID, PoolID, RepositoryID, ProviderName, ForgeEndpointName string
	Generation                                                                                                     uint64
	SpecFingerprint, PoolFingerprint, Image, Flavor, OSType, OSArch                                                string
	TemplateID                                                                                                     uint64
	ShellEnabled, ExtraSpecsEmpty, Enabled                                                                         bool
	MinIdleRunners, MaxRunners                                                                                     uint64
	ObservedAt                                                                                                     time.Time
}
type disabledPoolObserver interface {
	ObserveDisabledRepoPool(context.Context, string, string, garmapi.DisabledPoolSpec) (garmapi.DisabledPoolObservation, error)
}
type confirmedStage struct {
	stage     StagedPool
	spec      garmapi.DisabledPoolSpec
	signature string
}

// confirmedStage reads and validates the LIVE bound journal and global stop
// barrier in one short transaction. No remote IO occurs while that lock is held.
func (m *Manager) confirmedStage(ctx context.Context, id string) (confirmedStage, error) {
	var result confirmedStage
	if !validUUID(id) {
		return result, ErrJournal
	}
	err := m.journal.transaction(ctx, func(tx *sql.Tx) error {
		var lineage, identity string
		var pending int
		if tx.QueryRow("SELECT lineage FROM journal_identity WHERE id=1").Scan(&lineage) != nil || lineage != m.journal.Lineage() || tx.QueryRow("SELECT identity FROM binding WHERE id=1").Scan(&identity) != nil || identity != m.remote.ManagementIdentity() {
			return ErrJournal
		}
		if tx.QueryRow("SELECT count(*) FROM intents WHERE state IN ('pending','unknown')").Scan(&pending) != nil {
			return ErrJournal
		}
		if pending != 0 {
			return ErrBlocked
		}
		var kind, target, expected, reason, hash, state, role string
		var desired, recorded []byte
		if tx.QueryRow("SELECT kind,target,expected,reason,request_hash,state,role,desired_json,result_json FROM intents WHERE request_id=?", id).Scan(&kind, &target, &expected, &reason, &hash, &state, &role, &desired, &recorded) != nil {
			return ErrJournal
		}
		if state != "confirmed" {
			return ErrBlocked
		}
		if kind != "repository" || role != "management" || !validJournalLineage(identity) || len(desired) > 65536 || len(recorded) > 8192 {
			return ErrJournal
		}
		var intent disabledPoolIntent
		if json.Unmarshal(desired, &intent) != nil || intent.Operation != "create_disabled_pool" || intent.Protocol != DisabledPoolProtocol || !bytes.Equal(encoded(intent), desired) {
			return ErrJournal
		}
		canonical, err := garmapi.CanonicalDisabledPoolSpec(intent.Spec)
		if err != nil || !bytes.Equal(encoded(canonical), encoded(intent.Spec)) {
			return ErrJournal
		}
		actual, err := requestHash(id, target, expected, kind, reason, intent)
		if err != nil || actual != hash {
			return ErrJournal
		}
		stage, err := m.decodeRecordedStage(recorded, id, target, canonical)
		if err != nil || !bytes.Equal(encoded(stage), recorded) {
			return ErrJournal
		}
		result = confirmedStage{stage: stage, spec: canonical, signature: hash + string(recorded)}
		return nil
	})
	return result, err
}

// ConfirmedDisabledPool derives target identity ONLY from a canonical confirmed
// live-journal request, then reads the exact disabled source pool. The global
// unresolved barrier is checked both before and after IO. This is an observation,
// not remote CAS: later/external writers may still act. No admission/route/budget,
// journal intent or pool allowlist is changed, and no POST/PUT/retry is performed.
func (m *Manager) ConfirmedDisabledPool(ctx context.Context, requestID string) (DisabledPoolFacts, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return DisabledPoolFacts{}, err
	}
	defer m.leave()
	observer, ok := m.remote.(disabledPoolObserver)
	if !ok {
		return DisabledPoolFacts{}, ErrStagedPoolObservation
	}
	record, err := m.confirmedStage(ctx, requestID)
	if err != nil {
		return DisabledPoolFacts{}, err
	}
	observed, err := observer.ObserveDisabledRepoPool(ctx, record.stage.RepositoryID, record.stage.PoolID, record.spec)
	if err != nil {
		return DisabledPoolFacts{}, ErrStagedPoolObservation
	}
	current, err := m.confirmedStage(ctx, requestID)
	if err != nil {
		return DisabledPoolFacts{}, err
	}
	if current.signature != record.signature {
		return DisabledPoolFacts{}, ErrJournal
	}
	pool := observed.Pool
	if pool.ID != record.stage.PoolID || pool.RepositoryID != record.stage.RepositoryID || pool.ProviderName != record.stage.ProviderName || pool.Generation != record.stage.Generation || pool.Fingerprint != record.stage.PoolFingerprint || pool.Enabled || pool.MinIdleRunners != 0 || pool.MaxRunners != record.spec.MaxRunners || observed.ForgeEndpointName == "" || observed.ObservedAt.IsZero() || ctx.Err() != nil {
		return DisabledPoolFacts{}, ErrStagedPoolObservation
	}
	return DisabledPoolFacts{Protocol: DisabledPoolProtocol, ManagementIdentity: record.stage.ManagementIdentity, JournalLineage: record.stage.JournalLineage, RequestID: requestID, PoolID: pool.ID, RepositoryID: pool.RepositoryID, ProviderName: pool.ProviderName, ForgeEndpointName: observed.ForgeEndpointName, Generation: pool.Generation, SpecFingerprint: record.stage.SpecFingerprint, PoolFingerprint: pool.Fingerprint, Image: record.spec.Image, Flavor: record.spec.Flavor, OSType: "linux", OSArch: record.spec.Arch, ExtraSpecsEmpty: true, MaxRunners: pool.MaxRunners, ObservedAt: observed.ObservedAt}, nil
}
