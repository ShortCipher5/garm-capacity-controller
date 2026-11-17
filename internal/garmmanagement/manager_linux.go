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
	dispatch := poolDispatch{API: "legacy", Update: owned, Fingerprint: expected}
	var conditionalETag string
	if useConditional {
		// Fetch the conditional revision UP FRONT and bind it to the reviewed
		// snapshot: the native tuple must agree with the legacy state whose
		// fingerprint the operator reviewed. Accepting a fresh revision here
		// could silently overwrite another writer's change or defeat the
		// minimum-idle decrease safeguard by re-basing the write onto state
		// nobody reviewed.
		ref := garmapi.EntityRef{Kind: garmapi.EntityKindRepository, ID: before.RepositoryID}
		current, err := m.conditional.GetConditionalSettings(ctx, ref, id)
		if err != nil {
			return garmapi.ManagedPool{}, err
		}
		if current.Settings.Priority != before.Priority || current.Settings.MaxRunners != before.MaxRunners || current.Settings.MinIdleRunners != before.MinIdleRunners {
			return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
		}
		conditionalETag = current.ETag
		dispatch = poolDispatch{API: "conditional", Update: owned, ETag: conditionalETag, Fingerprint: expected}
	}
	intent := Intent{RequestID: requestID, Kind: "pool", Target: id, Reason: reason, Before: encoded(before), Desired: encoded(desired), Dispatch: encoded(dispatch)}
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
		result, remoteErr = m.writeConditionalPool(ctx, id, owned, before, desired, requestID, conditionalETag)
	} else {
		result, remoteErr = m.remote.UpdatePool(ctx, id, expected, owned)
	}
	if err = m.complete(requestID, result, remoteErr); err != nil {
		return garmapi.ManagedPool{}, err
	}
	return result, nil
}

