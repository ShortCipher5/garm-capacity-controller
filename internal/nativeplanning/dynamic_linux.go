//go:build linux

package nativeplanning

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
)

// NewDynamicService explicitly permits no configured targets. It grants no
// registration authority: callers must persist reviewed registration first.
// The ordinary NewService continues to reject an empty binding set.
func NewDynamicService(inventory *sshinventory.Service, directory string) (*Service, error) {
	if inventory == nil || protectedDirectory(directory) != nil {
		return nil, ErrSelection
	}
	return &Service{inventory: inventory, bindings: map[string]Binding{}, cache: map[string]cached{}, directory: directory, dynamic: true}, nil
}

// AddBinding installs only a new immutable configuration, after checking the
// actual current inventory target. Exact repeats are idempotent; changes refuse.
// This is not an observation or a permission to operate on the native endpoint.
func (s *Service) AddBinding(ctx context.Context, id string, b Binding) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrSelection
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dynamic || !label.MatchString(id) || id != b.InventoryTarget.ID || ValidateBinding(b) != nil || s.current(ctx, b) != nil {
		return ErrSelection
	}
	if old, ok := s.bindings[id]; ok {
		if old == b {
			return nil
		}
		return ErrSelection
	}
	if len(s.bindings) >= 8 {
		return ErrSelection
	}
	for _, old := range s.bindings {
		if old.PreparationID == b.PreparationID || old.Host == b.Host {
			return ErrSelection
		}
	}
	if ctx.Err() != nil {
		return ErrSelection
	}
	s.bindings[id] = b
	return nil
}
