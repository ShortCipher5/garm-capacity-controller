package garmmanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const DisabledPoolProtocol = "native-disabled-repository-pool-v1"

// StagedPool is durable creation evidence only. A confirmed disabled readback is
// not route enrollment, admission readiness or an enable permit. Stock v0.2.1
// rejects native template-zero creation; the separate source8 patch is required.
type StagedPool struct {
	ProviderName       string `json:"provider_name"`
	Generation         uint64 `json:"generation"`
	PoolFingerprint    string `json:"pool_fingerprint,omitempty"`
	ManagementIdentity string `json:"management_identity"`
	JournalLineage     string `json:"journal_lineage"`
	Protocol           string `json:"protocol"`
	RequestID          string `json:"request_id"`
	PoolID             string `json:"pool_id"`
	RepositoryID       string `json:"repository_id"`
	SpecFingerprint    string `json:"spec_fingerprint"`
	DisabledReadback   bool   `json:"disabled_readback"`
	RouteRequired      bool   `json:"route_required"`
	RouteInstalled     bool   `json:"route_installed"`
	ActivationAllowed  bool   `json:"activation_allowed"`
}
type disabledPoolRemote interface {
	DisabledPoolCreationEnabled() bool
	PostDisabledRepoPool(context.Context, string, string, garmapi.DisabledPoolSpec) (garmapi.DisabledPoolReceipt, error)
	VerifyDisabledRepoPool(context.Context, garmapi.DisabledPoolReceipt, garmapi.DisabledPoolSpec) (garmapi.ManagedPool, error)
}
type disabledPoolIntent struct {
	Operation string                   `json:"operation"`
	Protocol  string                   `json:"protocol"`
	Spec      garmapi.DisabledPoolSpec `json:"spec"`
}

// capturePoolReceipt preserves a UUID before a subsequent GET or client timeout.
// It is not completion: the global pending slot remains occupied and audited.
func (j *Journal) capturePoolReceipt(ctx context.Context, id string, result StagedPool) error {
	if !validUUID(result.PoolID) || !validUUID(result.RepositoryID) || result.RequestID != id || result.Protocol != DisabledPoolProtocol || result.JournalLineage != j.Lineage() || !validJournalLineage(result.ManagementIdentity) || result.DisabledReadback || result.PoolFingerprint != "" || !result.RouteRequired || result.RouteInstalled || result.ActivationAllowed {
		return ErrJournal
	}
	return j.transaction(ctx, func(tx *sql.Tx) error {
		update, err := tx.Exec("UPDATE intents SET result_json=?,updated_at=? WHERE request_id=? AND state='pending'", encoded(result), timestamp(), id)
		if err != nil {
			return ErrJournal
		}
		n, err := update.RowsAffected()
		if err != nil || n != 1 {
			return ErrJournal
		}
		return audit(tx, id, "disabled_pool_uuid_received", encoded(result))
	})
}

