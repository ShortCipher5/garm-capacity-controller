//go:build linux

package nativeassembly

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryStatusReadsActualDurableHistoryWithoutCreatingFiles(t *testing.T) {
	s, _, recipes, saved := fixture(t)
	r := recipes["host"]
	ctx := context.Background()
	status, e := s.Status(ctx, "host", saved.SelectionDigest)
	if e != nil || status.State != "never_attempted" || status.PartialOutputPresent || status.Assembly != nil || status.ActivationAuthorized {
		t.Fatal(status, e)
	}
	files, e := os.ReadDir(r.OutputParent)
	if e != nil || len(files) != 0 {
		t.Fatal("status created state", e)
	}
	if _, e = s.Status(ctx, "host", strings.Repeat("e", 64)); e == nil {
		t.Fatal("unknown selection reported as unattempted")
	}
	completed, e := s.Build(ctx, "host", saved.SelectionDigest)
	if e != nil {
		t.Fatal(e)
	}
	status, e = s.Status(ctx, "host", saved.SelectionDigest)
	if e != nil || status.State != "completed_bundle" || status.Assembly == nil || *status.Assembly != completed.Report() || status.PartialOutputPresent || status.ActivationAuthorized {
		t.Fatal(status, e)
	}
	if e = os.WriteFile(filepath.Join(completed.BundlePath(), "capsule.json"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("changed bytes promoted as completed")
	}
}
func TestRecoveryStatusUnknownOrOrphanNeverAllowsRetry(t *testing.T) {
	s, _, recipes, saved := fixture(t)
	r := recipes["host"]
	ctx := context.Background()
	input := filepath.Join(r.Build.PrivateDirectory, "owner-server.key")
	original, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(input); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Build(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("missing private input built")
	}
	status, e := s.Status(ctx, "host", saved.SelectionDigest)
	if e != nil || status.State != "assembly_unknown" || status.PartialOutputPresent || status.Assembly != nil {
		t.Fatal(status, e)
	}
	if e = os.WriteFile(input, original, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Build(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("unknown attempt replayed after repair")
	}
	if e = os.Mkdir(r.output(saved.SelectionDigest), 0700); e != nil {
		t.Fatal(e)
	}
	status, e = s.Status(ctx, "host", saved.SelectionDigest)
	if e != nil || status.State != "assembly_unknown" || !status.PartialOutputPresent {
		t.Fatal(status, e)
	}
	if e = os.WriteFile(filepath.Join(r.OutputParent, r.stem(saved.SelectionDigest)+".receipt.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("malformed receipt treated as unknown or complete")
	}
}
func TestRecoveryStatusRefusesOrphanAndBusySnapshots(t *testing.T) {
	s, _, recipes, saved := fixture(t)
	r := recipes["host"]
	ctx := context.Background()
	s.mu.Lock()
	_, e := s.Status(ctx, "host", saved.SelectionDigest)
	s.mu.Unlock()
	if e == nil {
		t.Fatal("busy snapshot accepted")
	}
	if e = os.Mkdir(r.output(saved.SelectionDigest), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Status(ctx, "host", saved.SelectionDigest); e == nil {
		t.Fatal("orphan output reported never attempted")
	}
}
