package main

import (
	"context"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRetirementRequiresSeparateExplicitConfiguration(t *testing.T) {
	good := configuration{EnableNativeRetirement: true, Admin: &adminConfiguration{}, NativeRetirement: &nativeRetirementConfiguration{IntervalSeconds: 60, Owners: []nativeStockHostConfiguration{{}}}}
	if validateNativeRetirement(good) != nil {
		t.Fatal("configuration shape")
	}
	for _, change := range []func(*configuration){func(c *configuration) { c.EnableNativeRetirement = false }, func(c *configuration) { c.Admin = nil }, func(c *configuration) { c.NativeRetirement = nil }, func(c *configuration) { v := *c.NativeRetirement; v.Owners = nil; c.NativeRetirement = &v }, func(c *configuration) { v := *c.NativeRetirement; v.IntervalSeconds = 0; c.NativeRetirement = &v }} {
		c := good
		change(&c)
		if validateNativeRetirement(c) == nil {
			t.Fatal("invalid retirement configuration")
		}
	}
	if s, e := buildNativeRetirement(context.Background(), configuration{}, nil); s != nil || e != nil {
		t.Fatal("implicit retirement", e)
	}
}
func TestNativeRetirementRuntimeRefusesBeforeKeysAndListeners(t *testing.T) {
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	db, id := filepath.Join(dir, "db"), filepath.Join(dir, "id")
	if e := admission.InitializeSQLite(db, id, claimTestPolicy()); e != nil {
		t.Fatal(e)
	}
	s, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := configuration{EnableNativeRetirement: true, Admin: &adminConfiguration{}, NativeRetirement: &nativeRetirementConfiguration{IntervalSeconds: 60, Owners: []nativeStockHostConfiguration{{OwnerTLS: securefiles.TLSFiles{CA: "/missing", Certificate: "/missing", Key: "/missing"}}}}}
	before, e := s.Snapshot(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if service, e := buildNativeRetirement(t.Context(), c, s); service != nil || !errors.Is(e, nativecontroller.ErrUnqualified) {
		t.Fatal("runtime did not refuse first", e)
	}
	if e := serve(t.Context(), c, false, false); !errors.Is(e, nativecontroller.ErrUnqualified) {
		t.Fatal("listener guard changed", e)
	}
	after, e := s.Snapshot(t.Context())
	if e != nil || len(after.Audit) != len(before.Audit) {
		t.Fatal("refusal changed ledger", e)
	}
}
