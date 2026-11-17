package garmmanagement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Remote is implemented by the scoped, authenticated garmapi.ManagementClient.
// Implementations must return its explicit pre-dispatch errors ONLY before a PUT.
type Remote interface {
	ManagementIdentity() string
	Snapshot(context.Context) (garmapi.ManagementSnapshot, error)
	Pool(context.Context, string) (garmapi.ManagedPool, error)
	Repository(context.Context, string) (garmapi.ManagedRepository, error)
	UpdatePool(context.Context, string, string, garmapi.PoolUpdate) (garmapi.ManagedPool, error)
	UpdateRepositoryBalancer(context.Context, string, string, string) (garmapi.ManagedRepository, error)
}

type Manager struct {
	remote      Remote
	journal     *Journal
	conditional *garmapi.ConditionalManagementClient
	gate        chan struct{}
}

func New(remote Remote, journal *Journal) (*Manager, error) {
	return NewConditional(remote, journal, nil)
}

// NewConditional is New plus an optional experimental conditional settings
// client. When conditional is nil every behavior is identical to New. When
// set, pool updates touching only priority/max_runners/min_idle_runners on
// repository-scoped pools are written through the native conditional API
// (If-Match + Idempotency-Key) instead of the legacy GET/fingerprint/PUT
// path; all policy guards and journal semantics stay in this layer.
func NewConditional(remote Remote, journal *Journal, conditional *garmapi.ConditionalManagementClient) (*Manager, error) {
	if remote == nil || journal == nil {
		return nil, ErrJournal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if journal.bind(ctx, remote.ManagementIdentity()) != nil {
		return nil, ErrJournal
	}
	return &Manager{remote: remote, journal: journal, conditional: conditional, gate: make(chan struct{}, 1)}, nil
}
func (m *Manager) enter(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrJournal
	}
}
func (m *Manager) leave() { <-m.gate }

