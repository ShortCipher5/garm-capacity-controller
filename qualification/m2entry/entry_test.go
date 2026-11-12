//go:build linux && amd64

// Package m2entry tests a fixed POST-entry witness, never a native launcher.
package m2entry

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func sourceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
func build(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "new-build")
	command := exec.Command("/bin/sh", filepath.Join(sourceRoot(t), "integration/m2-entry-witness/build.sh"), out)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build-only compiler failed: %v %s", err, output)
	}
	artifact := filepath.Join(out, "entry-witness")
	validateELF(t, artifact)
	return artifact
}
func validateELF(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0500 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatal("unexpected artifact file mode")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data) > 65536 {
		t.Fatal("artifact size out of bounds")
	}
	binary, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	if binary.Type != elf.ET_EXEC || binary.Machine != elf.EM_X86_64 || binary.Class != elf.ELFCLASS64 {
		t.Fatal("unsupported ELF ABI")
	}
	stackSeen, entrySeen := false, false
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			t.Fatal("unexpected loader/runtime")
		}
		if program.Type == elf.PT_GNU_STACK {
			stackSeen = true
			if program.Flags&elf.PF_X != 0 {
				t.Fatal("executable stack")
			}
		}
		if program.Type == elf.PT_LOAD {
			if program.Flags&(elf.PF_W|elf.PF_X) == elf.PF_W|elf.PF_X {
				t.Fatal("writable executable segment")
			}
			if program.Flags&elf.PF_X != 0 && binary.Entry >= program.Vaddr && binary.Entry < program.Vaddr+program.Memsz {
				entrySeen = true
			}
		}
	}
	if !stackSeen || !entrySeen {
		t.Fatal("missing entry/stack policy")
	}
	symbols, err := binary.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range symbols {
		if symbol.Section == elf.SHN_UNDEF && symbol.Name != "" {
			t.Fatal("undefined runtime symbol")
		}
	}
	manifest, err := os.ReadFile(filepath.Join(filepath.Dir(path), "manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if !bytes.Contains(manifest, []byte("artifact_sha256="+hex.EncodeToString(digest[:]))) || !bytes.Contains(manifest, []byte("qualification=none\nexecution_authority=none\n")) {
		t.Fatal("manifest mismatch")
	}
	for _, item := range []struct{ file, field string }{{"entry.S", "entry_source_sha256"}, {"witness.c", "witness_source_sha256"}} {
		data, err := os.ReadFile(filepath.Join(sourceRoot(t), "integration/m2-entry-witness", item.file))
		if err != nil {
			t.Fatal(err)
		}
		sourceDigest := sha256.Sum256(data)
		if !bytes.Contains(manifest, []byte(item.field+"="+hex.EncodeToString(sourceDigest[:])+"\n")) {
			t.Fatal("source manifest mismatch")
		}
	}
}

