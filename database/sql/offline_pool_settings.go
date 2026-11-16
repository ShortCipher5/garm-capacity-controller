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

// ensureOfflinePoolSettingsSchema lazily creates the experimental schema.
// It is a no-op once the table exists. This function is the only place
// where the experimental schema is created; it is only reachable through
// the experimental store methods, which in turn are only reachable when
// the experimental.enable_offline_pool_settings config flag is enabled.
func (s *sqlDatabase) ensureOfflinePoolSettingsSchema() error {
	if s.conn.Migrator().HasTable(&OfflinePoolSettingsRevision{}) {
		return nil
	}
	if err := s.conn.AutoMigrate(&OfflinePoolSettingsRevision{}); err != nil {
		return fmt.Errorf("error creating offline pool settings schema: %w", err)
	}
	return nil
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
// The membership check, revision check, update and revision advance happen
// in one transaction; any failure (including a lost revision race detected
// at advance time) rolls everything back, leaving both the pool settings
// and the revision byte-for-byte unchanged.
func (s *sqlDatabase) UpdateOfflinePoolSettings(_ context.Context, entity params.ForgeEntity, poolID string, expected params.OfflinePoolSettingsRevision, patch params.OfflinePoolSettingsPatch) (params.OfflinePoolSettings, params.OfflinePoolSettingsRevision, error) {
	var settings params.OfflinePoolSettings
	var newRev params.OfflinePoolSettingsRevision

	if !patch.HasChanges() {
		return settings, newRev, runnerErrors.NewBadRequestError("empty patch")
	}

	if err := s.ensureOfflinePoolSettingsSchema(); err != nil {
		return settings, newRev, err
	}

	err := s.conn.Transaction(func(tx *gorm.DB) error {
		pool, err := s.getOfflineSettingsPool(tx, entity, poolID)
		if err != nil {
			return err
		}

		revRow, err := s.getOrCreateOfflineRevision(tx, pool.ID)
		if err != nil {
			return err
		}
		current := params.OfflinePoolSettingsRevision{Epoch: revRow.Epoch, Seq: revRow.Seq}
		if current != expected {
			return &common.OfflinePoolSettingsStaleRevisionError{Current: current}
		}

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
			return fmt.Errorf("min_idle_runners (%d) cannot exceed max_runners (%d): %w", newMinIdleRunners, newMaxRunners, runnerErrors.ErrUnprocessable)
		}

		// A no-op patch must not advance the revision.
		if newPriority == pool.Priority && newMaxRunners == pool.MaxRunners && newMinIdleRunners == pool.MinIdleRunners {
			settings = params.OfflinePoolSettings{
				Priority:       newPriority,
				MaxRunners:     newMaxRunners,
				MinIdleRunners: newMinIdleRunners,
			}
			newRev = current
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
		// revision and let the transaction rollback undo the pool update.
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
			return &common.OfflinePoolSettingsStaleRevisionError{
				Current: params.OfflinePoolSettingsRevision{Epoch: fresh.Epoch, Seq: fresh.Seq},
			}
		}

		settings = params.OfflinePoolSettings{
			Priority:       newPriority,
			MaxRunners:     newMaxRunners,
			MinIdleRunners: newMinIdleRunners,
		}
		newRev = params.OfflinePoolSettingsRevision{Epoch: current.Epoch, Seq: current.Seq + 1}
		return nil
	})
	if err != nil {
		return params.OfflinePoolSettings{}, params.OfflinePoolSettingsRevision{}, err
	}
	return settings, newRev, nil
}
