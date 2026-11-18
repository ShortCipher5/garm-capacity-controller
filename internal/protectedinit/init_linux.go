//go:build linux

// Package protectedinit implements an inert-by-default guest PID1 lifecycle.
// Process-tree exit is not job completion, native deletion, or release authority.
package protectedinit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrRefused = errors.New("protected runner init refused; retain unknown state")

// Config is an image-builder contract, not an operator assertion of authority.
// Domain and incarnation must later be bound by the native owner independently.
// The root-owned launcher must implement stock ephemeral runner bootstrap with
// immutable executable assets and separate writable runtime/work directories.
type Config struct {
	Domain      string `json:"domain"`
	Incarnation string `json:"incarnation"`
	Launcher    string `json:"launcher"`
	SHA256      string `json:"sha256"`
	UID         uint32 `json:"uid"`
	GID         uint32 `json:"gid"`
	Workdir     string `json:"workdir"`
	Journal     string `json:"journal"`
}
type Record struct {
	Domain         string    `json:"domain"`
	Incarnation    string    `json:"incarnation"`
	LauncherSHA256 string    `json:"launcher_sha256"`
	State          string    `json:"state"`
	At             time.Time `json:"at"`
	ExitCode       int       `json:"exit_code"`
	// Always false: neither process exit nor this journal proves a GitHub job ended.
	JobCompletionAuthorized bool `json:"job_completion_authorized"`
}

func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}

// rootFile rejects symlinks and writable path components. Trusted host/root
// mutation is out of scope; a hash is checked again on the opened descriptor.
func rootFile(path string, executable bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrRefused
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		current = filepath.Join(current, p)
		st, e := os.Lstat(current)
		if e != nil {
			return nil, ErrRefused
		}
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || s.Uid != 0 || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, ErrRefused
		}
		if i < len(parts)-1 && !st.IsDir() {
			return nil, ErrRefused
		}
		if i == len(parts)-1 && !st.Mode().IsRegular() {
			return nil, ErrRefused
		}
	}
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrRefused
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, ErrRefused
	}
	if executable && st.Mode().Perm()&0111 == 0 {
		f.Close()
		return nil, ErrRefused
	}
	n, e := unix.Fgetxattr(int(f.Fd()), "security.capability", nil)
	if n > 0 || (e != nil && e != unix.ENODATA && e != unix.ENOTSUP) {
		f.Close()
		return nil, ErrRefused
	}
	return f, nil
}
func Load(path string) (Config, error) {
	var c Config
	f, e := rootFile(path, false)
	if e != nil {
		return c, e
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Mode().Perm()&0077 != 0 {
		return c, ErrRefused
	}
	b, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil || len(b) > 16384 {
		return c, ErrRefused
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || !digest(c.Domain) || !digest(c.Incarnation) || !digest(c.SHA256) || c.UID < 1000 || c.GID < 1000 || c.UID == 65534 || c.GID == 65534 {
		return Config{}, ErrRefused
	}
	return c, nil
}
func writeRecord(dir string, r Record) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, "next"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	// Failed publication remains unknown and is deliberately not cleaned/replayed.
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(filepath.Join(dir, "next"), filepath.Join(dir, "state.json")); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Run is supported ONLY as root PID1 in a dedicated guest PID namespace.
// It never kills descendants, restarts the runner, deletes a native resource,
// returns capacity, or publishes a job-completion certificate.
func Run(c Config) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 || !digest(c.Domain) || !digest(c.Incarnation) || !digest(c.SHA256) || c.UID < 1000 || c.GID < 1000 {
		return ErrRefused
	}
	executable, e := rootFile(c.Launcher, true)
	if e != nil {
		return e
	}
	defer executable.Close()
	header := make([]byte, 4)
	if _, e = executable.ReadAt(header, 0); e != nil || string(header) != "\x7fELF" {
		return ErrRefused
	}
	h := sha256.New()
	if _, e = io.Copy(h, executable); e != nil || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
		return ErrRefused
	}
	if _, e = executable.Seek(0, 0); e != nil {
		return e
	}
	// Work directory must be a dedicated non-root-owned writable leaf whose
	// parent remains root-controlled; no caller-controlled ambient cwd/environment.
	marker := filepath.Join(filepath.Dir(c.Workdir), ".protected-runner-layout")
	m, e := rootFile(marker, false)
	if e != nil {
		return e
	}
	m.Close()
	st, e := os.Lstat(c.Workdir)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return ErrRefused
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != c.UID || owner.Gid != c.GID {
		return ErrRefused
	}
	// Existing journal path or any prior launch intent refuses replay, including
	// after a crash before spawn. Never infer 'not started' from absent child PID.
	marker = filepath.Join(filepath.Dir(c.Journal), ".protected-runner-layout")
	m, e = rootFile(marker, false)
	if e != nil {
		return e
	}
	m.Close()
	if filepath.Clean(c.Journal) != c.Journal || !filepath.IsAbs(c.Journal) || filepath.Clean(c.Workdir) != c.Workdir || !filepath.IsAbs(c.Workdir) {
		return ErrRefused
	}
	if e = os.Mkdir(c.Journal, 0700); e != nil {
		return ErrRefused
	}
	parent, e := os.Open(filepath.Dir(c.Journal))
	if e != nil {
		return ErrRefused
	}
	e = parent.Sync()
	parent.Close()
	if e != nil {
		return ErrRefused
	}
	r := Record{Domain: c.Domain, Incarnation: c.Incarnation, LauncherSHA256: c.SHA256, State: "launch_unknown", At: time.Now().UTC(), ExitCode: -1}
	if e = writeRecord(c.Journal, r); e != nil {
		return ErrRefused
	}
	// PID1 ignores termination requests instead of forwarding them to running jobs.
	// Cancellation/host reboot still cannot be called a completed job.
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		for range signals {
		}
	}()
	null, e := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if e != nil {
		return ErrRefused
	}
	defer null.Close()
	child, e := os.StartProcess("/proc/self/exe", []string{"capacity-runner-init", "--child"}, &os.ProcAttr{Dir: c.Workdir, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + c.Workdir, "LANG=C.UTF-8"}, Files: []*os.File{null, null, null, executable}, Sys: &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: c.UID, Gid: c.GID, NoSetGroups: false, Groups: []uint32{}}, Setsid: true}})
	if e != nil {
		return ErrRefused
	}
	pid := child.Pid
	child.Release()
	exit, e := reap(pid)
	if e != nil {
		return ErrRefused
	}
	r.State = "process_tree_exited_unqualified"
	r.At = time.Now().UTC()
	r.ExitCode = exit
	if e = writeRecord(c.Journal, r); e != nil {
		return ErrRefused
	}
	return nil
}

