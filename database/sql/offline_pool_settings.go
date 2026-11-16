// Copyright 2025 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm/database/common"
	"github.com/cloudbase/garm/params"
)

// OfflinePoolSettingsRevision is the per-pool revision record backing the
// strong ETags of the experimental offline pool settings API. It lives in
// its own lazily created table so that the stock schema is never touched.
type OfflinePoolSettingsRevision struct {
	// PoolID is the pool this revision belongs to.
	PoolID uuid.UUID `gorm:"type:uuid;primaryKey"`
	// Epoch is set once, when the row is created, and never changes after.
	Epoch uint64 `gorm:"not null"`
	// Seq is a per-pool monotonic counter, incremented on every committed,
	// non no-op offline settings update.
	Seq uint64 `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the table of the revision records.
func (OfflinePoolSettingsRevision) TableName() string {
	return "offline_pool_settings_revisions"
}

// OfflinePoolSettingsRequestReceipt is the durable receipt of one
// experimental PATCH request. Receipts are written in the same transaction
// as the settings effect (or alone for rejected outcomes that never touch
// the settings), which lets a client safely retry a request: an identical
// retry is answered from the stored receipt without re-applying anything.
//
// There is deliberately no garbage collection or expiration in this
// version: rows accumulate for as long as the table exists.
type OfflinePoolSettingsRequestReceipt struct {
	// RequestID is the client supplied Idempotency-Key (or a server
	// generated UUID when the client did not send one; those rows are
	// durable but not addressable for replay).
	RequestID string `gorm:"type:text;primaryKey"`
	// PoolID is the pool the request targeted.
	PoolID uuid.UUID `gorm:"type:uuid;not null"`
	// PayloadHash is the hex-encoded SHA-256 of the exact payload bytes
	// the client sent.
	PayloadHash string `gorm:"type:text;not null"`
	// PresentedETag is the quoted If-Match value the client presented, or
	// NULL when the header was malformed.
	PresentedETag *string `gorm:"column:presented_etag;type:text"`
	// Outcome is the durable classification of the request.
	Outcome string `gorm:"type:text;not null;check:outcome IN ('applied_changed','applied_noop','rejected_stale','rejected_invalid','rejected_policy','rejected_malformed')"`
	// ResultingETag is the quoted ETag observed (stale rejections) or
	// produced (applied outcomes) by the request, or NULL when there is
	// none.
	ResultingETag *string `gorm:"column:resulting_etag;type:text"`
	// CreatedAt is the time the receipt was persisted.
	CreatedAt time.Time `gorm:"not null"`
}

// TableName returns the table of the request receipts.
func (OfflinePoolSettingsRequestReceipt) TableName() string {
	return "offline_pool_settings_request_receipts"
}

// ensureOfflinePoolSettingsSchema lazily creates the experimental schema.
// It is a no-op once the tables exist. This function is the only place
// where the experimental schema is created; it is only reachable through
// the experimental store methods, which in turn are only reachable when
// the experimental.enable_offline_pool_settings config flag is enabled.
func (s *sqlDatabase) ensureOfflinePoolSettingsSchema() error {
	if s.conn.Migrator().HasTable(&OfflinePoolSettingsRevision{}) &&
		s.conn.Migrator().HasTable(&OfflinePoolSettingsRequestReceipt{}) {
		return nil
	}
	if err := s.conn.AutoMigrate(&OfflinePoolSettingsRevision{}, &OfflinePoolSettingsRequestReceipt{}); err != nil {
		return fmt.Errorf("error creating offline pool settings schema: %w", err)
	}
	return nil
}

// bumpOfflineRevisionForLegacyWrite advances the experimental offline pool
// settings revision of a pool from the stock (legacy) pool update path, so
// that a client holding a strong ETag from the experimental API observes
// stock settings writes as conflicts.
//
// The bump is deliberately best-effort and inline: it runs inside the same
// transaction as the stock settings write, but it never creates the
// experimental side table, never alters the stock schema and never fails
// the stock write. When the experimental feature was never enabled (or the
// pool has no revision row yet), the UPDATE either errors with "no such
// table" (ignored) or matches zero rows (normal).
func (s *sqlDatabase) bumpOfflineRevisionForLegacyWrite(tx *gorm.DB, poolID uuid.UUID) {
	_ = tx.Model(&OfflinePoolSettingsRevision{}).
		Where("pool_id = ?", poolID).
		Update("seq", gorm.Expr("seq + 1")).Error
}

// getOrCreateOfflineRevision returns the revision row of a pool, creating
// it on first use. Must be called inside a transaction.
func (s *sqlDatabase) getOrCreateOfflineRevision(tx *gorm.DB, poolID uuid.UUID) (OfflinePoolSettingsRevision, error) {
	var rev OfflinePoolSettingsRevision
	err := tx.Where("pool_id = ?", poolID).First(&rev).Error
	if err == nil {
		return rev, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return OfflinePoolSettingsRevision{}, fmt.Errorf("error fetching offline pool settings revision: %w", err)
	}

	rev = OfflinePoolSettingsRevision{
		PoolID: poolID,
		Epoch:  uint64(time.Now().Unix()),
	}
	// Two racing first-time users may both try to insert; only one wins,
	// the other keeps the existing row.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rev).Error; err != nil {
		return OfflinePoolSettingsRevision{}, fmt.Errorf("error creating offline pool settings revision: %w", err)
	}

	if err := tx.Where("pool_id = ?", poolID).First(&rev).Error; err != nil {
		return OfflinePoolSettingsRevision{}, fmt.Errorf("error re-fetching offline pool settings revision: %w", err)
	}
	return rev, nil
}

// getOfflineSettingsPool fetches a pool within a transaction, verifying
// membership in the path entity:
//   - pool does not exist at all -> ErrNotFound
//   - pool exists but is not a member of the path entity (including a
//     nonexistent path entity) -> ErrPoolNotMemberOfEntity
func (s *sqlDatabase) getOfflineSettingsPool(tx *gorm.DB, entity params.ForgeEntity, poolID string) (Pool, error) {
	pool, err := s.getEntityPool(tx, entity.EntityType, entity.ID, poolID)
	if err == nil {
		return pool, nil
	}
	if !errors.Is(err, runnerErrors.ErrNotFound) {
		return Pool{}, err
	}

	if _, ferr := s.getPoolByID(tx, poolID); ferr == nil {
		return Pool{}, fmt.Errorf("pool %s is not a member of %s entity %s: %w", poolID, entity.EntityType, entity.ID, common.ErrPoolNotMemberOfEntity)
	}
	return Pool{}, err
}

func offlineSettingsFromPool(pool Pool) params.OfflinePoolSettings {
	return params.OfflinePoolSettings{
		Priority:       pool.Priority,
		MaxRunners:     pool.MaxRunners,
		MinIdleRunners: pool.MinIdleRunners,
	}
}

// GetOfflinePoolSettings implements common.OfflinePoolSettingsStore.
func (s *sqlDatabase) GetOfflinePoolSettings(_ context.Context, entity params.ForgeEntity, poolID string) (params.OfflinePoolSettings, params.OfflinePoolSettingsRevision, error) {
	var settings params.OfflinePoolSettings
	var rev params.OfflinePoolSettingsRevision

	if err := s.ensureOfflinePoolSettingsSchema(); err != nil {
		return settings, rev, err
	}

	err := s.conn.Transaction(func(tx *gorm.DB) error {
		pool, err := s.getOfflineSettingsPool(tx, entity, poolID)
		if err != nil {
			return err
		}
		settings = offlineSettingsFromPool(pool)

		revRow, err := s.getOrCreateOfflineRevision(tx, pool.ID)
		if err != nil {
			return err
		}
		rev = params.OfflinePoolSettingsRevision{Epoch: revRow.Epoch, Seq: revRow.Seq}
		return nil
	})
	if err != nil {
		return params.OfflinePoolSettings{}, params.OfflinePoolSettingsRevision{}, err
	}
	return settings, rev, nil
}

// UpdateOfflinePoolSettings implements common.OfflinePoolSettingsStore.
//
// One transaction covers everything: the entity membership check (403/404,
// never receipted), the idempotency replay/id-reuse check, the revision
// check, the settings update, the conditional revision advance and the
// durable request receipt. A stale precondition or a policy rejection rolls
// back the settings write but still commits the receipt, so the rejection
// is durable and a retry can be answered from it. A lost revision race
// detected at advance time behaves like a stale rejection. Only generic
// failures (which map to 503) leave no trace at all.
//
// The revision advance remains the single conditional UPDATE also used to
// detect lost races; the stock update path never calls this method, so one
// PATCH through the experimental API advances seq exactly once.
func (s *sqlDatabase) UpdateOfflinePoolSettings(_ context.Context, entity params.ForgeEntity, poolID string, req params.OfflinePoolSettingsUpdateRequest) (params.OfflinePoolSettingsUpdateResult, error) {
	var result params.OfflinePoolSettingsUpdateResult

	if err := s.ensureOfflinePoolSettingsSchema(); err != nil {
		return result, err
	}

	// classErr is a classified, non-fatal failure (stale, policy, malformed)
	// that is reported to the caller only after its receipt committed.
	var classErr error

	txErr := s.conn.Transaction(func(tx *gorm.DB) error {
		pool, err := s.getOfflineSettingsPool(tx, entity, poolID)
		if err != nil {
			// 404/403: rejected before any write; no receipt.
			return err
		}

		receiptID := req.RequestID
		if receiptID == "" {
			// Receipts are durable for every PATCH; without a client key
			// the row simply is not addressable for replay.
			receiptID = uuid.NewString()
		}

		if req.RequestID != "" {
			stored, found, err := s.getRequestReceipt(tx, req.RequestID)
			if err != nil {
				return err
			}
			if found {
				if stored.PoolID == pool.ID && stored.PayloadHash == req.PayloadHash {
					// Safe retry: answer from the stored receipt without
					// applying or writing anything.
					result.Replayed = true
					result.Outcome = params.OfflinePoolSettingsRequestOutcome(stored.Outcome)
					if stored.ResultingETag != nil {
						result.ResultingETag = *stored.ResultingETag
					}
					return nil
				}
				// Same request id, different payload or scope: the stored
				// receipt is left untouched and no state changes.
				return fmt.Errorf("Idempotency-Key %q was already used with a different payload or pool: %w", req.RequestID, common.ErrOfflinePoolSettingsRequestIDReuse)
			}
		}

		var presentedETag *string
		if req.IfMatchErr == nil {
			presented := req.IfMatch.ETag()
			presentedETag = &presented
		}

		var outcome params.OfflinePoolSettingsRequestOutcome
		var resultingETag *string
		switch {
		case req.BodyErr != nil || req.IfMatchErr != nil || req.Patch == nil:
			outcome = params.OfflinePoolSettingsOutcomeRejectedMalformed
			classErr = req.BodyErr
			if classErr == nil {
				classErr = req.IfMatchErr
			}
			if classErr == nil {
				// Cannot happen for requests that went through the handler
				// (the strict decoder requires at least one field); kept as
				// a defensive backstop.
				classErr = runnerErrors.NewBadRequestError("empty patch")
			}
		default:
			classErr = s.applyOfflineSettingsPatch(tx, pool, req, &outcome, &resultingETag, &result)
		}
		result.Outcome = outcome

		receipt := OfflinePoolSettingsRequestReceipt{
			RequestID:     receiptID,
			PoolID:        pool.ID,
			PayloadHash:   req.PayloadHash,
			PresentedETag: presentedETag,
			Outcome:       string(outcome),
			ResultingETag: resultingETag,
			CreatedAt:     time.Now().UTC(),
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
		if res.Error != nil {
			return fmt.Errorf("error persisting offline pool settings request receipt: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// A concurrent request with the same key committed its receipt
			// first. Answer with the stored outcome when it is the same
			// logical request, else report the id reuse.
			stored, _, err := s.getRequestReceipt(tx, receiptID)
			if err != nil {
				return err
			}
			if stored.PoolID == pool.ID && stored.PayloadHash == req.PayloadHash {
				result = params.OfflinePoolSettingsUpdateResult{
					Replayed:      true,
					Outcome:       params.OfflinePoolSettingsRequestOutcome(stored.Outcome),
					ResultingETag: stringValue(stored.ResultingETag),
				}
				classErr = nil
				return nil
			}
			return fmt.Errorf("Idempotency-Key %q was already used with a different payload or pool: %w", receiptID, common.ErrOfflinePoolSettingsRequestIDReuse)
		}
		return nil
	})
	if txErr != nil {
		return params.OfflinePoolSettingsUpdateResult{}, txErr
	}
	if classErr != nil {
		return result, classErr
	}
	return result, nil
}

// getRequestReceipt fetches one request receipt by id.
func (s *sqlDatabase) getRequestReceipt(tx *gorm.DB, requestID string) (OfflinePoolSettingsRequestReceipt, bool, error) {
	var stored OfflinePoolSettingsRequestReceipt
	err := tx.Where("request_id = ?", requestID).First(&stored).Error
	if err == nil {
		return stored, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OfflinePoolSettingsRequestReceipt{}, false, nil
	}
	return OfflinePoolSettingsRequestReceipt{}, false, fmt.Errorf("error fetching offline pool settings request receipt: %w", err)
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// applyOfflineSettingsPatch applies a validated patch inside the caller's
// transaction. It records the durable outcome and resulting ETag, and, for
// applied requests, the resulting state. Classified rejections are returned
// as errors (stale, policy); the caller persists the receipt either way.
func (s *sqlDatabase) applyOfflineSettingsPatch(tx *gorm.DB, pool Pool, req params.OfflinePoolSettingsUpdateRequest, outcome *params.OfflinePoolSettingsRequestOutcome, resultingETag **string, result *params.OfflinePoolSettingsUpdateResult) error {
	revRow, err := s.getOrCreateOfflineRevision(tx, pool.ID)
	if err != nil {
		return err
	}

	current := params.OfflinePoolSettingsRevision{Epoch: revRow.Epoch, Seq: revRow.Seq}
	if current != req.IfMatch {
		currentETag := current.ETag()
		*outcome = params.OfflinePoolSettingsOutcomeRejectedStale
		*resultingETag = &currentETag
		return &common.OfflinePoolSettingsStaleRevisionError{Current: current}
	}

	patch := req.Patch
	newPriority := pool.Priority
	if patch.Priority != nil {
		newPriority = *patch.Priority
	}
	newMaxRunners := pool.MaxRunners
	if patch.MaxRunners != nil {
		newMaxRunners = *patch.MaxRunners
	}
	newMinIdleRunners := pool.MinIdleRunners
	if patch.MinIdleRunners != nil {
		newMinIdleRunners = *patch.MinIdleRunners
	}

	// The min_idle_runners <= max_runners invariant is checked against
	// the current row inside the transaction whenever either count is
	// supplied.
	if (patch.MaxRunners != nil || patch.MinIdleRunners != nil) && newMinIdleRunners > newMaxRunners {
		*outcome = params.OfflinePoolSettingsOutcomeRejectedPolicy
		return fmt.Errorf("min_idle_runners (%d) cannot exceed max_runners (%d): %w", newMinIdleRunners, newMaxRunners, runnerErrors.ErrUnprocessable)
	}

	// A no-op patch must not advance the revision.
	if newPriority == pool.Priority && newMaxRunners == pool.MaxRunners && newMinIdleRunners == pool.MinIdleRunners {
		currentETag := current.ETag()
		*outcome = params.OfflinePoolSettingsOutcomeAppliedNoop
		*resultingETag = &currentETag
		result.Settings = params.OfflinePoolSettings{
			Priority:       newPriority,
			MaxRunners:     newMaxRunners,
			MinIdleRunners: newMinIdleRunners,
		}
		result.Revision = current
		return nil
	}

	updates := map[string]interface{}{
		"priority":         newPriority,
		"max_runners":      newMaxRunners,
		"min_idle_runners": newMinIdleRunners,
	}
	if err := tx.Model(&Pool{}).Where("id = ?", pool.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("error updating pool settings: %w", err)
	}

	// Conditional revision advance. If this UPDATE matches no row, the
	// revision changed under us after the check above; report the fresh
	// revision as a stale rejection and let the transaction rollback undo
	// the pool update.
	newRev := params.OfflinePoolSettingsRevision{Epoch: current.Epoch, Seq: current.Seq + 1}
	res := tx.Model(&OfflinePoolSettingsRevision{}).
		Where("pool_id = ? AND epoch = ? AND seq = ?", pool.ID, current.Epoch, current.Seq).
		Update("seq", current.Seq+1)
	if res.Error != nil {
		return fmt.Errorf("error advancing offline pool settings revision: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		var fresh OfflinePoolSettingsRevision
		if ferr := tx.Where("pool_id = ?", pool.ID).First(&fresh).Error; ferr != nil {
			return fmt.Errorf("error fetching fresh offline pool settings revision: %w", ferr)
		}
		freshRev := params.OfflinePoolSettingsRevision{Epoch: fresh.Epoch, Seq: fresh.Seq}
		freshETag := freshRev.ETag()
		*outcome = params.OfflinePoolSettingsOutcomeRejectedStale
		*resultingETag = &freshETag
		return &common.OfflinePoolSettingsStaleRevisionError{Current: freshRev}
	}

	newRevETag := newRev.ETag()
	*outcome = params.OfflinePoolSettingsOutcomeAppliedChanged
	*resultingETag = &newRevETag
	result.Settings = params.OfflinePoolSettings{
		Priority:       newPriority,
		MaxRunners:     newMaxRunners,
		MinIdleRunners: newMinIdleRunners,
	}
	result.Revision = newRev
	return nil
}