func TestM2EntryBuildOnlyELFAndSourceSyscallContract(t *testing.T) {
	build(t) // The artifact is NOT executed by this test.
	source, err := os.ReadFile(filepath.Join(sourceRoot(t), "integration/m2-entry-witness/witness.c"))
	if err != nil {
		t.Fatal(err)
	}
	expression := regexp.MustCompile(`call6\(__NR_([a-z0-9_]+),`)
	matches := expression.FindAllSubmatch(source, -1)
	allowed := map[string]bool{"exit": true, "clock_gettime": true, "statx": true, "fcntl": true, "poll": true, "write": true, "getpid": true, "getgroups": true, "prctl": true, "getresuid": true, "getresgid": true, "capget": true, "read": true}
	seen := map[string]bool{}
	for _, match := range matches {
		name := string(match[1])
		if !allowed[name] {
			t.Fatalf("unreviewed syscall %s", name)
		}
		seen[name] = true
	}
	if len(seen) != len(allowed) || strings.Count(string(source), "call6(") != len(matches)+1 || strings.Count(string(source), `"syscall"`) != 1 {
		t.Fatal("unreviewed syscall dispatch shape")
	}
	if strings.Count(string(source), "PR_GET_NO_NEW_PRIVS") != 1 || strings.Contains(string(source), "PR_SET_") {
		t.Fatal("unexpected privilege mutation")
	}
	entry, err := os.ReadFile(filepath.Join(sourceRoot(t), "integration/m2-entry-witness/entry.S"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(entry), "syscall") != 1 || !bytes.Contains(entry, []byte("mov $60, %eax")) {
		t.Fatal("unreviewed entry syscall")
	}
}

func TestM2EntryBuildRefusesExistingAndSymlinkOutputs(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(existing, symlink); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, symlink, dangling} {
		command := exec.Command("/bin/sh", filepath.Join(sourceRoot(t), "integration/m2-entry-witness/build.sh"), path)
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		if err := command.Run(); err == nil {
			t.Fatal("preexisting output accepted")
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("sentinel modified")
	}
}

func requireExecution(t *testing.T) {
	t.Helper()
	if os.Getenv("GCC_M2_ENTRY_WITNESS_TESTS") != "1" {
		t.Skip("post-entry witness execution not requested; no launch qualification")
	}
}

type child struct {
	command        *exec.Cmd
	report         *os.File
	gate           *os.File
	done           chan error
	finished       bool
	stdout, stderr bytes.Buffer
}

func start(t *testing.T, artifact string, extra []*os.File) *child {
	t.Helper()
	c := &child{done: make(chan error, 1)}
	c.command = exec.Command(artifact, "--ignored-not-a-command")
	c.command.Env = []string{"PATH=/no-tools", "HOME=/no-home", "LD_PRELOAD=/no-library", "LD_LIBRARY_PATH=/no-libraries", "BASH_ENV=/no-shell", "NODE_OPTIONS=--require /no-module"}
	c.command.Stdin = bytes.NewReader(nil)
	c.command.Stdout = &c.stdout
	c.command.Stderr = &c.stderr
	c.command.ExtraFiles = extra
	if err := c.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { c.done <- c.command.Wait() }()
	t.Cleanup(func() {
		if c.gate != nil {
			c.gate.Close()
		}
		if c.report != nil {
			c.report.Close()
		}
		if !c.finished {
			select {
			case <-c.done:
				c.finished = true
			case <-time.After(7 * time.Second):
				t.Error("owned witness did not naturally terminate; no kill/retry attempted")
			}
		}
	})
	return c
}
func ownedChild(t *testing.T, artifact string) *child {
	t.Helper()
	reportReader, reportWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		reportReader.Close()
		reportWriter.Close()
		t.Fatal(err)
	}
	c := start(t, artifact, []*os.File{reportWriter, gateReader})
	c.report = reportReader
	c.gate = gateWriter
	reportWriter.Close()
	gateReader.Close()
	return c
}
func report(t *testing.T, c *child) map[string]string {
	t.Helper()
	if err := c.report.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(io.LimitReader(c.report, 1025))
	header, err := reader.ReadString('\n')
	if err != nil || header != "garm-m2-post-entry-witness/v1\n" {
		t.Fatal("missing post-entry header")
	}
	fields := map[string]string{}
	total := len(header)
	for {
		line, err := reader.ReadString('\n')
		total += len(line)
		if err != nil || total > 1024 {
			t.Fatal("invalid bounded report")
		}
		if line == "end\n" {
			break
		}
		pair := strings.SplitN(strings.TrimSuffix(line, "\n"), "=", 2)
		if len(pair) != 2 || fields[pair[0]] != "" {
			t.Fatal("invalid report field")
		}
		fields[pair[0]] = pair[1]
	}
	expected := []string{"pid", "ruid", "euid", "suid", "rgid", "egid", "sgid", "groups_count", "no_new_privs", "cap_inheritable", "cap_permitted", "cap_effective", "birth_qualified", "execution_authority"}
	if len(fields) != len(expected) {
		t.Fatal("unexpected field count")
	}
	for _, key := range expected {
		if fields[key] == "" {
			t.Fatal("missing report field")
		}
	}
	if fields["birth_qualified"] != "false" || fields["execution_authority"] != "none" {
		t.Fatal("unexpected witness authority")
	}
	for _, key := range expected[:9] {
		value, err := strconv.ParseUint(fields[key], 10, 32)
		if err != nil || strconv.FormatUint(value, 10) != fields[key] {
			t.Fatal("invalid numeric observation")
		}
	}
	for _, key := range expected[9:12] {
		data, err := hex.DecodeString(fields[key])
		if err != nil || len(data) != 8 || strings.ToLower(fields[key]) != fields[key] {
			t.Fatal("invalid capability observation")
		}
	}
	return fields
}
func wait(t *testing.T, c *child, code int) {
	t.Helper()
	var err error
	select {
	case err = <-c.done:
		c.finished = true
	case <-time.After(7 * time.Second):
		t.Fatal("bounded owned child completion unavailable; no kill/retry")
	}
	if code == 0 {
		if err != nil {
			t.Fatalf("fixed witness exit failed: %v", err)
		}
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != code {
			t.Fatalf("unexpected fixed witness exit: %v", err)
		}
	}
	if c.stdout.Len() != 0 || c.stderr.Len() != 0 {
		t.Fatal("witness wrote outside report channel")
	}
}