// poolDispatch is the exact dispatch record persisted with a pool intent so
// recovery re-presents the identical request. Without it, recovery would have
// to reconstruct a request — dropping fields that were supplied at their
// current value, confusing legacy and conditional APIs, and re-deriving a
// precondition nobody reviewed.
type poolDispatch struct {
	API    string             `json:"api"` // "conditional" or "legacy"
	Update garmapi.PoolUpdate `json:"update"`
	// ETag is the strong quoted conditional precondition bound to the
	// reviewed revision at dispatch time (conditional API only).
	ETag string `json:"etag,omitempty"`
	// Fingerprint is the reviewed fingerprint the operator presented.
	Fingerprint string `json:"fingerprint"`
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
// caller, against the revision bound in UpdatePool (the one the operator
// reviewed). On a confirmed outcome the result is refreshed from a legacy
// read so callers receive current fingerprint/generation; if that refresh
// fails the journaled desired state is returned and the caller must refresh.
func (m *Manager) writeConditionalPool(ctx context.Context, id string, update garmapi.PoolUpdate, before, desired garmapi.ManagedPool, requestID, etag string) (garmapi.ManagedPool, error) {
	if err := conditionalPoolGuards(update, before); err != nil {
		return garmapi.ManagedPool{}, err
	}
	ref := garmapi.EntityRef{Kind: garmapi.EntityKindRepository, ID: before.RepositoryID}
	patch := garmapi.PoolSettingsPatch{Priority: update.Priority, MaxRunners: update.MaxRunners, MinIdleRunners: update.MinIdleRunners}
	// Replay and fresh-applied results are both confirmed outcomes; the
	// durable receipt (keyed by request id) guarantees at-most-once effect.
	if _, err := m.conditional.PatchConditionalSettings(ctx, ref, id, etag, requestID, patch); err != nil {
		return garmapi.ManagedPool{}, err
	}
	out := desired
	if fresh, ferr := m.remote.Pool(ctx, id); ferr == nil {
		out = fresh
	}
	return out, nil
}

// ResolvePool settles the authoritative outcome of a pending pool write. It
// re-presents the EXACT journaled dispatch — same supplied fields, same
// request id, and the ORIGINAL conditional precondition — so the native
// durable receipt answers definitively. A replay (already applied) confirms
// the write regardless of how the current tuple later moved: replaying it
// changes nothing, and a later writer raising min idle must not block
// confirming a historical outcome. A fresh apply re-checks the policy guards
// against the current tuple as a tripwire. Every non-definitive answer holds
// UNKNOWN: after receipt history may be lost, a 412, an id-reuse refusal or
// a 4xx is not proof the original write failed, and applying the journaled
// desire against a freshly obtained revision would overwrite writers nobody
// reviewed. Resolution requires the experimental conditional client; if it
// is disabled (e.g. after a restart with the flag off) the outcome cannot be
// established and the intent stays UNKNOWN. Only conditional settings intents
// recorded with a dispatch record are resolvable; legacy intents keep
// observe-only reconciliation semantics.
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
	if len(intent.Dispatch) == 0 {
		// Intents journaled before dispatch records existed, or without one,
		// are never re-presented from a reconstruction.
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	var dispatch poolDispatch
	if json.Unmarshal(intent.Dispatch, &dispatch) != nil || dispatch.API != "conditional" {
		// A legacy write must never be resolved as a partial conditional
		// write; keep observe-only semantics for it.
		return garmapi.ManagedPool{}, ErrRequestConflict
	}
	if m.conditional == nil {
		// Restarted with the experimental client disabled (or the native
		// endpoint removed) while a conditional write is still pending: there
		// is no receipt to consult and no safe way to re-present the write.
		// Hold UNKNOWN instead of dereferencing a nil client; re-enable the
		// experiment or reconcile the intent manually.
		return garmapi.ManagedPool{}, ErrOutcomeUnknown
	}
	var before garmapi.ManagedPool
	if json.Unmarshal(intent.Before, &before) != nil || before.RepositoryID == "" {
		return garmapi.ManagedPool{}, ErrJournal
	}
	ref := garmapi.EntityRef{Kind: garmapi.EntityKindRepository, ID: before.RepositoryID}
	current, err := m.conditional.GetConditionalSettings(ctx, ref, intent.Target)
	if err != nil {
		// The read says nothing about the original write; hold.
		return garmapi.ManagedPool{}, ErrOutcomeUnknown
	}
	// Re-evaluate policy against the CURRENT tuple, not the journaled base:
	// re-applying a value that merely repeated the reviewed state could still
	// decrease the current minimum idle after another writer raised it.
	currentBase := before
	currentBase.Priority = current.Settings.Priority
	currentBase.MaxRunners = current.Settings.MaxRunners
	currentBase.MinIdleRunners = current.Settings.MinIdleRunners
	patch := garmapi.PoolSettingsPatch{Priority: dispatch.Update.Priority, MaxRunners: dispatch.Update.MaxRunners, MinIdleRunners: dispatch.Update.MinIdleRunners}
	// Present the original precondition, never a fresh one: a stored receipt
	// answers regardless, and without one a stale precondition refuses
	// instead of applying the journaled desire against an unreviewed revision.
	result, err := m.conditional.PatchConditionalSettings(ctx, ref, intent.Target, dispatch.ETag, requestID, patch)
	if err != nil {
		// 412 (stale), 409 (id bound to other content) and every 4xx share a
		// cause with receipt loss: none of them proves the original write
		// failed. Hold UNKNOWN; operator reconciliation is the only exit.
		return garmapi.ManagedPool{}, ErrOutcomeUnknown
	}
	if result.Replay == nil && result.Settings != nil {
		// This call freshly applied the write. Under the wire contract the
		// original strong ETag precondition guarantees the apply landed on the
		// reviewed snapshot — a changed revision refuses with 412 above — so
		// this guard passes identically to the original dispatch. It is kept
		// as a tripwire against ETag semantics drift: if a server ever applied
		// against a changed tuple, the policy error surfaces instead of a
		// false confirmation. The intent stays pending and a later resolve
		// confirms from the now-stored receipt. A replay (already applied)
		// skips the guard: replaying a historical write changes nothing, and a
		// later writer raising min idle must not block confirming the
		// original outcome.
		if err := conditionalPoolGuards(dispatch.Update, currentBase); err != nil {
			return garmapi.ManagedPool{}, err
		}
	}
	desired := before
	_ = json.Unmarshal(intent.Desired, &desired)
	out := desired
	if fresh, ferr := m.remote.Pool(ctx, intent.Target); ferr == nil {
		out = fresh
	}
	if cerr := m.complete(requestID, out, nil); cerr != nil {
		return garmapi.ManagedPool{}, cerr
	}
	return out, nil
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
