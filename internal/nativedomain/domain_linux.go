//go:build linux

// Package nativedomain owns birth of a fresh private Incus daemon domain. It does
// not grant allocation, native-mutation, retirement or capacity-release authority.
package nativedomain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var ErrDomain = errors.New("native domain: fresh root-owned domain required")
var ErrUnknown = errors.New("native domain: launch consumed; outcome unknown; no retry or cleanup permitted")

// Birth is durable domain provenance, not proof of physical capacity or native
// operation completion. A consumed launch is never automatically retried.
type Birth struct {
	LauncherSHA256   string    `json:"launcher_sha256"`
	Version          int       `json:"version"`
	ID               string    `json:"id"`
	BinarySHA256     string    `json:"binary_sha256"`
	LaunchConsumedAt time.Time `json:"launch_consumed_at"`
}

// Domain has no exported constructor from observations or operator claims.
// Root and the installed Incus binary/dependencies are trusted. Unprivileged
// users and guests must never be given its socket, directory or HTTP transport.
type Domain struct {
	done            chan struct{}
	pidfd           int
	pidfdMu         sync.Mutex
	birth           Birth
	directory       string
	pid             int
	startTime       string
	dead            atomic.Bool
	clientClaimed   atomic.Bool
	cgroupNamespace uint64
	transport       *http.Transport
}

// ClaimClient permanently binds this live domain object to one native adapter.
// It is only an in-process lifecycle guard, never proof of host exclusivity.
// It cannot be reset and there is no constructor adopting a surviving daemon.
func (d *Domain) ClaimClient() error {
	if d == nil || d.dead.Load() || !d.clientClaimed.CompareAndSwap(false, true) {
		return ErrDomain
	}
	return nil
}

// StartFresh creates a new domain (never adopts a directory, socket or daemon),
// consumes launch durably, and starts the root-owned incusd executable exactly
// once. Parent must already exist; every ancestor must be root-owned and not
// group/other-writable or a symlink. The child gets a closed environment, no
// inherited listeners and no command/configuration arguments. Incus's default
// fresh database has no remote listener. The private parent protects even an
// Incus-created group-readable socket. No project or existing host is adopted.
//
// Cancellation, startup failure and Close NEVER kill the daemon, delete files,
// retry launch, release capacity, or interrupt instances. On failure after
// creation the directory is deliberately retained, and subsequent calls refuse
// it. Reconnection/restart is intentionally unsupported until separately fenced.
// This does not initialize storage/profiles, expose a general native proxy, or
// establish safe guest policy; callers must NOT enable provisioning from birth
// alone. Requires independently qualified pinned Incus and host packaging.
func StartFresh(ctx context.Context, directory, executable, launcher string) (*Domain, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if os.Geteuid() != 0 || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(directory)+len("/incus/unix.socket") >= 104 {
		return nil, ErrDomain
	}
	if err := securePath(filepath.Dir(directory), true); err != nil {
		return nil, err
	}
	binary, binaryHash, err := openRootExecutable(executable)
	if err != nil {
		return nil, err
	}
	defer binary.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	helper, helperHash, err := openRootExecutable(launcher)
	if err != nil {
		return nil, err
	}
	defer helper.Close()
	namespace, err := namespaceInode("/proc/self/ns/cgroup")
	if err != nil {
		return nil, ErrDomain
	}
	birth, err := consume(directory, binaryHash, helperHash)
	if err != nil {
		return nil, err
	}
	if os.Mkdir(filepath.Join(directory, "tmp"), 0700) != nil {
		return nil, ErrUnknown
	}
	// The open executable descriptor prevents pathname replacement between hash
	// and exec. Root changing the underlying inode is outside the threat model.
	cmd := exec.Command("/proc/self/fd/4")
	cmd.ExtraFiles = []*os.File{binary, helper}
	pidfd := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd, Setsid: true}
	cmd.Dir = directory
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "TMPDIR=" + filepath.Join(directory, "tmp"), "INCUS_DIR=" + filepath.Join(directory, "incus"), "INCUS_EXEC_PATH=/proc/self/exe"}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if ctx.Err() != nil {
		return nil, ErrUnknown
	}
	if err = cmd.Start(); err != nil {
		return nil, ErrUnknown
	}
	d := &Domain{cgroupNamespace: namespace, birth: birth, directory: directory, pid: cmd.Process.Pid, pidfd: pidfd, done: make(chan struct{})}
	if pidfd < 0 {
		go func() { _ = cmd.Wait() }()
		return nil, ErrUnknown
	}
	d.startTime, err = processStart(d.pid)
	if err != nil {
		go d.reap(cmd)
		return nil, ErrUnknown
	}
	go d.reap(cmd)
	d.transport = &http.Transport{Proxy: nil, DialContext: d.dial, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		connection, e := d.dial(waitCtx, "tcp", "native.invalid:80")
		if e == nil {
			_ = connection.Close()
			return d, nil
		}
		if d.dead.Load() || waitCtx.Err() != nil {
			d.Close()
			return nil, ErrUnknown
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			d.Close()
			return nil, ErrUnknown
		case <-timer.C:
		}
	}
}

func securePath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrDomain
	}
	current := "/"
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := -1; i < len(components); i++ {
		if i >= 0 && components[i] != "" {
			current = filepath.Join(current, components[i])
		}
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return ErrDomain
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return ErrDomain
		}
		if i < len(components)-1 || directory {
			if !info.IsDir() {
				return ErrDomain
			}
		} else if !info.Mode().IsRegular() {
			return ErrDomain
		}
	}
	return nil
}

