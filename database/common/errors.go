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

package common

import (
	"fmt"

	"github.com/cloudbase/garm/params"
)

var (
	ErrProducerClosed            = fmt.Errorf("producer is closed")
	ErrProducerTimeoutErr        = fmt.Errorf("producer timeout error")
	ErrProducerAlreadyRegistered = fmt.Errorf("producer already registered")
	ErrConsumerAlreadyRegistered = fmt.Errorf("consumer already registered")
	ErrWatcherAlreadyStarted     = fmt.Errorf("watcher already started")
	ErrWatcherNotInitialized     = fmt.Errorf("watcher not initialized")
	ErrInvalidOperationType      = fmt.Errorf("invalid operation")
	ErrInvalidEntityType         = fmt.Errorf("invalid entity type")
	ErrNoFiltersProvided         = fmt.Errorf("no filters provided")
	// ErrPoolNotMemberOfEntity is returned by the experimental offline pool
	// settings store when the requested pool exists but does not belong to
	// the entity in the request path.
	ErrPoolNotMemberOfEntity = fmt.Errorf("pool is not a member of the path entity")
	// ErrOfflinePoolSettingsStaleRevision is matched (via Is) by
	// *OfflinePoolSettingsStaleRevisionError.
	ErrOfflinePoolSettingsStaleRevision = fmt.Errorf("stale offline pool settings revision")
	// ErrOfflinePoolSettingsRequestIDReuse is returned by the experimental
	// offline pool settings store when a client reuses an Idempotency-Key
	// with a payload hash or pool scope different from the stored receipt.
	// The original receipt is left untouched.
	ErrOfflinePoolSettingsRequestIDReuse = fmt.Errorf("request id was already used with a different payload or scope")
)

// OfflinePoolSettingsStaleRevisionError is returned by the experimental
// offline pool settings store when the caller supplied revision does not
// match the current revision of the pool settings. The current revision is
// included so callers can surface it (e.g. as an ETag on a 412 response).
type OfflinePoolSettingsStaleRevisionError struct {
	Current params.OfflinePoolSettingsRevision
}

func (e *OfflinePoolSettingsStaleRevisionError) Error() string {
	return fmt.Sprintf("stale offline pool settings revision: current %q", e.Current.String())
}

// Is matches ErrOfflinePoolSettingsStaleRevision.
func (e *OfflinePoolSettingsStaleRevisionError) Is(target error) bool {
	return target == ErrOfflinePoolSettingsStaleRevision
}
