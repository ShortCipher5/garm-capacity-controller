//go:build linux

// Package nativecapsule constructs a fresh paused nspawn namespace for explicit
// provisioning. Staged is not native execution or physical capacity authority.
package nativecapsule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedevice"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeids"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"golang.org/x/sys/unix"
)

var ErrCapsule = errors.New("native capsule construction incomplete; retain birth and do not retry")

const StagePath = "/usr/lib/garm-capacity/native-stage"
const BootstrapSocket = "/run/garm-capacity/bootstrap.sock"

type Configuration struct{ Nspawn, NspawnSHA256, Launcher, LauncherSHA256, Image, ImageSHA256, StageSHA256, Registry, StateParent string }
type launchSpec struct {
	Image  string `json:"image"`
	Socket string `json:"socket"`
	Birth  string `json:"birth"`
	Start  uint64 `json:"start"`
	Count  uint64 `json:"count"`
}
type Staged struct {
	peer      *capsulepeer.Peer
	conn      *net.UnixConn
	birth     nativeids.Record
	directory string
	limits    nativeboundary.Observation
	stageSHA  string
	imageSHA  string
	devices   nativedevice.Boundary
	partition *birthPartition
	artifacts nativezfs.HostArtifactRoot
}

func (s *Staged) Peer() *capsulepeer.Peer {
	if s == nil {
		return nil
	}
	return s.peer
}
func (s *Staged) Birth() nativeids.Record {
	if s == nil {
		return nativeids.Record{}
	}
	return s.birth
}

// Close closes only observation/control descriptors. No signal, process kill,
// namespace destruction, ZFS rollback or ID reuse is attempted.
func (s *Staged) Close() {
	if s == nil {
		return
	}
	if s.conn != nil {
		s.conn.Close()
	}
	if s.peer != nil {
		s.peer.Close()
	}
	s.partition.close()
}

// Start creates a fresh child cgroup and launches a pinned nspawn binary into it
// from birth using clone3/CLONE_INTO_CGROUP. It returns only after the pinned
// image's PID1 helper connects through the fresh private Unix socket and its
// actual kernel namespace/credential identity has been checked. No Incus starts
// here, and no network bridge/uplink is created on the host.
func Start(ctx context.Context, c Configuration, limits nativeboundary.Observation, devices nativedevice.Boundary) (*Staged, error) {
	return nil, ErrCapsule // A finite birth partition is mandatory; no legacy fallback.
}

