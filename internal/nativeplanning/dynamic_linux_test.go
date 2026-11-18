//go:build linux

package nativeplanning

import (
	"context"
	"os"
	"sync"
	"testing"
)

func TestDynamicPlanningActualInventoryImmutableBindingAndProbe(t *testing.T) {
	inventory, b := actualInventory(t)
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".dynamic-planning-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	if _, e = NewService(inventory, nil, dir); e == nil {
		t.Fatal("static empty allowed")
	}
	s, e := NewDynamicService(inventory, dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Bindings()) != 0 {
		t.Fatal("fake initial binding")
	}
	id := b.InventoryTarget.ID
	bad := b
	bad.InventoryTarget.ReviewDigest = "wrong"
	if s.AddBinding(context.Background(), id, bad) == nil {
		t.Fatal("unverified target")
	}
	if e = s.AddBinding(context.Background(), id, b); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.AddBinding(context.Background(), id, b) != nil {
				t.Error("exact repeat")
			}
			copy := s.Bindings()
			delete(copy, id)
		}()
	}
	wg.Wait()
	changed := b
	changed.Host = "different"
	if s.AddBinding(context.Background(), id, changed) == nil {
		t.Fatal("replacement")
	}
	if len(s.Bindings()) != 1 {
		t.Fatal("mutable view")
	}
	p, e := s.Probe(context.Background(), id, Digest(b), "22222222-2222-4222-8222-222222222222")
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.Save(context.Background(), id, p.ReviewDigest, p.Defaults)
	if e != nil || saved.Selection.Binding != b {
		t.Fatal(saved, e)
	}
	static, e := NewService(inventory, map[string]Binding{id: b}, dir)
	if e != nil {
		t.Fatal(e)
	}
	if static.AddBinding(context.Background(), id, b) == nil {
		t.Fatal("static mutation")
	}
}
