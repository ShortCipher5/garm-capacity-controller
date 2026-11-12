//go:build linux && amd64

// Package m2parent compiles/inspects VM-only source. It NEVER links or runs a
// native parent, invokes cgroups/clone/exec, or qualifies any native boundary.
package m2parent

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
func compileOnly(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "new-objects")
	command := exec.Command("/bin/sh", filepath.Join(repository(t), "integration/m2-native-parent/build-objects.sh"), out)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile-only failed: %v %s", err, output)
	}
	return out
}
func TestNativeParentCompileOnlyNotExecutable(t *testing.T) {
	out := compileOnly(t)
	for _, name := range []string{"native-parent.o", "embedded-witness.o"} {
		path := filepath.Join(out, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() <= 0 || info.Size() > 262144 {
			t.Fatal("unsafe object file shape")
		}
		file, err := elf.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if file.Type != elf.ET_REL || file.Machine != elf.EM_X86_64 || file.Class != elf.ELFCLASS64 || file.Entry != 0 || len(file.Progs) != 0 {
			file.Close()
			t.Fatal("parent objects became runnable ELF")
		}
		if name == "native-parent.o" {
			symbols, err := file.Symbols()
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			allowed := map[string]bool{"_binary_witness_entry_witness_start": true, "_binary_witness_entry_witness_end": true}
			found := map[string]bool{}
			for _, symbol := range symbols {
				if symbol.Section == elf.SHN_UNDEF && symbol.Name != "" {
					if !allowed[symbol.Name] {
						file.Close()
						t.Fatalf("unexpected runtime dependency %s", symbol.Name)
					}
					found[symbol.Name] = true
				}
			}
			if len(found) != 2 {
				file.Close()
				t.Fatal("missing fixed-blob linkage contract")
			}
		} else {
			section := file.Section(".rodata")
			if section == nil || section.Flags&(elf.SHF_WRITE|elf.SHF_EXECINSTR) != 0 {
				file.Close()
				t.Fatal("embedded witness not readonly data")
			}
			actual, err := section.Data()
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join(out, "witness/entry-witness"))
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			if !bytes.Equal(actual, expected) {
				file.Close()
				t.Fatal("wrong embedded payload")
			}
		}
		file.Close()
	}
	manifest, err := os.ReadFile(filepath.Join(out, "manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"parent_linked=false\n", "parent_executed=false\n", "qualification=none\n", "execution_authority=none\n"} {
		if !bytes.Contains(manifest, []byte(boundary)) {
			t.Fatal("manifest qualification boundary missing")
		}
	}
	for _, name := range []string{"native-parent.o", "embedded-witness.o", "witness/entry-witness"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if !bytes.Contains(manifest, []byte(name+"_sha256="+hex.EncodeToString(digest[:])+"\n")) {
			t.Fatal("object manifest mismatch")
		}
	}
	for _, source := range []struct{ file, field string }{{"parent.c", "parent_source_sha256"}, {"build-objects.sh", "recipe_source_sha256"}} {
		data, err := os.ReadFile(filepath.Join(repository(t), "integration/m2-native-parent", source.file))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if !bytes.Contains(manifest, []byte(source.field+"="+hex.EncodeToString(digest[:])+"\n")) {
			t.Fatal("source manifest mismatch")
		}
	}
}
func TestNativeParentStaticNoFallbackSingleCloneAndNoActor(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repository(t), "integration/m2-native-parent/parent.c"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	allowed := map[string]bool{"exit": true, "clock_gettime": true, "close": true, "statx": true, "fcntl": true, "poll": true, "write": true, "read": true, "openat": true, "fstatfs": true, "memfd_create": true, "pwrite64": true, "fchmod": true, "pipe2": true, "waitid": true, "dup3": true, "close_range": true, "execveat": true, "clone3": true}
	expression := regexp.MustCompile(`sys6\(__NR_([a-z0-9_]+),`)
	matches := expression.FindAllStringSubmatch(source, -1)
	seen := map[string]int{}
	for _, match := range matches {
		if !allowed[match[1]] {
			t.Fatalf("unreviewed native syscall %s", match[1])
		}
		seen[match[1]]++
	}
	if len(seen) != len(allowed) || strings.Count(source, "sys6(") != len(matches)+1 || strings.Count(source, `"syscall"`) != 1 {
		t.Fatal("unreviewed syscall dispatch shape")
	}
	if seen["clone3"] != 1 || seen["execveat"] != 1 {
		t.Fatal("native retry/fallback path appeared")
	}
	for _, required := range []string{"args.flags=CLONE_INTO_CGROUP | CLONE_PIDFD;", "args.cgroup=TARGET_CGROUP;", "MFD_CLOEXEC | MFD_ALLOW_SEALING | MFD_EXEC", "F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_EXEC | F_SEAL_SEAL", "P_PIDFD", "CLOEXEC_RANGE", "birth_qualified=false", "execution_authority=none"} {
		if !strings.Contains(source, required) {
			t.Fatalf("native contract missing %s", required)
		}
	}
	if strings.Contains(source, "int main(") || strings.Contains(source, "void _start(") {
		t.Fatal("host-executable bootstrap added")
	}
	recipe, err := os.ReadFile(filepath.Join(repository(t), "integration/m2-native-parent/build-objects.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(recipe, []byte(" -c ")) || bytes.Contains(recipe, []byte("-Wl,")) {
		t.Fatal("default parent build now links")
	}
	// This is source regression evidence, NOT syscall execution, a kernel proof or
	// authorization to run/link a native parent in this or any other environment.
}
func TestNativeParentNewOnlyObjectsPreserveExisting(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, link, dangling} {
		command := exec.Command("/bin/sh", filepath.Join(repository(t), "integration/m2-native-parent/build-objects.sh"), path)
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		if err := command.Run(); err == nil {
			t.Fatal("preexisting output accepted")
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("existing state overwritten")
	}
}