func StartWithPartition(ctx context.Context, c Configuration, limits nativeboundary.Observation, devices nativedevice.Boundary, budgets capsulepeer.PartitionSpec) (*Staged, error) {
	return nil, ErrCapsule // The actual host artifact root is mandatory.
}
func startWithArtifacts(ctx context.Context, c Configuration, limits nativeboundary.Observation, devices nativedevice.Boundary, budgets capsulepeer.PartitionSpec, artifacts nativezfs.HostArtifactRoot) (*Staged, error) {
	if _, e := artifacts.Observe(ctx); e != nil {
		return nil, ErrCapsule
	}
	if budgets.Validate(limits.Limits()) != nil {
		return nil, ErrCapsule
	}
	if ctx == nil || ctx.Err() != nil || devices.Recheck(ctx) != nil || devices.Record().CgroupID != limits.RootInode() || devices.Record().NamespaceID != limits.NamespaceInode() || os.Geteuid() != 0 || !limits.Valid() || !hash(c.StageSHA256) || !safe(c.Image) || !safe(c.StateParent) || secure(c.StateParent, true) != nil {
		return nil, ErrCapsule
	}
	actual, e := nativeboundary.Observe(limits.Limits())
	if e != nil || actual.NamespaceInode() != limits.NamespaceInode() || actual.RootInode() != limits.RootInode() {
		return nil, ErrCapsule
	}
	nspawn, e := pinned(c.Nspawn, c.NspawnSHA256, 128<<20, true)
	if e != nil {
		return nil, e
	}
	defer nspawn.Close()
	helper, e := pinned(c.Launcher, c.LauncherSHA256, 128<<20, true)
	if e != nil {
		return nil, e
	}
	defer helper.Close()
	if probeNspawn(ctx, nspawn, helper) != nil {
		return nil, ErrCapsule
	}
	image, e := pinned(c.Image, c.ImageSHA256, 16<<30, false)
	if e != nil {
		return nil, e
	}
	image.Close()
	ids, e := nativeids.Reserve(c.Registry)
	if e != nil {
		return nil, ErrCapsule
	}
	birth := ids.Record()
	// nspawn257 supports one identical UID/GID shift, never a guessed mapping.
	if birth.UIDStart != birth.GIDStart {
		return nil, ErrCapsule
	}
	directory := filepath.Join(c.StateParent, birth.BirthID)
	if os.Mkdir(directory, 0700) != nil || syncDir(c.StateParent) != nil {
		return nil, ErrCapsule
	}
	socket := filepath.Join(directory, "bootstrap.sock")
	if len(socket) >= 104 {
		return nil, ErrCapsule
	}
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if e != nil {
		return nil, ErrCapsule
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if os.Chmod(socket, 0600) != nil || os.Chown(socket, int(birth.UIDStart), int(birth.GIDStart)) != nil {
		return nil, ErrCapsule
	}
	spec := launchSpec{c.Image, socket, birth.BirthID, birth.UIDStart, birth.Count}
	specRaw, _ := json.Marshal(spec)
	specPath := filepath.Join(directory, "launch.json")
	sf, e := os.OpenFile(specPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, ErrCapsule
	}
	defer sf.Close()
	if _, e = sf.Write(specRaw); e != nil || sf.Sync() != nil || syncDir(directory) != nil {
		return nil, ErrCapsule
	}
	if _, e = sf.Seek(0, 0); e != nil {
		return nil, ErrCapsule
	}
	partition, e := preparePartition(directory, birth.BirthID, budgets, limits)
	if e != nil {
		return nil, ErrCapsule
	}
	partitionOK := false
	defer func() {
		if !partitionOK {
			partition.close()
		}
	}()
	if partition.delegate(uint32(birth.UIDStart), uint32(birth.GIDStart)) != nil {
		return nil, ErrCapsule
	}
	cg := partition.groups["bootstrap"]
	pidfd := -1
	cmd := exec.Command("/proc/self/fd/4")
	cmd.ExtraFiles = []*os.File{nspawn, helper, sf}
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd, Setsid: true, UseCgroupFD: true, CgroupFD: cg}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "SYSTEMD_NSPAWN_UNIFIED_HIERARCHY=1", "SYSTEMD_NSPAWN_USE_CGNS=0"}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if ctx.Err() != nil || cmd.Start() != nil {
		return nil, ErrCapsule
	}
	// No CommandContext: cancellation must never kill a surviving native domain.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	if pidfd < 0 {
		return nil, ErrCapsule
	}
	defer unix.Close(pidfd)
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		if ctx.Err() != nil || time.Now().After(deadline) || !pidLive(pidfd) {
			return nil, ErrCapsule
		}
		select {
		case <-done:
			return nil, ErrCapsule
		default:
		}
		listener.SetDeadline(time.Now().Add(time.Second))
		conn, e := listener.AcceptUnix()
		if e != nil {
			if ne, ok := e.(net.Error); ok && ne.Timeout() {
				continue
			}
			return nil, ErrCapsule
		}
		conn.SetDeadline(deadline)
		peer, e := capsulepeer.ObserveManaged(conn, ids, c.StageSHA256, limits)
		if e != nil {
			conn.Close()
			return nil, ErrCapsule
		}
		// This listener admits exactly one consumed birth, never a reconnect/adoption.
		var hello struct {
			Version int `json:"version"`
		}
		raw, e := readLine(conn, 128)
		if e != nil || string(raw) != "{\"version\":1}\n" || json.Unmarshal(raw, &hello) != nil || !pidLive(pidfd) || !peer.Paused() {
			conn.Close()
			peer.Close()
			return nil, ErrCapsule
		}
		conn.SetDeadline(time.Time{})
		if partition.recheck(ctx) != nil {
			peer.Close()
			conn.Close()
			return nil, ErrCapsule
		}
		partitionOK = true
		return &Staged{peer: peer, conn: conn, birth: birth, directory: directory, limits: limits, stageSHA: c.StageSHA256, imageSHA: c.ImageSHA256, devices: devices, partition: partition, artifacts: artifacts}, nil
	}
}
func pidLive(fd int) bool {
	v := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, e := unix.Poll(v, 0)
	return e == nil && n == 0
}
func readLine(r io.Reader, max int) ([]byte, error) {
	b := []byte{}
	one := []byte{0}
	for len(b) < max {
		if _, e := io.ReadFull(r, one); e != nil {
			return nil, e
		}
		b = append(b, one[0])
		if one[0] == '\n' {
			return b, nil
		}
	}
	return nil, ErrCapsule
}
func hash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func safe(s string) bool {
	return filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsAny(s, ":\n\r\x00")
}
func secure(path string, dir bool) error {
	if !safe(path) {
		return ErrCapsule
	}
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
			return ErrCapsule
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 {
			return ErrCapsule
		}
		if current != path || dir {
			if !st.IsDir() {
				return ErrCapsule
			}
		}
	}
	return nil
}
func pinned(path, digest string, max int64, executable bool) (*os.File, error) {
	if !hash(digest) || secure(path, false) != nil {
		return nil, ErrCapsule
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrCapsule
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > max || (executable && st.Mode().Perm()&0111 == 0) {
		f.Close()
		return nil, ErrCapsule
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != digest {
		f.Close()
		return nil, ErrCapsule
	}
	if _, e = f.Seek(0, 0); e != nil {
		f.Close()
		return nil, ErrCapsule
	}
	return f, nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func args(s launchSpec) ([]string, error) {
	if !safe(s.Image) || !safe(s.Socket) || !hash(s.Birth) || s.Start == 0 || s.Count != nativeids.BlockSize || s.Start > 1<<32-1 || s.Count > 1<<32-1-s.Start {
		return nil, ErrCapsule
	}
	return []string{"systemd-nspawn", "--settings=no", "--register=no", "--keep-unit", "--private-users=" + strconv.FormatUint(s.Start, 10) + ":" + strconv.FormatUint(s.Count, 10), "--private-users-ownership=map", "--private-network", "--read-only", "--image=" + s.Image, "--machine=" + s.Birth, "--console=passive", "--link-journal=no", "--resolv-conf=off", "--timezone=off", "--suppress-sync=no", "--bind=" + s.Socket + ":" + BootstrapSocket, "--bind=/dev/zfs:/dev/zfs", "--bind=/sys/fs/cgroup:/sys/fs/cgroup", StagePath}, nil
}

// ExecLauncher runs only in the dedicated root helper with fixed descriptors.
// It strips ambient descriptors/environment and never accepts arbitrary argv.
func ExecLauncher() error {
	if os.Geteuid() != 0 {
		return ErrCapsule
	}
	if len(os.Args) == 2 && os.Args[1] == "--probe-nspawn-version" {
		var st unix.Stat_t
		if unix.Fstat(3, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
			return ErrCapsule
		}
		return syscall.Exec("/proc/self/fd/3", []string{"systemd-nspawn", "--version"}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"})
	}
	if len(os.Args) != 1 {
		return ErrCapsule
	}
	for _, fd := range []int{3, 5} {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 {
			return ErrCapsule
		}
	}
	f := os.NewFile(5, "launch-spec")
	raw, e := io.ReadAll(io.LimitReader(f, 8193))
	if e != nil || len(raw) > 8192 {
		return ErrCapsule
	}
	var s launchSpec
	if json.Unmarshal(raw, &s) != nil {
		return ErrCapsule
	}
	canonical, _ := json.Marshal(s)
	if string(canonical) != string(raw) {
		return ErrCapsule
	}
	argv, e := args(s)
	if e != nil {
		return e
	}
	if unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrCapsule
	}
	return syscall.Exec("/proc/self/fd/3", argv, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "SYSTEMD_NSPAWN_UNIFIED_HIERARCHY=1", "SYSTEMD_NSPAWN_USE_CGNS=0"})
}

// Probe starts no capsule and runs only the pinned vendor binary's version mode.
// Its separate sanitized helper prevents ambient listener inheritance.
func probeNspawn(ctx context.Context, binary, helper *os.File) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", "--probe-nspawn-version")
	cmd.ExtraFiles = []*os.File{binary, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	out, e := cmd.Output()
	if e != nil || !supportedNspawn(out) {
		return ErrCapsule
	}
	return nil
}
func supportedNspawn(raw []byte) bool {
	if len(raw) > 8192 {
		return false
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	return line == "systemd 257" || strings.HasPrefix(line, "systemd 257 (")
}
