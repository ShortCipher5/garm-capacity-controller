//go:build linux

package nativecapsule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedns"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeids"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"golang.org/x/sys/unix"
)

const RuntimeRoot = "/var/lib/garm-capacity-native"
const NativePath = "/usr/libexec/incus/incusd"
const QuotaPath = "/usr/lib/garm-capacity/quota-read"
const ZFSPath = "/usr/sbin/zfs"

// bootSpec is internal parent-to-pinned-image protocol, not an HTTP API or a
// caller-provided proof. Parent release must come from a genuine fresh ZFS Birth.
type bootSpec struct {
	Partition         capsulepeer.PartitionSpec `json:"partition"`
	ParentLimits      nativeboundary.Limits     `json:"parent_limits"`
	DNSResolvers      []string                  `json:"dns_resolvers"`
	Version           int                       `json:"version"`
	BirthID           string                    `json:"birth_id"`
	Dataset           string                    `json:"dataset"`
	DatasetGUID       string                    `json:"dataset_guid"`
	RuntimeGUID       string                    `json:"runtime_guid"`
	PoolGUID          string                    `json:"pool_guid"`
	QuotaBytes        int64                     `json:"quota_bytes"`
	ZFSSHA256         string                    `json:"zfs_sha256"`
	QuotaHelperSHA256 string                    `json:"quota_helper_sha256"`
	NativeSHA256      string                    `json:"native_sha256"`
}

func validBoot(b bootSpec) bool {
	if _, e := nativedns.Content(b.DNSResolvers); e != nil {
		return false
	}
	if b.Partition.Validate(b.ParentLimits) != nil || b.Version != 3 || !hash(b.BirthID) || !strings.HasSuffix(b.Dataset, "/domain-"+b.BirthID) || b.QuotaBytes <= 0 || !hash(b.ZFSSHA256) || !hash(b.QuotaHelperSHA256) || !hash(b.NativeSHA256) {
		return false
	}
	for _, p := range strings.Split(b.Dataset, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for i, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '_' || r == '.')) {
				return false
			}
		}
	}
	for _, v := range []string{b.DatasetGUID, b.RuntimeGUID, b.PoolGUID} {
		n, e := strconv.ParseUint(v, 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != v {
			return false
		}
	}
	return b.DatasetGUID != b.RuntimeGUID && b.DatasetGUID != b.PoolGUID && b.RuntimeGUID != b.PoolGUID
}

