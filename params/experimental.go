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

// OfflinePoolSettingsRequestOutcome is the durable classification of one
// experimental PATCH request. It is persisted in the request receipts side
// table and returned verbatim when a request is answered from a stored
// receipt (idempotent replay).
type OfflinePoolSettingsRequestOutcome string

const (
	// OfflinePoolSettingsOutcomeAppliedChanged marks a request that changed
	// the pool settings and advanced the revision.
	OfflinePoolSettingsOutcomeAppliedChanged OfflinePoolSettingsRequestOutcome = "applied_changed"
	// OfflinePoolSettingsOutcomeAppliedNoop marks a request whose supplied
	// values all equaled the current values; it succeeded without advancing
	// the revision.
	OfflinePoolSettingsOutcomeAppliedNoop OfflinePoolSettingsRequestOutcome = "applied_noop"
	// OfflinePoolSettingsOutcomeRejectedStale marks a request rejected with a
	// stale precondition (HTTP 412).
	OfflinePoolSettingsOutcomeRejectedStale OfflinePoolSettingsRequestOutcome = "rejected_stale"
	// OfflinePoolSettingsOutcomeRejectedInvalid is reserved for payload
	// rejections that are not syntax-level malformed (e.g. semantically
	// invalid content). No code path produces it in this version; it only
	// exists in the database CHECK constraint so the outcome set is stable.
	OfflinePoolSettingsOutcomeRejectedInvalid OfflinePoolSettingsRequestOutcome = "rejected_invalid"
	// OfflinePoolSettingsOutcomeRejectedPolicy marks a request rejected by
	// the coupled-limits policy (min_idle_runners > max_runners, HTTP 422).
	OfflinePoolSettingsOutcomeRejectedPolicy OfflinePoolSettingsRequestOutcome = "rejected_policy"
	// OfflinePoolSettingsOutcomeRejectedMalformed marks a request rejected
	// with HTTP 400 because its body or If-Match precondition was malformed.
	OfflinePoolSettingsOutcomeRejectedMalformed OfflinePoolSettingsRequestOutcome = "rejected_malformed"
)

// OfflinePoolSettingsUpdateRequest carries everything the store needs to
// classify, apply and receipt one experimental PATCH request. The handler
// performs all HTTP-level validation (it owns the detailed rejection
// errors) and passes the results in; the store replays, applies and
// persists the durable receipt in a single transaction.
type OfflinePoolSettingsUpdateRequest struct {
	// RequestID is the raw Idempotency-Key header value; empty when the
	// client did not send one.
	RequestID string
	// PayloadHash identifies the exact payload bytes the client sent
	// (hex-encoded SHA-256).
	PayloadHash string
	// IfMatchErr carries the If-Match parsing failure, if any. A missing
	// If-Match header never reaches the store: the handler rejects it with
	// 428 before calling in, and a 428 never persists a receipt.
	IfMatchErr error
	// IfMatch is the parsed precondition. Only meaningful when IfMatchErr
	// is nil.
	IfMatch OfflinePoolSettingsRevision
	// BodyErr carries the strict body decoding failure, if any.
	BodyErr error
	// Patch is the decoded patch; nil when BodyErr is set.
	Patch *OfflinePoolSettingsPatch
}

// OfflinePoolSettingsUpdateResult describes how one experimental PATCH
// request was answered.
type OfflinePoolSettingsUpdateResult struct {
	// Replayed is true when the request was answered from a stored receipt:
	// nothing was applied and nothing was written.
	Replayed bool
	// Outcome is the durable classification of the request. It is set for
	// replayed requests (mirroring the stored row) and for applied
	// requests; for fresh rejections it mirrors the returned error.
	Outcome OfflinePoolSettingsRequestOutcome
	// Settings and Revision carry the applied state of a fresh applied
	// request; they are zero for rejections and replays.
	Settings OfflinePoolSettings
	Revision OfflinePoolSettingsRevision
	// ResultingETag is the quoted ETag recorded by the original request. It
	// is set for replayed requests (the stored value) and lets the handler
	// answer a safe retry without re-deriving any state.
	ResultingETag string
}

// OfflinePoolSettingsRequestReplay is the wire document returned when a
// PATCH is answered from a stored request receipt instead of being
// re-applied: the durable outcome of the original request, plus the ETag
// that resulted from it.
type OfflinePoolSettingsRequestReplay struct {
	// Outcome is the stored outcome of the original request.
	Outcome OfflinePoolSettingsRequestOutcome `json:"outcome"`
	// ResultingETag is the quoted ETag observed or produced by the original
	// request.
	ResultingETag string `json:"resulting_etag"`
}
