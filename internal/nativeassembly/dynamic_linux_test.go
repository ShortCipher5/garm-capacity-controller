//go:build linux

package nativeassembly

import (
	"context"
	"sync"
	"testing"
)

func TestDynamicAssemblyActualSavedSelectionWithoutFakeBaseline(t *testing.T) {
	static, planning, recipes, saved := fixture(t)
	if _, e := NewService(planning, nil); e == nil {
		t.Fatal("static empty allowed")
	}
	s, e := NewDynamicService(planning)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Build(context.Background(), "host", saved.SelectionDigest); e == nil {
		t.Fatal("built absent recipe")
	}
	r := recipes["host"]
	if static.AddRecipe(context.Background(), "host", r) == nil {
		t.Fatal("static mutation")
	}
	bad := r
	bad.Binding.Host = "wrong"
	if s.AddRecipe(context.Background(), "host", bad) == nil {
		t.Fatal("unbound recipe")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.AddRecipe(context.Background(), "host", r); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	changed := r
	changed.Build.ExportManifestSHA256 = "different"
	if s.AddRecipe(context.Background(), "host", changed) == nil {
		t.Fatal("recipe replaced")
	}
	result, e := s.Build(context.Background(), "host", saved.SelectionDigest)
	if e != nil || result.Report().ActivationAuthorized {
		t.Fatal(e)
	}
	if _, e = s.InspectCompleted(context.Background(), "host", saved.SelectionDigest); e != nil {
		t.Fatal(e)
	}
	if s.AddRecipe(context.Background(), "host", changed) == nil {
		t.Fatal("completed recipe replaced")
	}
}
