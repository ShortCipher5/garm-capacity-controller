//go:build linux

package nativeassembly

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
)

// NewDynamicService is the explicit pending-target mode. It creates no fake
// Bundle, preparation Plan or readiness. Static NewService still rejects empty.
func NewDynamicService(planning *nativeplanning.Service) (*Service, error) {
	if planning == nil {
		return nil, ErrAssembly
	}
	return &Service{planning: planning, recipes: map[string]Recipe{}, dynamic: true}, nil
}

// AddRecipe follows the same assembly->planning lock order as Build/Inspect.
// The durable registration service must first record and verify materialization.
// No existing recipe can be replaced, including after an uncertain build.
func (s *Service) AddRecipe(ctx context.Context, id string, r Recipe) error {
	if s == nil || ctx == nil || ctx.Err() != nil {
		return ErrAssembly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dynamic {
		return ErrAssembly
	}
	assets, e := nativebundle.FreezeAssetReference(r.Build.RuntimeAssets, r.Build.Architecture)
	if e != nil {
		return ErrAssembly
	}
	r.Build.RuntimeAssets = assets
	b, ok := s.planning.Bindings()[id]
	if !ok || nativeplanning.Digest(b) != nativeplanning.Digest(r.Binding) || nativeplanning.ValidateBinding(b) != nil || r.Build.ID != b.PreparationID || r.Build.SelectionFile != "" || r.Build.ExpectedSelectionDigest != "" || r.Build.ExpectedBindingFile == "" || r.Build.OutputDirectory != "" || privateDirectory(r.OutputParent) != nil {
		return ErrAssembly
	}
	if old, ok := s.recipes[id]; ok {
		if nativeplanning.Digest(old) == nativeplanning.Digest(r) {
			return nil
		}
		return ErrAssembly
	}
	if len(s.recipes) >= 8 || ctx.Err() != nil {
		return ErrAssembly
	}
	s.recipes[id] = r
	return nil
}