// Child is the fixed post-credential-drop fd-exec boundary. No arbitrary argv,
// executable path or environment is accepted. The image launcher owns bootstrap.
func Child() error {
	if os.Geteuid() < 1000 || os.Getegid() < 1000 {
		return ErrRefused
	}
	if e := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); e != nil {
		return e
	}
	if e := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); e != nil {
		return e
	}
	var hdr unix.CapUserHeader
	hdr.Version = unix.LINUX_CAPABILITY_VERSION_3
	caps := make([]unix.CapUserData, 2)
	if e := unix.Capget(&hdr, &caps[0]); e != nil {
		return e
	}
	for _, v := range caps {
		if v.Effective != 0 || v.Permitted != 0 || v.Inheritable != 0 {
			return ErrRefused
		}
	}
	if e := unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); e != nil {
		return e
	}
	unix.CloseOnExec(3)
	return syscall.Exec("/proc/self/fd/3", []string{"protected-runner-launcher"}, os.Environ())
}
func reap(main int) (int, error) {
	exit := -1
	seen := false
	for {
		var status syscall.WaitStatus
		pid, e := syscall.Wait4(-1, &status, 0, nil)
		if e == syscall.EINTR {
			continue
		}
		if e == syscall.ECHILD {
			if !seen {
				return -1, ErrRefused
			}
			return exit, nil
		}
		if e != nil {
			return -1, fmt.Errorf("reap: %w", e)
		}
		if pid == main {
			seen = true
			if status.Exited() {
				exit = status.ExitStatus()
			} else {
				exit = 128 + int(status.Signal())
			}
		}
	}
}