func TestM2EntryOwnedExecutionPostEntryOnly(t *testing.T) {
	requireExecution(t)
	artifact := build(t)
	c := ownedChild(t, artifact)
	fields := report(t, c)
	if fields["pid"] != strconv.Itoa(c.command.Process.Pid) || fields["ruid"] != strconv.Itoa(os.Getuid()) || fields["euid"] != strconv.Itoa(os.Geteuid()) || fields["rgid"] != strconv.Itoa(os.Getgid()) || fields["egid"] != strconv.Itoa(os.Getegid()) {
		t.Fatal("self observation mismatch")
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fields["suid"] != strconv.Itoa(os.Geteuid()) || fields["sgid"] != strconv.Itoa(os.Getegid()) || fields["groups_count"] != strconv.Itoa(len(groups)) || fields["no_new_privs"] != strconv.Itoa(nnp) {
		t.Fatal("ordinary non-set-ID exec observation mismatch")
	}
	select {
	case <-c.done:
		c.finished = true
		t.Fatal("witness exited before parent gate")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := c.gate.Write([]byte{0xa5}); err != nil {
		t.Fatal(err)
	}
	wait(t, c, 0)
	// This is post-entry liveness of a fixed helper, NOT atomic birth, sandbox,
	// no other accepted work, installed artifact or job retirement evidence.
}
func TestM2EntryOwnedGateEOFInvalidAndTimeoutRefuse(t *testing.T) {
	requireExecution(t)
	artifact := build(t)
	for _, test := range []struct {
		name string
		code int
	}{{"eof", 114}, {"invalid", 115}, {"timeout", 114}} {
		t.Run(test.name, func(t *testing.T) {
			c := ownedChild(t, artifact)
			report(t, c)
			switch test.name {
			case "eof":
				c.gate.Close()
			case "invalid":
				if _, err := c.gate.Write([]byte{0}); err != nil {
					t.Fatal(err)
				}
			}
			wait(t, c, test.code)
		})
	}
}
func TestM2EntryOwnedNonPipeAndSamePipeRefuseBeforeReport(t *testing.T) {
	requireExecution(t)
	artifact := build(t)
	regular, err := os.CreateTemp(t.TempDir(), "unchanged")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := regular.WriteString("sentinel"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"regular-report", "regular-gate", "same-pipe", "wrong-direction"} {
		t.Run(test, func(t *testing.T) {
			firstReader, firstWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer firstReader.Close()
			defer firstWriter.Close()
			secondReader, secondWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer secondReader.Close()
			defer secondWriter.Close()
			extras := []*os.File{firstWriter, secondReader}
			switch test {
			case "regular-report":
				extras[0] = regular
			case "regular-gate":
				extras[1] = regular
			case "same-pipe":
				extras[1] = firstReader
			case "wrong-direction":
				extras[0] = firstReader
			}
			c := start(t, artifact, extras)
			wait(t, c, 111)
		})
	}
	data, err := os.ReadFile(regular.Name())
	if err != nil || string(data) != "sentinel" {
		t.Fatal("non-pipe sentinel modified")
	}
}
