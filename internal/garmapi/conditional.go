package garmapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

var (
	// ErrConditionalManagementDisabled reports construction of the experimental
	// conditional settings client without the explicit opt-in flag. The
	// constructor fails closed instead of falling back to the unconditional
	// management client.
	ErrConditionalManagementDisabled = errors.New("experimental conditional settings client disabled")

	// ErrConditionalSettingsInvalid maps HTTP 400: the request shape was rejected.
	ErrConditionalSettingsInvalid = errors.New("conditional settings rejected: malformed request")
	// ErrConditionalSettingsForbidden maps HTTP 403: the authenticated identity
	// may not manage this pool's settings.
	ErrConditionalSettingsForbidden = errors.New("conditional settings rejected: not permitted")
	// ErrConditionalSettingsNotFound maps HTTP 404: the entity, pool or settings
	// resource is unknown.
	ErrConditionalSettingsNotFound = errors.New("conditional settings rejected: unknown entity or pool")
	// ErrConditionalSettingsUnprocessable maps HTTP 422: the values are
	// semantically invalid (for example min_idle_runners above max_runners).
	ErrConditionalSettingsUnprocessable = errors.New("conditional settings rejected: invalid setting values")
	// ErrConditionalSettingsPreconditionRequired maps HTTP 428: a fresh strong
	// ETag precondition is required.
	ErrConditionalSettingsPreconditionRequired = errors.New("conditional settings rejected: fresh ETag precondition required")
	// ErrConditionalSettingsRequestIDReuse maps HTTP 409: the Idempotency-Key
	// was already used with a different payload or pool. The request was
	// refused and nothing was written, so this is definitive, never
	// ErrManagementOutcomeUnknown.
	ErrConditionalSettingsRequestIDReuse = errors.New("conditional settings rejected: request id already used with different content")

	// ErrConditionalScopeUnsupported maps an entity scope the conditional
	// client deliberately does not implement. ManagementConfig carries
	// allowlists for repositories and pools only; organization and
	// enterprise settings have no validated client-side scope mapping, so
	// calls with those kinds fail closed with this typed error before any
	// network use instead of constructing an unvalidated URL.
	ErrConditionalScopeUnsupported = errors.New("conditional settings unsupported: entity scope not implemented")
)

// SettingsConflictError maps HTTP 412: the If-Match ETag precondition failed,
// so no write was applied. FreshETag carries the response's current strong
// ETag so callers can rebase with GetConditionalSettings and retry
// deliberately. It matches errors.Is(err, ErrManagementConflict). A server
// that omits a valid ETag leaves FreshETag empty; callers must refresh before
// retrying rather than reusing the stale precondition.
type SettingsConflictError struct {
	FreshETag string
}

func (e *SettingsConflictError) Error() string {
	return "conditional settings changed; refresh ETag before editing"
}
func (e *SettingsConflictError) Is(target error) bool { return target == ErrManagementConflict }

const (
	// EntityKindRepository is the only fully implemented conditional
	// settings scope today; its IDs are allowlisted in ManagementConfig.
	EntityKindRepository = "repository"
	// EntityKindOrganization and EntityKindEnterprise are valid wire kinds
	// with no allowlisted client scope yet (ManagementConfig carries no
	// organization or enterprise ID lists). Calls with these kinds fail
	// before any network use with ErrConditionalScopeUnsupported.
	EntityKindOrganization = "organization"
	EntityKindEnterprise   = "enterprise"
)

// EntityRef identifies exactly one managed entity. Kind is one of the
// EntityKind constants; ID is a lowercase UUID.
type EntityRef struct {
	Kind string
	ID   string
}

// PoolSettings is the strict conditional-settings projection. Unknown upstream
// fields are not retained.
type PoolSettings struct {
	Priority       uint64 `json:"priority"`
	MaxRunners     uint64 `json:"max_runners"`
	MinIdleRunners uint64 `json:"min_idle_runners"`
}

// PoolSettingsPatch carries only the fields to change; at least one field must
// be non-nil. Nil fields are omitted from the strict JSON body, so the wire
// shape is always a non-empty subset of {priority, max_runners,
// min_idle_runners}.
type PoolSettingsPatch struct {
	Priority       *uint64 `json:"priority,omitempty"`
	MaxRunners     *uint64 `json:"max_runners,omitempty"`
	MinIdleRunners *uint64 `json:"min_idle_runners,omitempty"`
}

// SettingsResult is the authoritative post-state plus its new strong ETag.
// ETag is the required If-Match precondition for the next
// PatchConditionalSettings call. GetConditionalSettings returns it directly;
// PatchConditionalSettings returns it as ConditionalPatchResult.Settings for
// a fresh applied document.
type SettingsResult struct {
	Settings PoolSettings
	ETag     string
}