// RunStage runs only as PID1 in the pinned read-only capsule image. It does not
// fork jobs, create datasets, choose an uplink, or weaken host limits. It waits
// for the parent's one-use provisioning release, then verifies actual delegated
// storage identity/quota and execs only the pinned native daemon.
func RunStage() error {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 || len(os.Args) != 1 {
		return ErrCapsule
	}
	uid, e := os.ReadFile("/proc/self/uid_map")
	if e != nil || !nonHostMap(uid) {
		return ErrCapsule
	}
	gid, e := os.ReadFile("/proc/self/gid_map")
	if e != nil || !nonHostMap(gid) {
		return ErrCapsule
	}
	conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: BootstrapSocket, Net: "unix"})
	if e != nil {
		return ErrCapsule
	}
	defer conn.Close()
	raw, e := conn.SyscallConn()
	if e != nil {
		return ErrCapsule
	}
	var cred *unix.Ucred
	var ce error
	if raw.Control(func(fd uintptr) { cred, ce = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }) != nil || ce != nil || cred == nil || cred.Pid != 0 || cred.Uid != 65534 || cred.Gid != 65534 {
		return ErrCapsule
	}
	conn.SetDeadline(time.Now().Add(15 * time.Minute))
	if _, e = conn.Write([]byte("{\"version\":1}\n")); e != nil {
		return ErrCapsule
	}
	body, e := readLine(conn, 8192)
	if e != nil {
		return ErrCapsule
	}
	var b bootSpec
	if json.Unmarshal(body, &b) != nil || !validBoot(b) {
		return ErrCapsule
	}
	canonical, _ := json.Marshal(b)
	if string(body) != string(canonical)+"\n" {
		return ErrCapsule
	}
	if placeDaemon(b) != nil {
		return ErrCapsule
	}
	if verifyResolverImage(b.DNSResolvers) != nil {
		return ErrCapsule
	}
	reader, e := nativequota.New(nativequota.Configuration{Binary: ZFSPath, BinarySHA256: b.ZFSSHA256, Helper: QuotaPath, HelperSHA256: b.QuotaHelperSHA256})
	if e != nil {
		return ErrCapsule
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parent, e := reader.Read(ctx, b.Dataset, b.QuotaBytes)
	if e != nil || parent.GUID() != b.DatasetGUID {
		return ErrCapsule
	}
	runtime, e := reader.ReadFilesystem(ctx, b.Dataset+"/runtime")
	if e != nil || runtime.GUID() != b.RuntimeGUID || runtime.Quota() != 0 {
		return ErrCapsule
	}
	pool, e := reader.ReadFilesystem(ctx, b.Dataset+"/pool")
	if e != nil || pool.GUID() != b.PoolGUID || pool.Quota() != 0 {
		return ErrCapsule
	}
	// The image supplies an empty real mountpoint, never a host bind path.
	if secure(RuntimeRoot, true) != nil {
		return ErrCapsule
	}
	entries, e := os.ReadDir(RuntimeRoot)
	if e != nil || len(entries) != 0 {
		return ErrCapsule
	}
	var before unix.Stat_t
	if unix.Stat(RuntimeRoot, &before) != nil {
		return ErrCapsule
	}
	if unix.Mount(b.Dataset+"/runtime", RuntimeRoot, "zfs", unix.MS_NODEV|unix.MS_NOSUID, "") != nil {
		return ErrCapsule
	}
	var after unix.Stat_t
	var fs unix.Statfs_t
	if unix.Stat(RuntimeRoot, &after) != nil || unix.Statfs(RuntimeRoot, &fs) != nil || fs.Type != 0x2fc12fc1 || after.Dev == before.Dev || after.Uid != 0 || after.Gid != 0 || after.Mode&0777 != 0700 {
		return ErrCapsule
	}
	// A mount is not a GUID proof; re-read the actual delegated object as well.
	runtime, e = reader.ReadFilesystem(ctx, b.Dataset+"/runtime")
	if e != nil || runtime.GUID() != b.RuntimeGUID || runtime.Quota() != 0 {
		return ErrCapsule
	}
	parent, e = reader.Read(ctx, b.Dataset, b.QuotaBytes)
	if e != nil || parent.GUID() != b.DatasetGUID {
		return ErrCapsule
	}
	for _, name := range []string{"incus", "tmp"} {
		if os.Mkdir(filepath.Join(RuntimeRoot, name), 0700) != nil {
			return ErrCapsule
		}
	}
	native, e := pinned(NativePath, b.NativeSHA256, 128<<20, true)
	if e != nil {
		return ErrCapsule
	}
	defer native.Close()
	if _, e = conn.Write([]byte("{\"version\":1,\"state\":\"native_exec_pending\"}\n")); e != nil {
		return ErrCapsule
	}
	// The acknowledgement is NOT daemon readiness. Parent must observe exact
	// native executable + Unix peer + API identity before using the domain.
	target := fmt.Sprintf("/proc/self/fd/%d", native.Fd())
	if unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrCapsule
	}
	return syscall.Exec(target, []string{"incusd"}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "INCUS_DIR=" + RuntimeRoot + "/incus", "TMPDIR=" + RuntimeRoot + "/tmp", "INCUS_EXEC_PATH=/proc/self/exe"})
}
func nonHostMap(raw []byte) bool {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		return false
	}
	f := strings.Fields(lines[0])
	if len(f) != 3 || f[0] != "0" || f[2] != strconv.FormatUint(nativeids.BlockSize, 10) {
		return false
	}
	n, e := strconv.ParseUint(f[1], 10, 32)
	return e == nil && n > 0 && n <= 1<<32-1-nativeids.BlockSize && strconv.FormatUint(n, 10) == f[1]
}

// The image resolver is immutable and must match the actual egress birth, not
// the host's resolver, a caller path or a symlink into a writable mount.
func verifyResolverImage(resolvers []string) error {
	want, e := nativedns.Content(resolvers)
	if e != nil || secure("/etc", true) != nil {
		return ErrCapsule
	}
	st, e := os.Lstat("/etc/resolv.conf")
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0444 || st.Sys().(*syscall.Stat_t).Uid != 0 {
		return ErrCapsule
	}
	f, e := os.OpenFile("/etc/resolv.conf", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return ErrCapsule
	}
	defer f.Close()
	got, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || !bytes.Equal(got, want) {
		return ErrCapsule
	}
	return nil
}
