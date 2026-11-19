//go:build linux

package nativepreflight

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func fixture() inputs {
	return inputs{kernel: "6.8.0-test", arch: "x86_64", uid: 0, cgroup: func() bool { return true }, read: func(p string) (string, error) {
		switch p {
		case "/proc/self/uid_map":
			return "0 0 4294967295", nil
		case "/proc/self/status":
			return "CapEff:\tffffffffffffffff\n", nil
		case "/sys/fs/cgroup/cgroup.controllers":
			return "cpu memory pids io", nil
		case "/sys/module/zfs/version":
			return "2.2.6-1", nil
		}
		return "", fmt.Errorf("unavailable")
	}}
}
func TestMatchedPrerequisitesCannotQualify(t *testing.T) {
	r, e := collect(context.Background(), fixture())
	if e != nil || r.State != "unqualified_no_activation" {
		t.Fatal(r, e)
	}
	for _, c := range r.Checks {
		if c.Name == "execution_boundary" && c.State != "unqualified" {
			t.Fatal(c)
		}
	}
}
func TestMissingPrivilege(t *testing.T) {
	in := fixture()
	in.uid = 1000
	in.kernel = "6.9.0"
	in.read = func(string) (string, error) { return "", fmt.Errorf("denied") }
	r, e := collect(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "cap_") || c.Name == "effective_uid" {
			if c.State != "missing_or_unsupported" {
				t.Fatal(c)
			}
		}
	}
}
func TestDuplicateCapsAndCancellation(t *testing.T) {
	if _, ok := statusField("CapEff: 1\nCapEff: f\n", "CapEff"); ok {
		t.Fatal("duplicate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := collect(ctx, fixture()); e == nil {
		t.Fatal("cancel")
	}
	if _, e := readFixed("/etc/passwd"); e == nil {
		t.Fatal("arbitrary path")
	}
}

func TestLibraryMatchIsNotABIOrRuntimeQualification(t *testing.T) {
	in := fixture()
	in.library = func() (string, string, error) {
		return "d6f72b1de7484cc42fa5c84e5b0429f2f544ebd0e38367b08ee5cffbd482f5c2", "GLIBC_2.43", nil
	}
	r, e := collect(context.Background(), in)
	if e != nil || r.State != "unqualified_no_activation" {
		t.Fatal(r, e)
	}
	seen := false
	for _, c := range r.Checks {
		if c.Name == "lxc_required_glibc" {
			seen = true
			if c.State != "observed_requirement_only" || c.Observed != "GLIBC_2.43" {
				t.Fatal(c)
			}
		}
	}
	if !seen {
		t.Fatal("missing ABI report")
	}
}

func TestHostVersionsAreObservationsNotCompatibilityGates(t *testing.T) {
	for _, kernel := range []string{"6.8.0", "6.12.57+deb13", "6.18.57", "7.0.0"} {
		for _, zfs := range []string{"2.2.6-1", "2.3.4", "future-vendor"} {
			in := fixture()
			in.kernel = kernel
			read := in.read
			in.read = func(path string) (string, error) {
				if path == "/sys/module/zfs/version" {
					return zfs, nil
				}
				return read(path)
			}
			r, err := collect(context.Background(), in)
			if err != nil || r.State != "unqualified_no_activation" {
				t.Fatal(r, err)
			}
			seen := 0
			for _, c := range r.Checks {
				switch c.Name {
				case "kernel_release", "zfs_module_version":
					seen++
					want := kernel
					if c.Name == "zfs_module_version" {
						want = zfs
					}
					if c.State != "informational" || c.Observed != want {
						t.Fatal(c)
					}
				case "zfs_module_presence":
					seen++
					if c.State != "informational" || c.Observed != "present" {
						t.Fatal(c)
					}
				case "incus_version":
					seen++
					if c.State != "not_exercised" || c.Observed != "" {
						t.Fatal(c)
					}
				}
			}
			if seen != 4 {
				t.Fatal("missing version observations", r)
			}
		}
	}
}

func TestUnavailableVersionsDoNotImplyCompatibility(t *testing.T) {
	in := fixture()
	in.kernel = ""
	in.read = func(string) (string, error) { return "partial", fmt.Errorf("unavailable") }
	r, err := collect(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range r.Checks {
		if c.Name == "zfs_module_presence" {
			seen++
			if c.State != "unavailable" || c.Observed != "" {
				t.Fatal(c)
			}
		}
		if c.Name == "kernel_release" || c.Name == "zfs_module_version" {
			seen++
			if c.State != "unavailable" || c.Observed != "" {
				t.Fatal(c)
			}
		}
	}
	if seen != 3 {
		t.Fatal("missing unavailable checks", r)
	}
}