// ReplayOutcome is the durable terminal outcome of an earlier PATCH, served
// from the server's stored request receipt when the same (Idempotency-Key,
// payload) is re-presented. The receipt does not store the settings body, so
// a replay never carries a settings tuple: re-GET deliberately if the tuple
// is needed. Outcome is the receipt's terminal outcome ("applied_changed" or
// "applied_noop") and ETag is its resulting strong ETag, accepted only when
// it equals the response's ETag header.
type ReplayOutcome struct {
	Outcome string
	ETag    string
}

// ConditionalPatchResult is the success result of PatchConditionalSettings.
// Exactly one field is non-nil: Settings carries the fresh applied document,
// while Replay carries the receipt outcome when the server answered from its
// durable request receipt. Keeping the two statically distinct prevents a
// replay's absent settings tuple from being mistaken for real pool settings.
// A non-nil error always accompanies the zero value (neither field set).
type ConditionalPatchResult struct {
	Replay   *ReplayOutcome
	Settings *SettingsResult
}

// ConditionalManagementClient is an opt-in experimental client for the native
// conditional pool-settings API (wire v0). It is constructed only when
// ManagementConfig.ExperimentalConditionalSettings is true; the flag's zero
// value leaves every existing code path untouched.
//
// Semantics: a request is dispatched at most once per call and never redirects
// or retries. A transport error, timeout or 5xx after a PATCH dispatch returns
// ErrManagementOutcomeUnknown because the write may have committed: callers
// must read authoritative state and reconcile. This client never auto-retries
// and never falls back to an unconditional write.
//
// Safeguard boundary: this is a low-level wire client, not a policy enforcer.
// It validates scope, preconditions, ranges and response shape, but it never
// applies the controller's policy guards — for example the rule against
// decreasing min_idle_runners before assignment-safe retirement of the excess
// runners (enforced on the unconditional ManagementClient write path, and by
// the manager layer above it). A successful PATCH here is transport and
// precondition confirmation, never policy approval; callers embedding this
// client must enforce those guards themselves.
type ConditionalManagementClient struct {
	client       *Client
	base         string
	pools        map[string]bool
	repositories map[string]bool
	mu           sync.Mutex
}

// NewConditionalManagement builds the experimental client only behind the
// explicit opt-in; otherwise it fails closed with
// ErrConditionalManagementDisabled. Scope validation mirrors NewManagement:
// exact allowlisted UUIDs, independent of ledger pool IDs.
func NewConditionalManagement(config ManagementConfig) (*ConditionalManagementClient, error) {
	if !config.ExperimentalConditionalSettings {
		return nil, ErrConditionalManagementDisabled
	}
	if len(config.PoolIDs) == 0 || len(config.PoolIDs)+len(config.RepositoryIDs) > 1000 {
		return nil, ErrManagement
	}
	c, err := New(config.Endpoint, config.Token, config.Trust, config.Providers, nil)
	if err != nil {
		return nil, ErrManagement
	}
	m := &ConditionalManagementClient{client: c, base: strings.TrimSuffix(c.endpoint, "/instances"), pools: map[string]bool{}, repositories: map[string]bool{}}
	for _, pair := range []struct {
		ids   []string
		scope map[string]bool
	}{{config.PoolIDs, m.pools}, {config.RepositoryIDs, m.repositories}} {
		for _, id := range pair.ids {
			if !managementID(id) || pair.scope[id] {
				c.Close()
				return nil, ErrManagement
			}
			pair.scope[id] = true
		}
	}
	return m, nil
}
func (m *ConditionalManagementClient) Close() { m.client.Close() }

// conditionalPath validates exact scope before any network use. IDs pass the
// managementID UUID shape check, so any constructed path can never carry
// arbitrary input. Repository scope is fully implemented: repository IDs and
// all pool IDs keep exact allowlist scope. Organization and enterprise kinds
// are recognised but not implemented — ManagementConfig carries no scope
// lists for them — so they fail with ErrConditionalScopeUnsupported rather
// than producing an unvalidated URL. Unknown kinds fail as unsafe input.
func (m *ConditionalManagementClient) conditionalPath(ref EntityRef, poolID string) (string, error) {
	switch ref.Kind {
	case EntityKindOrganization, EntityKindEnterprise:
		return "", ErrConditionalScopeUnsupported
	case EntityKindRepository:
	default:
		return "", ErrManagementUnsafe
	}
	if !managementID(ref.ID) || !managementID(poolID) || !m.pools[poolID] {
		return "", ErrManagementUnsafe
	}
	if !m.repositories[ref.ID] {
		return "", ErrManagementUnsafe
	}
	return "/offline/entities/" + ref.Kind + "/" + url.PathEscape(ref.ID) + "/pools/" + url.PathEscape(poolID) + "/settings", nil
}

