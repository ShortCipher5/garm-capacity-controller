//go:build linux

// Package nativepreflight reports read-only prerequisites. Matching a version or
// privilege is not proof that the dormant native execution boundary is qualified.
package nativepreflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativelxc"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Check struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Observed string `json:"observed,omitempty"`
	Required string `json:"required"`
}
type Report struct {
	Version    string    `json:"version"`
	ObservedAt time.Time `json:"observed_at"`
	State      string    `json:"state"`
	Checks     []Check   `json:"checks"`
}
type inputs struct {
	library      func() (string, string, error)
	kernel, arch string
	uid          int
	read         func(string) (string, error)
	cgroup       func() bool
}

func Collect(ctx context.Context) (Report, error) {
	var u unix.Utsname
	if e := unix.Uname(&u); e != nil {
		return Report{}, e
	}
	return collect(ctx, inputs{kernel: unix.ByteSliceToString(u.Release[:]), arch: unix.ByteSliceToString(u.Machine[:]), uid: os.Geteuid(), read: readFixed, library: inspectLibrary, cgroup: func() bool {
		var s unix.Statfs_t
		return unix.Statfs("/sys/fs/cgroup", &s) == nil && s.Type == unix.CGROUP2_SUPER_MAGIC
	}})
}
func collect(ctx context.Context, in inputs) (Report, error) {
	if ctx == nil || ctx.Err() != nil {
		return Report{}, fmt.Errorf("preflight cancelled")
	}
	r := Report{Version: "native-host-preflight/v1", ObservedAt: time.Now().UTC(), State: "unqualified_no_activation"}
	add := func(name, observed, required string, ok bool) {
		state := "missing_or_unsupported"
		if ok {
			state = "observed_prerequisite_only"
		}
		r.Checks = append(r.Checks, Check{name, state, observed, required})
	}
	add("kernel_release", in.kernel, "Linux 6.8.x supported-source profile; vendor/kernel qualification still required", strings.HasPrefix(in.kernel, "6.8."))
	add("architecture", in.arch, "x86_64 for selected pinned LXC image profile", in.arch == "x86_64")
	add("effective_uid", strconv.Itoa(in.uid), "0 in initial host user namespace", in.uid == 0)
	uidMap, e := in.read("/proc/self/uid_map")
	f := strings.Fields(uidMap)
	add("uid_mapping", strings.Join(f, " "), "single 0 0 4294967295 mapping; not host-identity proof", e == nil && len(f) == 3 && f[0] == "0" && f[1] == "0" && f[2] == "4294967295")
	add("cgroup_filesystem", "", "cgroup v2 mounted at /sys/fs/cgroup", in.cgroup())
	controllers, e := in.read("/sys/fs/cgroup/cgroup.controllers")
	have := map[string]bool{}
	for _, v := range strings.Fields(controllers) {
		have[v] = true
	}
	add("cgroup_controllers", strings.Join(strings.Fields(controllers), " "), "cpu memory pids io", e == nil && have["cpu"] && have["memory"] && have["pids"] && have["io"])
	status, e := in.read("/proc/self/status")
	capValue, ok := statusField(status, "CapEff")
	caps, parseErr := strconv.ParseUint(capValue, 16, 64)
	for _, cap := range []struct {
		name string
		bit  uint
	}{{"CHOWN", 0}, {"DAC_OVERRIDE", 1}, {"SETGID", 6}, {"SETUID", 7}, {"NET_ADMIN", 12}, {"SYS_PTRACE", 19}, {"SYS_ADMIN", 21}, {"SYS_RESOURCE", 24}, {"MKNOD", 27}, {"BPF", 39}} {
		add("cap_"+strings.ToLower(cap.name), "", "effective CAP_"+cap.name, e == nil && ok && parseErr == nil && caps&(uint64(1)<<cap.bit) != 0)
	}
	version, e := in.read("/sys/module/zfs/version")
	version = strings.TrimSpace(version)
	add("zfs_module_version", version, "2.2.6 or 2.2.6 vendor suffix; hash/kernel ABI still require qualification", e == nil && (version == "2.2.6" || strings.HasPrefix(version, "2.2.6-")))
	lockdown, e := in.read("/sys/kernel/security/lockdown")
	if e == nil {
		r.Checks = append(r.Checks, Check{"kernel_lockdown", "informational", strings.TrimSpace(lockdown), "must permit qualified BPF/device/namespace operations; not exercised"})
	} else {
		r.Checks = append(r.Checks, Check{"kernel_lockdown", "unavailable", "", "absence is not proof privileges are sufficient"})
	}
	if in.library != nil {
		digest, glibc, err := in.library()
		add("lxc_library_sha256", digest, "reviewed Debian "+nativelxc.DebianPackage+" candidate at /"+nativelxc.DebianLibrary+"; runtime image checked separately", err == nil && digest == nativelxc.DebianLibrarySHA256)
		state := "unavailable"
		if err == nil {
			state = "observed_requirement_only"
		}
		r.Checks = append(r.Checks, Check{"lxc_required_glibc", state, glibc, "actual image must provide every imported ABI; no loader execution performed"})
	}
	r.Checks = append(r.Checks, Check{"native_artifacts", "not_exercised", "", "pinned Incus, LXC, nspawn, helpers and runtime image plus complete ABI closure"}, Check{"active_namespace_storage_network", "not_exercised", "", "actual isolated construction, ZFS zone, nftables and peer checks remain mandatory"}, Check{"host_memory_reserve", "unqualified", "", "explicit conservative system reserve; ARC targets are not hard ceilings"}, Check{"execution_boundary", "unqualified", "", "existing activation guard remains closed; this report cannot enable execution"})
	if ctx.Err() != nil {
		return Report{}, ctx.Err()
	}
	return r, nil
}
func statusField(s, key string) (string, bool) {
	out := ""
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, key+":") {
			f := strings.Fields(strings.TrimPrefix(l, key+":"))
			if len(f) != 1 {
				return "", false
			}
			out = f[0]
			n++
		}
	}
	return out, n == 1
}
func readFixed(path string) (string, error) {
	// No caller paths or external binaries. O_NOFOLLOW rejects final-file
	// aliases; intermediate fixed paths are not a confinement proof. Filesystem
	// validation rejects ordinary files masquerading as kernel diagnostics.
	allowed := map[string]int64{"/proc/self/uid_map": unix.PROC_SUPER_MAGIC, "/proc/self/status": unix.PROC_SUPER_MAGIC, "/sys/fs/cgroup/cgroup.controllers": unix.CGROUP2_SUPER_MAGIC, "/sys/module/zfs/version": unix.SYSFS_MAGIC, "/sys/kernel/security/lockdown": unix.SECURITYFS_MAGIC}
	want, ok := allowed[path]
	if !ok {
		return "", fmt.Errorf("unsupported preflight input")
	}
	// /proc/self intentionally resolves to this process. All other paths have
	// fixed kernel-generated locations; O_NOFOLLOW prevents final-file aliases.
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil || int64(fs.Type) != want {
		return "", fmt.Errorf("unexpected preflight filesystem")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 {
		return "", fmt.Errorf("bounded preflight read failed")
	}
	return string(raw), nil
}

// inspectLibrary inspects bytes only. A matching vendor library is not proof that
// the isolated image contains it, maps it or satisfies its imported ABI.
func inspectLibrary() (string, string, error) {
	path := "/" + nativelxc.DebianLibrary
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return "", "", e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0022 != 0 || st.Size != nativelxc.DebianLibrarySize {
		return "", "", fmt.Errorf("unsupported library metadata")
	}
	raw, e := io.ReadAll(io.LimitReader(f, nativelxc.DebianLibrarySize+1))
	if e != nil || len(raw) != nativelxc.DebianLibrarySize {
		return "", "", fmt.Errorf("bounded library read failed")
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if digest != nativelxc.DebianLibrarySHA256 {
		return digest, "", fmt.Errorf("library hash mismatch")
	}
	lib, e := elf.NewFile(bytes.NewReader(raw))
	if e != nil {
		return digest, "", e
	}
	defer lib.Close()
	symbols, e := lib.ImportedSymbols()
	if e != nil {
		return digest, "", e
	}
	versions := map[string]bool{}
	for _, symbol := range symbols {
		if strings.HasPrefix(symbol.Version, "GLIBC_") {
			versions[symbol.Version] = true
		}
	}
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)
	return digest, strings.Join(names, ","), nil
}
