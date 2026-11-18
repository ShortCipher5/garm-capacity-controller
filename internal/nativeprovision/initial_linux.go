//go:build linux

package nativeprovision

import (
	"context"
	"encoding/json"
	"os"
)

// InitialPlan binds the FIRST real assembled bundle to an approved configuration
// registration. RegistrationDigest is NOT a prior preparation plan or authority.
type InitialPlan struct {
	Version            string `json:"version"`
	PreparationID      string `json:"preparation_id"`
	TargetDigest       string `json:"target_digest"`
	RegistrationDigest string `json:"registration_digest"`
	SelectionDigest    string `json:"selection_digest"`
	ReviewDigest       string `json:"review_digest"`
}

func (s *Service) initialPlan() (*InitialPlan, error) {
	raw, e := s.read(s.plan.Manifest.ID + ".initial.json")
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, ErrInvalid
	}
	var r InitialPlan
	if json.Unmarshal(raw, &r) != nil {
		return nil, ErrInvalid
	}
	canonical, _ := json.Marshal(r)
	if string(canonical) != string(raw) || r.Version != "native-initial-plan/v1" || r.PreparationID != s.plan.Manifest.ID || r.TargetDigest != targetDigest(s.plan.Target) || !registryHash(r.RegistrationDigest) || !registryHash(r.SelectionDigest) || !registryHash(r.ReviewDigest) {
		return nil, ErrInvalid
	}
	return &r, nil
}

// ReadInitialPlan is read-only recovery metadata. A caller must load and verify
// the actual completed bundle before constructing a usable service.
func ReadInitialPlan(directory, id string, t Target) (*InitialPlan, error) {
	if !registryHash(id) || protected(directory, uint32(os.Geteuid())) != nil {
		return nil, ErrInvalid
	}
	s := &Service{dir: directory, uid: uint32(os.Geteuid())}
	s.plan.Manifest.ID = id
	s.plan.Target = t
	return s.initialPlan()
}
func (s *Service) RegisterInitialPlan(ctx context.Context, registration, selection string) (InitialPlan, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || s.closed.Load() || !registryHash(registration) || !registryHash(selection) {
		return InitialPlan{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, e := s.lock()
	if e != nil {
		return InitialPlan{}, e
	}
	defer lock.Close()
	if ctx.Err() != nil {
		return InitialPlan{}, ErrInvalid
	}
	for _, suffix := range []string{".intent.json", ".receipt.json"} {
		if _, e = s.read(s.plan.Manifest.ID + suffix); !os.IsNotExist(e) {
			return InitialPlan{}, ErrInvalid
		}
	}
	n, e := s.latestSequence()
	if e != nil || n != 0 {
		return InitialPlan{}, ErrInvalid
	}
	revised, e := s.registeredPlan()
	if e != nil || revised != nil {
		return InitialPlan{}, ErrInvalid
	}
	want := InitialPlan{"native-initial-plan/v1", s.plan.Manifest.ID, targetDigest(s.plan.Target), registration, selection, s.ReviewDigest()}
	old, e := s.initialPlan()
	if e != nil {
		return InitialPlan{}, e
	}
	if old != nil {
		if *old == want {
			return *old, nil
		}
		return InitialPlan{}, ErrInvalid
	}
	raw, _ := json.Marshal(want)
	if s.write(s.plan.Manifest.ID+".initial.json", raw) != nil {
		return InitialPlan{}, ErrUnknown
	}
	return want, nil
}