func consume(directory, digest, launcherDigest string) (Birth, error) {
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Birth{}, ErrDomain
	}
	birth := Birth{Version: 1, ID: hex.EncodeToString(id[:]), BinarySHA256: digest, LaunchConsumedAt: time.Now().UTC()}
	birth.LauncherSHA256 = launcherDigest
	if err := os.Mkdir(directory, 0700); err != nil {
		return Birth{}, ErrDomain
	}
	// Even partial creation is deliberately never removed/reused.
	if err := syncDir(filepath.Dir(directory)); err != nil {
		return Birth{}, ErrUnknown
	}
	if err := os.Mkdir(filepath.Join(directory, "incus"), 0700); err != nil {
		return Birth{}, ErrUnknown
	}
	file, err := os.OpenFile(filepath.Join(directory, "birth.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Birth{}, ErrUnknown
	}
	err = json.NewEncoder(file).Encode(birth)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil || syncDir(directory) != nil {
		return Birth{}, ErrUnknown
	}
	return birth, nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func processStart(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", ErrUnknown
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", ErrUnknown
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", ErrUnknown
	}
	if _, err = strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", ErrUnknown
	}
	return fields[19], nil
}

func (d *Domain) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || !d.alive() || d.pid <= 0 || d.startTime == "" {
		return nil, ErrUnknown
	}
	start, err := processStart(d.pid)
	if err != nil || start != d.startTime {
		return nil, ErrUnknown
	}
	// No caller-supplied URL/host can select another daemon or network listener.
	socket := filepath.Join(d.directory, "incus", "unix.socket")
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return nil, ErrUnknown
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, ErrUnknown
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, ErrUnknown
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, ErrUnknown
	}
	var peer *syscall.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		peer, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || peerErr != nil || peer == nil || peer.Uid != 0 || int(peer.Pid) != d.pid || !d.alive() {
		conn.Close()
		return nil, ErrUnknown
	}
	observedNamespace, e := namespaceInode(fmt.Sprintf("/proc/%d/ns/cgroup", d.pid))
	if e != nil || observedNamespace != d.cgroupNamespace {
		conn.Close()
		return nil, ErrUnknown
	}
	start, err = processStart(d.pid)
	if err != nil || start != d.startTime {
		conn.Close()
		return nil, ErrUnknown
	}
	return conn, nil
}

func (d *Domain) Birth() Birth {
	if d == nil {
		return Birth{}
	}
	return d.birth
}

// RoundTrip accepts only the internal origin. It provides authenticated local
// transport, NOT authorization for arbitrary HTTP/native lifecycle requests.
// The containing broker must keep its own closed, validated native API surface.
func (d *Domain) RoundTrip(r *http.Request) (*http.Response, error) {
	if d == nil || d.transport == nil || r == nil || r.URL == nil || r.URL.Scheme != "http" || r.URL.Host != "native.invalid" || r.URL.User != nil || (r.Host != "" && r.Host != "native.invalid") {
		return nil, ErrDomain
	}
	return d.transport.RoundTrip(r)
}

// Close only closes idle client connections. It never stops the native daemon.
func (d *Domain) Close() {
	if d != nil && d.transport != nil {
		d.transport.CloseIdleConnections()
	}
}

func openRootExecutable(path string) (*os.File, string, error) {
	if securePath(path, false) != nil {
		return nil, "", ErrDomain
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, "", ErrDomain
	}
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 || i.Mode().Perm()&0111 == 0 || i.Sys().(*syscall.Stat_t).Uid != 0 {
		f.Close()
		return nil, "", ErrDomain
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		f.Close()
		return nil, "", ErrDomain
	}
	return f, hex.EncodeToString(h.Sum(nil)), nil
}
func (d *Domain) alive() bool {
	if d == nil {
		return false
	}
	d.pidfdMu.Lock()
	defer d.pidfdMu.Unlock()
	return !d.dead.Load() && d.pidfd >= 0 && unix.PidfdSendSignal(d.pidfd, 0, nil, 0) == nil
}
func (d *Domain) reap(cmd *exec.Cmd) {
	defer close(d.done)
	_ = cmd.Wait()
	d.dead.Store(true)
	d.pidfdMu.Lock()
	defer d.pidfdMu.Unlock()
	if d.pidfd >= 0 {
		_ = unix.Close(d.pidfd)
		d.pidfd = -1
	}
}

// Wait waits for daemon exit or owner shutdown. Neither outcome is completion
// evidence, permission to replay a mutation, nor capacity-release authority.
func (d *Domain) Wait(ctx context.Context) error {
	if d == nil || d.done == nil {
		return ErrUnknown
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.done:
		return ErrUnknown
	}
}

func namespaceInode(path string) (uint64, error) {
	st, e := os.Stat(path)
	if e != nil {
		return 0, ErrUnknown
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Ino == 0 {
		return 0, ErrUnknown
	}
	return s.Ino, nil
}

// Directory and CgroupNamespace expose immutable birth context for independent
// actual kernel/filesystem checks; neither value alone grants execution rights.
func (d *Domain) Directory() string {
	if d == nil {
		return ""
	}
	return d.directory
}
func (d *Domain) CgroupNamespace() uint64 {
	if d == nil {
		return 0
	}
	return d.cgroupNamespace
}
