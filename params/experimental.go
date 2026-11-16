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

package params

import "fmt"

// OfflinePoolSettingsMaxValue is the maximum accepted value for priority and
// min_idle_runners in the experimental offline pool settings API.
const OfflinePoolSettingsMaxValue = 1000000

// OfflinePoolSettingsMinMaxRunners is the minimum accepted value for
// max_runners in the experimental offline pool settings API.
const OfflinePoolSettingsMinMaxRunners = 1

// OfflinePoolSettings is the wire document exchanged by the experimental
// offline pool settings API. It is a strict subset of the pool record,
// covering only the fields that endpoint manages.
type OfflinePoolSettings struct {
	// Priority is the scheduling priority of the pool.
	Priority uint `json:"priority"`
	// MaxRunners is the maximum number of runners the pool may have.
	MaxRunners uint `json:"max_runners"`
	// MinIdleRunners is the minimum number of idle runners the pool
	// should keep around.
	MinIdleRunners uint `json:"min_idle_runners"`
}

// OfflinePoolSettingsPatch is a partial update for the offline pool
// settings. Nil fields are left unchanged.
type OfflinePoolSettingsPatch struct {
	Priority       *uint
	MaxRunners     *uint
	MinIdleRunners *uint
}

// HasChanges returns true if at least one field is set.
func (p OfflinePoolSettingsPatch) HasChanges() bool {
	return p.Priority != nil || p.MaxRunners != nil || p.MinIdleRunners != nil
}

// OfflinePoolSettingsRevision identifies one point in the revision history
// of a pool's offline settings. It maps 1:1 to the strong ETag quoted value
// "<epoch>:<seq>" exposed by the API.
type OfflinePoolSettingsRevision struct {
	// Epoch is set once, when the revision row is lazily created for a
	// pool, and never changes afterwards.
	Epoch uint64 `json:"epoch"`
	// Seq is a per-pool monotonic counter. It is incremented every time an
	// offline settings update commits successfully.
	Seq uint64 `json:"seq"`
}

// String returns the unquoted "<epoch>:<seq>" representation.
func (r OfflinePoolSettingsRevision) String() string {
	return fmt.Sprintf("%d:%d", r.Epoch, r.Seq)
}

// ETag returns the strong, quoted ETag representation of the revision.
func (r OfflinePoolSettingsRevision) ETag() string {
	return fmt.Sprintf("%q", r.String())
}