// Status persists across process restarts. Pending includes a possibly live,
// crashed-before-PUT or crashed-after-PUT writer; no automatic replay is allowed.
type Status struct {
	WriteBlocked bool     `json:"write_blocked"`
	Pending      []Intent `json:"pending"`
}
type Snapshot struct {
	garmapi.ManagementSnapshot
	Status Status `json:"journal"`
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	pending, err := m.journal.Pending(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{WriteBlocked: len(pending) != 0, Pending: pending}, nil
}
func (m *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return Snapshot{}, err
	}
	defer m.leave()
	snapshot, err := m.remote.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	status, err := m.Status(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{ManagementSnapshot: snapshot, Status: status}, nil
}
func (m *Manager) Audit(ctx context.Context, after uint64, limit int) ([]AuditEvent, error) {
	return m.journal.Audit(ctx, after, limit)
}

func validUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || strings.ToLower(id) != id {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16
}
func requestHash(id, target, expected, kind, reason string, body any) (string, error) {
	if !validUUID(id) || !validUUID(target) || len(expected) != 64 || strings.TrimSpace(reason) != reason || reason == "" || len(reason) > 512 {
		return "", garmapi.ErrManagementUnsafe
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return "", garmapi.ErrManagementUnsafe
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return "", garmapi.ErrManagementUnsafe
		}
	}
	encoded, err := json.Marshal(struct {
		Target, Expected, Kind, Reason string
		Body                           any
	}{target, expected, kind, reason, body})
	if err != nil {
		return "", garmapi.ErrManagementUnsafe
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
func encoded(value any) []byte { data, _ := json.Marshal(value); return data }
func (m *Manager) complete(id string, result any, remoteErr error) error {
	state := "confirmed"
	var data []byte
	if remoteErr == nil {
		data = encoded(result)
	} else if errors.Is(remoteErr, garmapi.ErrManagementDisabled) || errors.Is(remoteErr, garmapi.ErrManagementConflict) || errors.Is(remoteErr, garmapi.ErrManagementUnsafe) ||
		// Definitive conditional-write rejections: the server refused this
		// exact request before (or instead of) any effect. They are not
		// uncertain outcomes and must not linger as unknown.
		errors.Is(remoteErr, garmapi.ErrConditionalSettingsInvalid) || errors.Is(remoteErr, garmapi.ErrConditionalSettingsForbidden) ||
		errors.Is(remoteErr, garmapi.ErrConditionalSettingsNotFound) || errors.Is(remoteErr, garmapi.ErrConditionalSettingsUnprocessable) ||
		errors.Is(remoteErr, garmapi.ErrConditionalSettingsPreconditionRequired) || errors.Is(remoteErr, garmapi.ErrConditionalSettingsRequestIDReuse) ||
		errors.Is(remoteErr, garmapi.ErrConditionalScopeUnsupported) {
		state = "rejected"
	} else {
		state = "unknown"
	}
	// Client cancellation cannot discard the outcome record after remote dispatch.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if m.journal.finish(ctx, id, state, data) != nil {
		return ErrOutcomeUnknown
	}
	if state == "unknown" {
		return ErrOutcomeUnknown
	}
	return remoteErr
}

// UpdatePool records a durable immutable intent before calling the remote writer.
// A confirmed request-ID replay returns the recorded observation without reading
// or writing Garm. The caller must refresh separately to see current settings.
func (m *Manager) UpdatePool(ctx context.Context, requestID, id, expected string, update garmapi.PoolUpdate, reason string) (garmapi.ManagedPool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return garmapi.ManagedPool{}, err
	}
	defer m.leave()
	// Own a stable copy of caller-owned pointer/slice inputs.
	raw, err := json.Marshal(update)
	if err != nil {
		return garmapi.ManagedPool{}, garmapi.ErrManagementUnsafe
	}
	var owned garmapi.PoolUpdate
	if json.Unmarshal(raw, &owned) != nil {
		return garmapi.ManagedPool{}, garmapi.ErrManagementUnsafe
	}
	if owned.Tags != nil {
		sort.Strings(*owned.Tags)
	}
	hash, err := requestHash(requestID, id, expected, "pool", reason, owned)
	if err != nil {
		return garmapi.ManagedPool{}, err
	}
	if result, exists, err := m.journal.replay(ctx, requestID, hash); err != nil {
		return garmapi.ManagedPool{}, err
	} else if exists {
		var pool garmapi.ManagedPool
		if json.Unmarshal(result, &pool) != nil {
			return garmapi.ManagedPool{}, ErrJournal
		}
		return pool, nil
	}
	before, err := m.remote.Pool(ctx, id)
	if err != nil {
		return garmapi.ManagedPool{}, err
	}
	if before.Fingerprint != expected {
		return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
	}
	desired := before
	desired.Tags = append([]string{}, before.Tags...)
	if owned.Tags != nil {
		desired.Tags = append([]string{}, (*owned.Tags)...)
	}
	if owned.Priority != nil {
		desired.Priority = *owned.Priority
	}
	if owned.MaxRunners != nil {
		desired.MaxRunners = *owned.MaxRunners
	}
	if owned.MinIdleRunners != nil {
		desired.MinIdleRunners = *owned.MinIdleRunners
	}
	if owned.Enabled != nil {
		desired.Enabled = *owned.Enabled
	}
	// The experimental conditional path applies only when the update touches
	// nothing outside the native conditional contract (priority, max_runners,
	// min_idle_runners) and the pool is repository-scoped (the only scope the
	// conditional client implements). Everything else keeps the legacy path.
	useConditional := m.conditional != nil && before.RepositoryID != "" && owned.Tags == nil && owned.Enabled == nil &&
		(owned.Priority != nil || owned.MaxRunners != nil || owned.MinIdleRunners != nil)
	intent := Intent{RequestID: requestID, Kind: "pool", Target: id, Reason: reason, Before: encoded(before), Desired: encoded(desired)}
	replay, err := m.journal.begin(ctx, intent, hash, expected)
	if err != nil {
		return garmapi.ManagedPool{}, err
	}
	if replay != nil {
		var pool garmapi.ManagedPool
		if json.Unmarshal(replay, &pool) != nil {
			return garmapi.ManagedPool{}, ErrJournal
		}
		return pool, nil
	}
	var result garmapi.ManagedPool
	var remoteErr error
	if useConditional {
		result, remoteErr = m.writeConditionalPool(ctx, id, owned, before, desired, requestID)
	} else {
		result, remoteErr = m.remote.UpdatePool(ctx, id, expected, owned)
	}
	if err = m.complete(requestID, result, remoteErr); err != nil {
		return garmapi.ManagedPool{}, err
	}
	return result, nil
}

// conditionalPoolGuards mirrors the legacy ManagementClient policy guards so
// both write paths enforce identical controller policy. The low-level
// conditional client never enforces these; the manager layer owns policy.
func conditionalPoolGuards(update garmapi.PoolUpdate, before garmapi.ManagedPool) error {
	if update.Priority != nil && *update.Priority > 1000000 {
		return garmapi.ErrManagementUnsafe
	}
	maximum, minimum := before.MaxRunners, before.MinIdleRunners
	if update.MaxRunners != nil {
		maximum = *update.MaxRunners
	}
	if update.MinIdleRunners != nil {
		minimum = *update.MinIdleRunners
	}
	// The minimum-idle decrease safeguard stays in force until safe
	// retirement exists; decreasing min_idle releases capacity accounting
	// without proof the physical runners are gone.
	if maximum == 0 || maximum > 1000000 || minimum > maximum || minimum < before.MinIdleRunners {
		return garmapi.ErrManagementUnsafe
	}
	return nil
}

