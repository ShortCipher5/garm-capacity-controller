//go:build linux

package nativecapacity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, values map[string]string) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	for name, value := range values {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}
func base() map[string]string {
	return map[string]string{
		"cpu.max": "max 100000", "memory.max": "max", "memory.swap.max": "max", "pids.max": "max",
		"runner/cgroup.type": "domain", "runner/cpu.max": "250000 100000", "runner/memory.max": "8589934592", "runner/memory.swap.max": "0", "runner/pids.max": "512",
		"runner/job/cgroup.type": "domain", "runner/job/cpu.max": "max 100000", "runner/job/memory.max": "max", "runner/job/memory.swap.max": "max", "runner/job/pids.max": "1024",
	}
}
func TestVisibleAncestorQuotasAreNotLeafDefaults(t *testing.T) {
	r, _ := fixture(t, base())
	got, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUQuotaCores != (Limit{"5/2", "runner"}) || got.MemoryBytes.Value != "8589934592" || got.SwapBytes.Value != "0" || got.PIDs.Value != "512" || len(got.Missing) != 1 || len(got.VisibleAncestors) != 3 {
		t.Fatalf("%+v", got)
	}
}
func TestTightestQuotaIsAcrossDifferentPeriodsAndExact(t *testing.T) {
	data := base()
	data["cpu.max"] = "1 3"
	data["runner/job/cpu.max"] = "2 5"
	r, _ := fixture(t, data)
	got, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUQuotaCores != (Limit{"1/3", "."}) {
		t.Fatalf("%+v", got)
	}
}
func TestMissingControlIsExplicitNotUnlimited(t *testing.T) {
	data := base()
	delete(data, "memory.max")
	delete(data, "runner/memory.swap.max")
	r, _ := fixture(t, data)
	got, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Missing) != 3 || got.SwapBytes.Value != "" || got.MemoryBytes.Value != "8589934592" {
		t.Fatalf("%+v", got)
	}
}
func TestQuotaDoesNotAccountForSiblingAllocationOrMembership(t *testing.T) {
	data := base()
	r, dir := fixture(t, data)
	before, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	// A sibling may spend the entire parent quota, and a foreign daemon/VM may
	// reside outside this tree. Neither condition changes these configuration facts.
	if err := os.Mkdir(filepath.Join(dir, "foreign"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runner", "memory.current"), []byte("8589934592\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	if before.MemoryBytes != after.MemoryBytes {
		t.Fatal("quota observations confused with available capacity")
	}
}
func TestHiddenMountAncestorCannotBeInferred(t *testing.T) {
	data := base()
	r, dir := fixture(t, data)
	sub, err := os.OpenRoot(filepath.Join(dir, "runner"))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	whole, err := readVisible(r.Open, "runner/job")
	if err != nil {
		t.Fatal(err)
	}
	visible, err := readVisible(sub.Open, "job")
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.VisibleAncestors) != 3 || len(visible.VisibleAncestors) != 2 {
		t.Fatal("fixture did not hide ancestor")
	}
	// Identical apparent bound does not certify visibility of the physical root.
	if whole.MemoryBytes.Value != visible.MemoryBytes.Value {
		t.Fatal("fixture")
	}
}
func TestMalformedOrUnsupportedFailsWithoutPartialResult(t *testing.T) {
	for _, s := range []string{"", "0 100000", "1 0", "-1 2", "1  2", "1\t2", "max", "1 2 3", "18446744073709551616 1", strings.Repeat("9", 257)} {
		t.Run(s, func(t *testing.T) {
			data := base()
			data["runner/cpu.max"] = s
			r, _ := fixture(t, data)
			got, err := readVisible(r.Open, "runner/job")
			if err == nil || len(got.VisibleAncestors) != 0 {
				t.Fatalf("accepted %q or leaked partial: %+v %v", s, got, err)
			}
		})
	}
	for _, kind := range []string{"threaded", "domain threaded", "domain invalid", ""} {
		data := base()
		data["runner/cgroup.type"] = kind
		r, _ := fixture(t, data)
		if _, err := readVisible(r.Open, "runner/job"); err == nil {
			t.Fatalf("accepted %q", kind)
		}
	}
}
func TestPublicReaderRejectsOrdinaryFilesystemAndTraversal(t *testing.T) {
	_, dir := fixture(t, base())
	if _, err := Read(dir, "runner/job"); err == nil {
		t.Fatal("synthetic files mistaken for native cgroup")
	}
	for _, target := range []string{".", "../runner", "/runner", "runner/../job", "runner//job"} {
		if _, err := Read(dir, target); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
}
func TestNumericBoundDoesNotOverflow(t *testing.T) {
	got, err := parseLimit("18446744073709551615 1", true)
	if err != nil || got.RatString() != "18446744073709551615" {
		t.Fatalf("%v %v", got, err)
	}
	for _, s := range []string{"-1", "+1", "1.0", "18446744073709551616", "1 "} {
		if _, err := parseLimit(s, false); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func TestReadFailureAndDeepTreeRefusePartialObservation(t *testing.T) {
	r, _ := fixture(t, base())
	denied := func(name string) (*os.File, error) {
		if name == "runner/memory.max" {
			return nil, os.ErrPermission
		}
		return r.Open(name)
	}
	if got, err := readVisible(denied, "runner/job"); err == nil || len(got.VisibleAncestors) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	data := map[string]string{}
	dir := ""
	for i := 0; i < maxAncestors+1; i++ {
		if dir != "" {
			dir += "/"
		}
		dir += "a"
		data[dir+"/cgroup.type"] = "domain"
	}
	deep, _ := fixture(t, data)
	if got, err := readVisible(deep.Open, dir); err == nil || len(got.VisibleAncestors) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRealRootTypeOmissionOnlyAcceptedAtVisibleRoot(t *testing.T) {
	data := base()
	delete(data, "cgroup.type")
	r, _ := fixture(t, data)
	got, err := readVisible(r.Open, "runner/job")
	if err != nil || len(got.VisibleAncestors) != 3 || len(got.Missing) == 0 || got.Missing[len(got.Missing)-1] != "cgroup.type" {
		t.Fatalf("%+v %v", got, err)
	}
	delete(data, "runner/cgroup.type")
	r2, _ := fixture(t, data)
	if _, err := readVisible(r2.Open, "runner/job"); err == nil {
		t.Fatal("missing non-root type accepted")
	}
	for _, kind := range []string{"threaded", "domain threaded", "garbage", ""} {
		data := base()
		data["cgroup.type"] = kind
		r, _ := fixture(t, data)
		if _, err := readVisible(r.Open, "runner/job"); err == nil {
			t.Fatalf("present invalid root type %q accepted", kind)
		}
	}
	r3, _ := fixture(t, base())
	denied := func(name string) (*os.File, error) {
		if name == "cgroup.type" {
			return nil, os.ErrPermission
		}
		return r3.Open(name)
	}
	if _, err := readVisible(denied, "runner/job"); err == nil {
		t.Fatal("root type permission error accepted")
	}
}
