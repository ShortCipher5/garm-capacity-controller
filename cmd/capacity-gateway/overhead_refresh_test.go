package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerCollectionRecipesRejectMissingPrivateSources(t *testing.T) {
	p := filepath.Join(t.TempDir(), "recipes")
	for _, raw := range []string{`[]`, `[{"id":"owner","proof":{}}]`, `[{"id":"owner","targets":[{"binding_sequence":1,"kind":"both","endpoint":"https://private.invalid","tls":{"ca":"/missing","certificate":"/missing","key":"/missing"}}]}]`} {
		if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := loadOverheadRecipes(p); e == nil {
			t.Fatal("unsafe recipe accepted")
		}
	}
	if e := os.Chmod(p, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := loadOverheadRecipes(p); e == nil {
		t.Fatal("public config accepted")
	}
}