// writeConditionalPool writes the settings tuple through the native
// conditional API after the journaled intent is durably recorded by the
// caller. On a confirmed outcome the result is refreshed from a legacy read
// so callers receive current fingerprint/generation; if that refresh fails
// the journaled desired state is returned and the caller must refresh.
func (m *Manager) writeConditionalPool(ctx context.Context, id string, update garmapi.PoolUpdate, before, desired garmapi.ManagedPool, requestID string) (garmapi.ManagedPool, error) {
	if err := conditionalPoolGuards(update, before); err != nil {
		return garmapi.ManagedPool{}, err
	}
	ref := garmapi.EntityRef{Kind: garmapi.EntityKindRepository, ID: before.RepositoryID}
	patch := garmapi.PoolSettingsPatch{Priority: update.Priority, MaxRunners: update.MaxRunners, MinIdleRunners: update.MinIdleRunners}
	current, err := m.conditional.GetConditionalSettings(ctx, ref, id)
	if err != nil {
		return garmapi.ManagedPool{}, err
	}
	// Replay and fresh-applied results are both confirmed outcomes; the
	// durable receipt (keyed by request id) guarantees at-most-once effect.
	if _, err := m.conditional.PatchConditionalSettings(ctx, ref, id, current.ETag, requestID, patch); err != nil {
		return garmapi.ManagedPool{}, err
	}
	out := desired
	if fresh, ferr := m.remote.Pool(ctx, id); ferr == nil {
		out = fresh
	}
	return out, nil
}