// CreateDisabledPool is a typed experimental library foundation, not an admin
// route. Spec must come from a fixed trusted template. It NEVER enables a pool,
// changes client allowlists, installs an admission route, or retries a POST.
// Repository-kind intents target the repository subresource, explicitly recording
// operation=create_disabled_pool; this shares the existing global stop barrier.
func (m *Manager) CreateDisabledPool(ctx context.Context, requestID, repositoryID, expected string, spec garmapi.DisabledPoolSpec, reason string) (StagedPool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return StagedPool{}, err
	}
	defer m.leave()
	remote, ok := m.remote.(disabledPoolRemote)
	if !ok || !remote.DisabledPoolCreationEnabled() {
		return StagedPool{}, garmapi.ErrManagementDisabled
	}
	owned, err := garmapi.CanonicalDisabledPoolSpec(spec)
	if err != nil {
		return StagedPool{}, err
	}
	desired := disabledPoolIntent{"create_disabled_pool", DisabledPoolProtocol, owned}
	hash, err := requestHash(requestID, repositoryID, expected, "repository", reason, desired)
	if err != nil {
		return StagedPool{}, err
	}
	decodeRecorded := func(raw json.RawMessage) (StagedPool, error) {
		var result StagedPool
		if json.Unmarshal(raw, &result) != nil || result.Protocol != DisabledPoolProtocol || result.ManagementIdentity != m.remote.ManagementIdentity() || result.JournalLineage != m.journal.Lineage() || result.RequestID != requestID || result.RepositoryID != repositoryID || !validUUID(result.PoolID) || result.SpecFingerprint != garmapi.DisabledPoolSpecFingerprint(owned) || result.ProviderName != owned.ProviderName || !validJournalLineage(result.PoolFingerprint) || !result.DisabledReadback || !result.RouteRequired || result.RouteInstalled || result.ActivationAllowed {
			return StagedPool{}, ErrJournal
		}
		return result, nil
	}
	if raw, exists, err := m.journal.replay(ctx, requestID, hash); err != nil {
		return StagedPool{}, err
	} else if exists {
		return decodeRecorded(raw)
	}
	before, err := m.remote.Repository(ctx, repositoryID)
	if err != nil || before.Fingerprint != expected {
		// Another writer may have confirmed this same intent during the read. Do
		// not misreport its historical confirmation as a new preflight failure.
		if raw, exists, replayErr := m.journal.replay(ctx, requestID, hash); replayErr != nil {
			return StagedPool{}, replayErr
		} else if exists {
			return decodeRecorded(raw)
		}
		if err != nil {
			return StagedPool{}, err
		}
		return StagedPool{}, garmapi.ErrManagementConflict
	}
	intent := Intent{RequestID: requestID, Kind: "repository", Target: repositoryID, Reason: reason, Before: encoded(before), Desired: encoded(desired)}
	replayed, err := m.journal.begin(ctx, intent, hash, expected)
	if err != nil {
		return StagedPool{}, err
	}
	if replayed != nil {
		return decodeRecorded(replayed)
	}
	result := StagedPool{Protocol: DisabledPoolProtocol, ProviderName: owned.ProviderName, ManagementIdentity: m.remote.ManagementIdentity(), JournalLineage: m.journal.Lineage(), RequestID: requestID, RepositoryID: repositoryID, SpecFingerprint: garmapi.DisabledPoolSpecFingerprint(owned), RouteRequired: true}
	receipt, remoteErr := remote.PostDisabledRepoPool(ctx, repositoryID, expected, owned)
	if receipt.PoolID() != "" {
		result.PoolID = receipt.PoolID()
		persist, finish := context.WithTimeout(context.Background(), 5*time.Second)
		captureErr := m.journal.capturePoolReceipt(persist, requestID, result)
		finish()
		if captureErr != nil {
			return StagedPool{}, ErrOutcomeUnknown
		}
	}
	if remoteErr == nil {
		if result.PoolID == "" || receipt.RepositoryID() != repositoryID || receipt.SpecFingerprint() != result.SpecFingerprint {
			remoteErr = garmapi.ErrManagementOutcomeUnknown
		} else {
			var pool garmapi.ManagedPool
			pool, remoteErr = remote.VerifyDisabledRepoPool(ctx, receipt, owned)
			if remoteErr == nil && (pool.ID != result.PoolID || pool.RepositoryID != repositoryID || pool.ProviderName != owned.ProviderName || pool.Enabled || pool.MinIdleRunners != 0 || !validJournalLineage(pool.Fingerprint)) {
				remoteErr = garmapi.ErrManagementOutcomeUnknown
			}
			result.DisabledReadback = remoteErr == nil
			if remoteErr == nil {
				result.Generation = pool.Generation
				result.PoolFingerprint = pool.Fingerprint
			}
		}
	}
	state := "confirmed"
	if remoteErr != nil {
		state = "unknown"
		if result.PoolID == "" && (errors.Is(remoteErr, garmapi.ErrManagementDisabled) || errors.Is(remoteErr, garmapi.ErrManagementConflict) || errors.Is(remoteErr, garmapi.ErrManagementUnsafe)) {
			state = "rejected"
		}
	}
	persist, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	if m.journal.finish(persist, requestID, state, encoded(result)) != nil {
		return StagedPool{}, ErrOutcomeUnknown
	}
	if state == "unknown" {
		return StagedPool{}, ErrOutcomeUnknown
	}
	if remoteErr != nil {
		return StagedPool{}, remoteErr
	}
	return result, nil
}
