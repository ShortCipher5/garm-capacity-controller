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

package runner

import (
	"context"
	"fmt"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm/auth"
	"github.com/cloudbase/garm/params"
)

// GetOfflinePoolSettings returns the offline settings of a pool and their
// current revision. It is part of the experimental, opt-in offline pool
// settings API.
func (r *Runner) GetOfflinePoolSettings(ctx context.Context, entity params.ForgeEntity, poolID string) (params.OfflinePoolSettings, params.OfflinePoolSettingsRevision, error) {
	if !auth.IsAdmin(ctx) {
		return params.OfflinePoolSettings{}, params.OfflinePoolSettingsRevision{}, runnerErrors.ErrUnauthorized
	}

	settings, rev, err := r.store.GetOfflinePoolSettings(ctx, entity, poolID)
	if err != nil {
		return params.OfflinePoolSettings{}, params.OfflinePoolSettingsRevision{}, fmt.Errorf("error fetching offline pool settings: %w", err)
	}
	return settings, rev, nil
}

// UpdateOfflinePoolSettings classifies, applies and receipts one
// experimental PATCH request, guarded by the caller's admin rights. It is
// part of the experimental, opt-in offline pool settings API.
func (r *Runner) UpdateOfflinePoolSettings(ctx context.Context, entity params.ForgeEntity, poolID string, req params.OfflinePoolSettingsUpdateRequest) (params.OfflinePoolSettingsUpdateResult, error) {
	if !auth.IsAdmin(ctx) {
		return params.OfflinePoolSettingsUpdateResult{}, runnerErrors.ErrUnauthorized
	}

	result, err := r.store.UpdateOfflinePoolSettings(ctx, entity, poolID, req)
	if err != nil {
		return params.OfflinePoolSettingsUpdateResult{}, fmt.Errorf("error updating offline pool settings: %w", err)
	}
	return result, nil
}