// conditionalETag accepts only a strong quoted "<epoch>:<seq>" ETag. Weak
// validators and arbitrary tokens are rejected so If-Match never carries
// operator- or server-supplied garbage.
func conditionalETag(value string) bool {
	if len(value) < len("\"0:0\"") || len(value) > 72 || !strings.HasPrefix(value, "\"") || !strings.HasSuffix(value, "\"") {
		return false
	}
	parts := strings.Split(value[1:len(value)-1], ":")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 30 {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// conditionalRejection maps the typed 4xx rejections. Anything unmapped is
// resolved by the caller per method.
func conditionalRejection(status int) error {
	switch status {
	case http.StatusBadRequest:
		return ErrConditionalSettingsInvalid
	case http.StatusForbidden:
		return ErrConditionalSettingsForbidden
	case http.StatusNotFound:
		return ErrConditionalSettingsNotFound
	case http.StatusConflict:
		return ErrConditionalSettingsRequestIDReuse
	case http.StatusUnprocessableEntity:
		return ErrConditionalSettingsUnprocessable
	case http.StatusPreconditionRequired:
		return ErrConditionalSettingsPreconditionRequired
	}
	return nil
}

// conditionalReplay is the wire document a PATCH answered from a stored
// request receipt carries on HTTP 200 instead of the plain settings tuple
// (the server does not store the original settings body).
type conditionalReplay struct {
	Outcome       string `json:"outcome"`
	ResultingETag string `json:"resulting_etag"`
}

// decodeConditionalReplay recognises a 200 replay envelope: exactly the
// {outcome, resulting_etag} document, an applied-only outcome, and a
// resulting_etag that is a strong quoted ETag equal to the response's ETag
// header. The wire carries no request-id echo, so that equality is the only
// consistency check the protocol supports; any mismatch or corrupt envelope
// means the outcome cannot be confirmed and must surface as
// ErrManagementOutcomeUnknown at the caller, never as a confirmed outcome.
func decodeConditionalReplay(data []byte, etag string) (ReplayOutcome, bool) {
	fields, err := row(data)
	if err != nil || len(fields) != 2 || fields["outcome"] == nil || fields["resulting_etag"] == nil {
		return ReplayOutcome{}, false
	}
	var replay conditionalReplay
	if err := json.Unmarshal(data, &replay); err != nil {
		return ReplayOutcome{}, false
	}
	switch replay.Outcome {
	case "applied_changed", "applied_noop":
	default:
		return ReplayOutcome{}, false
	}
	if !conditionalETag(replay.ResultingETag) || replay.ResultingETag != etag {
		return ReplayOutcome{}, false
	}
	return ReplayOutcome{Outcome: replay.Outcome, ETag: replay.ResultingETag}, true
}

// conditionalDispatch sends exactly one request over the shared transport: no
// redirects, no retries, no ambient proxy. It returns the raw status, ETag
// header and bounded body so callers apply typed semantics per method. Reads
// fail with ErrManagement; writes fail with ErrManagementOutcomeUnknown because
// a dispatched write may have committed.
func (m *ConditionalManagementClient) conditionalDispatch(ctx context.Context, method, path string, body any, etag, requestID string) (int, string, []byte, error) {
	failure := ErrManagement
	if method != http.MethodGet {
		failure = ErrManagementOutcomeUnknown
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, "", nil, ErrManagementUnsafe
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, reader)
	if err != nil {
		return 0, "", nil, failure
	}
	req.Header.Set("Authorization", "Bearer "+m.client.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	if requestID != "" {
		req.Header.Set("Idempotency-Key", requestID)
	}
	res, err := m.client.http.Do(req)
	if err != nil {
		return 0, "", nil, failure
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return 0, "", nil, failure
	}
	return res.StatusCode, res.Header.Get("ETag"), data, nil
}

func decodeConditionalSettings(data []byte) (PoolSettings, error) {
	fields, err := row(data)
	if err != nil {
		return PoolSettings{}, ErrManagement
	}
	settings := PoolSettings{}
	if settings.Priority, err = number(fields, "priority", true); err != nil {
		return PoolSettings{}, err
	}
	if settings.MaxRunners, err = number(fields, "max_runners", true); err != nil {
		return PoolSettings{}, err
	}
	if settings.MinIdleRunners, err = number(fields, "min_idle_runners", true); err != nil {
		return PoolSettings{}, err
	}
	if settings.Priority > 1000000 || settings.MaxRunners > 1000000 || settings.MinIdleRunners > settings.MaxRunners {
		return PoolSettings{}, ErrManagement
	}
	return settings, nil
}

// GetConditionalSettings reads the pool's conditional settings and their
// strong ETag. The ETag is the required If-Match precondition for
// PatchConditionalSettings.
func (m *ConditionalManagementClient) GetConditionalSettings(ctx context.Context, ref EntityRef, poolID string) (SettingsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.conditionalPath(ref, poolID)
	if err != nil {
		return SettingsResult{}, err
	}
	status, etag, data, err := m.conditionalDispatch(ctx, http.MethodGet, path, nil, "", "")
	if err != nil {
		return SettingsResult{}, err
	}
	if status != http.StatusOK {
		if rejection := conditionalRejection(status); rejection != nil {
			return SettingsResult{}, rejection
		}
		return SettingsResult{}, ErrManagement
	}
	settings, err := decodeConditionalSettings(data)
	if err != nil || !conditionalETag(etag) {
		return SettingsResult{}, ErrManagement
	}
	return SettingsResult{Settings: settings, ETag: etag}, nil
}

// PatchConditionalSettings applies one strict patch behind the caller's strong
// ETag precondition, at most once per call. Transport errors, timeouts and 5xx
// after dispatch return ErrManagementOutcomeUnknown: the write may have
// committed, so never auto-retry and never fall back to an unconditional
// write; read authoritative state and reconcile first. A 412 returns a
// *SettingsConflictError carrying the fresh ETag for a deliberate rebase. A
// 409 returns ErrConditionalSettingsRequestIDReuse: the Idempotency-Key was
// already used with different content and nothing was written, so the outcome
// is known and must not be held as UNKNOWN. On success the returned
// ConditionalPatchResult has exactly one field set: Settings for a fresh
// applied document, or Replay when the server answered from its durable
// request receipt for a re-presented (key, payload) — the receipt carries no
// settings tuple, so re-GET deliberately if the tuple is needed.
func (m *ConditionalManagementClient) PatchConditionalSettings(ctx context.Context, ref EntityRef, poolID, etag, requestID string, patch PoolSettingsPatch) (ConditionalPatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.conditionalPath(ref, poolID)
	if err != nil {
		return ConditionalPatchResult{}, err
	}
	if !conditionalETag(etag) {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	if patch.Priority == nil && patch.MaxRunners == nil && patch.MinIdleRunners == nil {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	if patch.Priority != nil && *patch.Priority > 1000000 {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	if patch.MaxRunners != nil && (*patch.MaxRunners == 0 || *patch.MaxRunners > 1000000) {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	if patch.MinIdleRunners != nil && *patch.MinIdleRunners > 1000000 {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	if requestID != "" && !boundedSettingText(requestID, 256) {
		return ConditionalPatchResult{}, ErrManagementUnsafe
	}
	status, newETag, data, err := m.conditionalDispatch(ctx, http.MethodPatch, path, patch, etag, requestID)
	if err != nil {
		return ConditionalPatchResult{}, err
	}
	if status == http.StatusOK {
		settings, err := decodeConditionalSettings(data)
		if err != nil {
			// A PATCH answered from a stored request receipt (same
			// Idempotency-Key and payload) returns 200 with an outcome
			// envelope instead of the settings tuple. That outcome is
			// durable and known; it must not surface as UNKNOWN.
			if replay, ok := decodeConditionalReplay(data, newETag); ok {
				return ConditionalPatchResult{Replay: &replay}, nil
			}
			// The write may have committed but the response cannot be trusted.
			return ConditionalPatchResult{}, ErrManagementOutcomeUnknown
		}
		if !conditionalETag(newETag) {
			// The write may have committed but the response cannot be trusted.
			return ConditionalPatchResult{}, ErrManagementOutcomeUnknown
		}
		return ConditionalPatchResult{Settings: &SettingsResult{Settings: settings, ETag: newETag}}, nil
	}
	if status == http.StatusPreconditionFailed {
		fresh := ""
		if conditionalETag(newETag) {
			fresh = newETag
		}
		return ConditionalPatchResult{}, &SettingsConflictError{FreshETag: fresh}
	}
	if rejection := conditionalRejection(status); rejection != nil {
		return ConditionalPatchResult{}, rejection
	}
	return ConditionalPatchResult{}, ErrManagementOutcomeUnknown
}
