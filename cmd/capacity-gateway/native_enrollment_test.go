package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeEnrollmentPrivateRecipesRejectAmbiguity(t *testing.T) {
	for _, raw := range []string{`[]`, `[{"id":"x","mode":"automatic","owners":[]}]`, `[{"id":"x","id":"y","mode":"initial","owners":[]}]`, `[{"id":"x","mode":"initial","endpoint":"https://caller.invalid","owners":[]}]`} {
		path := filepath.Join(t.TempDir(), "recipes.json")
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := loadNativeEnrollmentRecipes(path); e == nil {
			t.Fatal("ambiguous recipe loaded", raw)
		}
	}
}