// ResolvePool settles the authoritative outcome of a pending pool write by
// re-presenting the journaled request to the native API, whose durable
// receipt answers definitively (or applies the idempotent effect if the
// original dispatch never arrived). A 412 or any ambiguous outcome does NOT
// clear the pending intent: once receipt history may be lost, a stale
// response is not proof of non-effect, so UNKNOWN stays UNKNOWN until this
// resolution (or operator reconciliation) succeeds. Only conditional
// settings intents are resolvable; legacy intents keep observe-only
// reconciliation semantics.
func (m *Manager) ResolvePool(ctx context.Context, requestID string) (garmapi.ManagedPool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return garmapi.ManagedPool{}, err
	}
	defer m.leave()
	pending, err := m.journal.Pending(ctx)
	if err != nil {
		return garmapi.ManagedPool{}, err
	}
	var intent *Intent
	for i := range pending {
		if pending[i].RequestID == requestID && pending[i].Kind == "pool" {
			intent = &pending[i]
			break
		}
	}
	if intent == nil {
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	var before, desired garmapi.ManagedPool
	if json.Unmarshal(intent.Before, &before) != nil || json.Unmarshal(intent.Desired, &desired) != nil {
		return garmapi.ManagedPool{}, ErrJournal
	}
	if m.conditional == nil || before.RepositoryID == "" {
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	update := garmapi.PoolUpdate{}
	if desired.Priority != before.Priority {
		v := desired.Priority
		update.Priority = &v
	}
	if desired.MaxRunners != before.MaxRunners {
		v := desired.MaxRunners
		update.MaxRunners = &v
	}
	if desired.MinIdleRunners != before.MinIdleRunners {
		v := desired.MinIdleRunners
		update.MinIdleRunners = &v
	}
	if update.Priority == nil && update.MaxRunners == nil && update.MinIdleRunners == nil {
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	if err := conditionalPoolGuards(update, before); err != nil {
		// The original dispatch could never have been sent past these guards;
		// a violating journaled intent means journal corruption.
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	ref := garmapi.EntityRef{Kind: garmapi.EntityKindRepository, ID: before.RepositoryID}
	patch := garmapi.PoolSettingsPatch{Priority: update.Priority, MaxRunners: update.MaxRunners, MinIdleRunners: update.MinIdleRunners}
	current, err := m.conditional.GetConditionalSettings(ctx, ref, intent.Target)
	if err != nil {
		// Resolution read failure says nothing about the original write; the
		// intent stays pending.
		return garmapi.ManagedPool{}, ErrOutcomeUnknown
	}
	_, err = m.conditional.PatchConditionalSettings(ctx, ref, intent.Target, current.ETag, requestID, patch)
	switch {
	case err == nil:
		out := desired
		if fresh, ferr := m.remote.Pool(ctx, intent.Target); ferr == nil {
			out = fresh
		}
		if cerr := m.complete(requestID, out, nil); cerr != nil {
			return garmapi.ManagedPool{}, cerr
		}
		return out, nil
	case errors.Is(err, garmapi.ErrManagementOutcomeUnknown), errors.Is(err, garmapi.ErrManagementConflict):
		// Ambiguous, or a stale precondition that cannot prove non-effect once
		// receipts may be lost: keep the intent pending.
		return garmapi.ManagedPool{}, ErrOutcomeUnknown
	default:
		if cerr := m.complete(requestID, nil, err); cerr != nil {
			return garmapi.ManagedPool{}, cerr
		}
		return garmapi.ManagedPool{}, err
	}
}
func (m *Manager) UpdateRepositoryBalancer(ctx context.Context, requestID, id, expected, value, reason string) (garmapi.ManagedRepository, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return garmapi.ManagedRepository{}, err
	}
	defer m.leave()
	hash, err := requestHash(requestID, id, expected, "repository", reason, value)
	if err != nil {
		return garmapi.ManagedRepository{}, err
	}
	if result, exists, err := m.journal.replay(ctx, requestID, hash); err != nil {
		return garmapi.ManagedRepository{}, err
	} else if exists {
		var repo garmapi.ManagedRepository
		if json.Unmarshal(result, &repo) != nil {
			return garmapi.ManagedRepository{}, ErrJournal
		}
		return repo, nil
	}
	before, err := m.remote.Repository(ctx, id)
	if err != nil {
		return garmapi.ManagedRepository{}, err
	}
	if before.Fingerprint != expected {
		return garmapi.ManagedRepository{}, garmapi.ErrManagementConflict
	}
	desired := before
	desired.Balancer = value
	replay, err := m.journal.begin(ctx, Intent{RequestID: requestID, Kind: "repository", Target: id, Reason: reason, Before: encoded(before), Desired: encoded(desired)}, hash, expected)
	if err != nil {
		return garmapi.ManagedRepository{}, err
	}
	if replay != nil {
		var repo garmapi.ManagedRepository
		if json.Unmarshal(replay, &repo) != nil {
			return garmapi.ManagedRepository{}, ErrJournal
		}
		return repo, nil
	}
	result, remoteErr := m.remote.UpdateRepositoryBalancer(ctx, id, expected, value)
	if err = m.complete(requestID, result, remoteErr); err != nil {
		return garmapi.ManagedRepository{}, err
	}
	return result, nil
}

type Observation struct {
	RequestID      string          `json:"request_id"`
	MatchesDesired bool            `json:"matches_desired"`
	Settings       json.RawMessage `json:"settings"`
}

func equivalent(kind string, observed, desired []byte) bool {
	var operation struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(desired, &operation) == nil && operation.Operation == "create_disabled_pool" {
		return false
	} // Repository GET never proves a POST completed or captures its UUID.
	if kind == "pool" {
		var a, b garmapi.ManagedPool
		if json.Unmarshal(observed, &a) != nil || json.Unmarshal(desired, &b) != nil {
			return false
		}
		a.Fingerprint = ""
		b.Fingerprint = ""
		sort.Strings(a.Tags)
		sort.Strings(b.Tags)
		return bytes.Equal(encoded(a), encoded(b))
	}
	var a, b garmapi.ManagedRepository
	if json.Unmarshal(observed, &a) != nil || json.Unmarshal(desired, &b) != nil {
		return false
	}
	a.Fingerprint = ""
	b.Fingerprint = ""
	return bytes.Equal(encoded(a), encoded(b))
}

// ReconcilePending records a fresh authoritative OBSERVATION only. Even matching
// desired settings cannot prove a delayed PUT has completed or an external writer
// is fenced. This foundation deliberately provides NO clear/reset/unblock method.
func (m *Manager) ReconcilePending(ctx context.Context) ([]Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	pending, err := m.journal.Pending(ctx)
	if err != nil {
		return nil, err
	}
	observations := []Observation{}
	for _, intent := range pending {
		var data []byte
		if intent.Kind == "pool" {
			settings, e := m.remote.Pool(ctx, intent.Target)
			if e != nil {
				return nil, e
			}
			data = encoded(settings)
		} else {
			settings, e := m.remote.Repository(ctx, intent.Target)
			if e != nil {
				return nil, e
			}
			data = encoded(settings)
		}
		matches := equivalent(intent.Kind, data, intent.Desired)
		kind := "observed_other"
		if matches {
			kind = "observed_desired"
		}
		if err = m.journal.observe(ctx, intent.RequestID, kind, data); err != nil {
			return nil, err
		}
		observations = append(observations, Observation{RequestID: intent.RequestID, MatchesDesired: matches, Settings: data})
	}
	return observations, nil
}
