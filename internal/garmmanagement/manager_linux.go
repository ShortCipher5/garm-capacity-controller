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
	remote  Remote
	journal *Journal
	gate    chan struct{}
}

func New(remote Remote, journal *Journal) (*Manager, error) {
	if remote == nil || journal == nil {
		return nil, ErrJournal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if journal.bind(ctx, remote.ManagementIdentity()) != nil {
		return nil, ErrJournal
	}
	return &Manager{remote: remote, journal: journal, gate: make(chan struct{}, 1)}, nil
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
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
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
	} else if errors.Is(remoteErr, garmapi.ErrManagementDisabled) || errors.Is(remoteErr, garmapi.ErrManagementConflict) || errors.Is(remoteErr, garmapi.ErrManagementUnsafe) {
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
	result, remoteErr := m.remote.UpdatePool(ctx, id, expected, owned)
	if err = m.complete(requestID, result, remoteErr); err != nil {
		return garmapi.ManagedPool{}, err
	}
	return result, nil
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
