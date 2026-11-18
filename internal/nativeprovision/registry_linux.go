//go:build linux

package nativeprovision

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxRegistryRevisions = 64

// RegisteredPlan is durable configuration history, never permission to execute.
// A new SSH transfer still requires the independently reviewed current plan.
type RegisteredPlan struct {
	Sequence             uint64 `json:"sequence"`
	PreparationID        string `json:"preparation_id"`
	TargetDigest         string `json:"target_digest"`
	PreviousReviewDigest string `json:"previous_review_digest"`
	ReviewDigest         string `json:"review_digest"`
	SelectionDigest      string `json:"selection_digest"`
}

func (s *Service) registeredPlan() (*RegisteredPlan, error) {
	f, e := os.Open(s.dir)
	if e != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	names, e := f.Readdirnames(16385)
	if (e != nil && e != io.EOF) || len(names) > 16384 {
		return nil, ErrInvalid
	}
	prefix := s.plan.Manifest.ID + ".registered."
	count := 0
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			count++
		}
	}
	if count > maxRegistryRevisions {
		return nil, ErrInvalid
	}
	var last *RegisteredPlan
	for i := 1; i <= count; i++ {
		raw, e := s.read(fmt.Sprintf("%s%06d.json", prefix, i))
		if e != nil {
			return nil, ErrInvalid
		}
		var r RegisteredPlan
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Sequence != uint64(i) || r.PreparationID != s.plan.Manifest.ID || r.TargetDigest != targetDigest(s.plan.Target) || !registryHash(r.PreviousReviewDigest) || !registryHash(r.ReviewDigest) || !registryHash(r.SelectionDigest) || r.PreviousReviewDigest == r.ReviewDigest || (last != nil && r.PreviousReviewDigest != last.ReviewDigest) {
			return nil, ErrInvalid
		}
		canonical, _ := json.Marshal(r)
		if string(canonical) != string(raw) {
			return nil, ErrInvalid
		}
		last = &r
	}
	return last, nil
}

func (s *Service) currentRegisteredPlan() error {
	r, e := s.registeredPlan()
	if e != nil {
		return e
	}
	if r != nil && r.ReviewDigest != s.ReviewDigest() {
		return ErrInvalid
	}
	return nil
}

// RegisterReviewedReplacement permanently changes the allowed plan for one
// unconsumed preparation ID. It serializes with actual execution across processes.
// Both services must come from actual loaded bundles for the same pinned target
// and journal. Callers must separately authorize the exact new selection/plan.
// No SSH, retry, upload, start or activation occurs here.
func (s *Service) RegisterReviewedReplacement(ctx context.Context, previous *Service, selectionDigest string) (RegisteredPlan, error) {
	if s == nil || previous == nil || s == previous || ctx == nil || ctx.Err() != nil || s.closed.Load() || previous.closed.Load() || !registryHash(selectionDigest) || s.dir != previous.dir || s.plan.Manifest.ID != previous.plan.Manifest.ID || targetDigest(s.plan.Target) != targetDigest(previous.plan.Target) || s.ReviewDigest() == previous.ReviewDigest() {
		return RegisteredPlan{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, e := s.lock()
	if e != nil {
		return RegisteredPlan{}, e
	}
	defer lock.Close()
	if ctx.Err() != nil {
		return RegisteredPlan{}, ErrInvalid
	}
	for _, suffix := range []string{".intent.json", ".receipt.json"} {
		if _, e := s.read(s.plan.Manifest.ID + suffix); !os.IsNotExist(e) {
			return RegisteredPlan{}, ErrInvalid
		}
	}
	seq, e := s.latestSequence()
	if e != nil || seq != 0 {
		return RegisteredPlan{}, ErrInvalid
	}
	last, e := s.registeredPlan()
	if e != nil {
		return RegisteredPlan{}, e
	}
	n := uint64(1)
	if last != nil {
		if last.ReviewDigest == s.ReviewDigest() && last.PreviousReviewDigest == previous.ReviewDigest() && last.SelectionDigest == selectionDigest {
			return *last, nil
		}
		if last.ReviewDigest != previous.ReviewDigest() {
			return RegisteredPlan{}, ErrInvalid
		}
		n = last.Sequence + 1
	}
	if n > maxRegistryRevisions {
		return RegisteredPlan{}, ErrInvalid
	}
	r := RegisteredPlan{n, s.plan.Manifest.ID, targetDigest(s.plan.Target), previous.ReviewDigest(), s.ReviewDigest(), selectionDigest}
	raw, _ := json.Marshal(r)
	if s.write(fmt.Sprintf("%s.registered.%06d.json", r.PreparationID, r.Sequence), raw) != nil {
		return RegisteredPlan{}, ErrUnknown
	}
	return r, nil
}

func registryHash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == v
}

func targetDigest(t Target) string { raw, _ := json.Marshal(t); return digest(raw) }

// RegisteredConfiguration reads durable reload metadata for startup recovery.
// It does not require this service's old review to remain current, and cannot
// authorize a transfer. The caller must re-open the completed fixed assembly.
func (s *Service) RegisteredConfiguration() (*RegisteredPlan, error) {
	if s == nil {
		return nil, ErrInvalid
	}
	return s.registeredPlan()
}

// Successor constructs a candidate from actual bundle bytes without changing the
// registry. The target, preparation ID and protected journal cannot change.
func (s *Service) Successor(b *Bundle) (*Service, error) {
	if s == nil || s.closed.Load() {
		return nil, ErrInvalid
	}
	next, e := NewService(b, s.plan.Target, s.dir)
	if e != nil {
		return nil, e
	}
	if next.plan.Manifest.ID != s.plan.Manifest.ID {
		next.Close()
		return nil, ErrInvalid
	}
	return next, nil
}
func (s *Service) PreparationID() string {
	if s == nil {
		return ""
	}
	return s.plan.Manifest.ID
}

func (s *Service) MatchesTarget(t Target) bool {
	return s != nil && targetDigest(s.plan.Target) == targetDigest(t)
}
