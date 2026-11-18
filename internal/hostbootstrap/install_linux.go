package hostbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const home = "/var/lib/garm-inventory"
const binaryPath = "/usr/local/libexec/capacity-host-inventory"
const keyfile = "/etc/ssh/authorized_keys/garm-inventory"
const policyFile = "/etc/garm-capacity-bootstrap/host.json"
const journal = "/var/lib/garm-capacity-bootstrap/inventory-plan"

// CheckPolicy rejects alternative authentication and authorization paths. No
// policy is changed and sshd is never restarted/reloaded.
func CheckPolicy(raw string) error {
	required := map[string]string{"forcecommand": "none", "usedns": "no", "authenticationmethods": "publickey", "pubkeyauthentication": "yes", "passwordauthentication": "no", "kbdinteractiveauthentication": "no", "gssapiauthentication": "no", "hostbasedauthentication": "no", "authorizedkeyscommand": "none", "trustedusercakeys": "none", "authorizedprincipalscommand": "none", "permituserenvironment": "no", "authorizedkeysfile": keyfile}
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "acceptenv" {
			for _, v := range f[1:] {
				if v != "LANG" && v != "LC_*" {
					return errors.New("effective SSH AcceptEnv permits unsafe variables")
				}
			}
		}
		if len(f) < 2 {
			continue
		}
		if want, ok := required[f[0]]; ok {
			if seen[f[0]] || strings.Join(f[1:], " ") != want {
				return fmt.Errorf("effective SSH policy unsupported: %s", f[0])
			}
			seen[f[0]] = true
		}
	}
	for k := range required {
		if !seen[k] {
			return fmt.Errorf("effective SSH policy missing: %s", k)
		}
	}
	return nil
}
func protected(path string, dir bool) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 || (p == path && dir && !st.IsDir()) || (p != path && !st.IsDir()) {
			return errors.New("root-controlled path required")
		}
		if p == "/" {
			return nil
		}
	}
}

// File capabilities can elevate an otherwise ordinary 0755 executable.
func noFileCapabilities(path string) bool {
	n, e := syscall.Getxattr(path, "security.capability", nil)
	return (e == nil && n == 0) || errors.Is(e, syscall.ENODATA) || errors.Is(e, syscall.ENOTSUP)
}

// trustedShell verifies every original symlink and every traversed parent,
// not only the final resolved executable. Standard root-owned /bin symlinks work.
func trustedShell(path string, depth int) error {
	if depth > 40 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("untrusted shell path")
	}
	if e := protected("/", true); e != nil {
		return e
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	current := "/"
	for i, part := range parts {
		current = filepath.Join(current, part)
		st, e := os.Lstat(current)
		if e != nil {
			return e
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 {
			return errors.New("shell path must be root owned")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(current)
			if e != nil {
				return e
			}
			// Reject dot traversal rather than normalize across a symlink boundary.
			// Cleaning "link/../x" can resolve differently from the kernel.
			for _, component := range strings.Split(target, "/") {
				if component == "." || component == ".." {
					return errors.New("shell symlink traversal unsupported")
				}
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			target = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			return trustedShell(target, depth+1)
		}
		if st.Mode().Perm()&0022 != 0 || (i < len(parts)-1 && !st.IsDir()) || (i == len(parts)-1 && (!st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || !noFileCapabilities(current))) {
			return errors.New("untrusted shell path")
		}
	}
	return nil
}

// installOnce never overwrites an existing file. Interrupted creation leaves a
// private temp file, not a partial destination. A retry accepts identical bytes.
func installOnce(path string, data []byte, mode os.FileMode) error {
	return installFile(path, data, mode, protected)
}
func installFile(path string, data []byte, mode os.FileMode, protect func(string, bool) error) error {
	if e := protect(filepath.Dir(path), true); e != nil {
		return e
	}
	if st, e := os.Lstat(path); e == nil {
		if !st.Mode().IsRegular() || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || st.Mode().Perm() != mode || !noFileCapabilities(path) || protect(path, false) != nil {
			return errors.New("existing installation differs")
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, int64(len(data)+1)))
		if e != nil || !bytes.Equal(b, data) {
			return errors.New("existing installation differs")
		}
		d, e := os.Open(filepath.Dir(path))
		if e != nil {
			return e
		}
		defer d.Close()
		return d.Sync()
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".inventory-install-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Link(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// HostPolicy is installed by the trusted host administrator, never supplied by
// the SSH caller. The artifact allowlist prevents a limited sudo user installing
// caller-invented code; local endpoint coordinates account for NAT explicitly.
type HostPolicy struct {
	Address        string `json:"address"`
	Port           uint16 `json:"port"`
	ControllerIP   string `json:"controller_ip"`
	LocalAddress   string `json:"local_address"`
	LocalPort      uint16 `json:"local_port"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

func (p HostPolicy) Check(r Request) error {
	if !IP(p.LocalAddress) || p.LocalPort == 0 || p.Address != r.Plan.Address || p.Port != r.Plan.Port || p.ControllerIP != r.Plan.ControllerIP || p.ArtifactSHA256 != r.Plan.ArtifactSHA256 {
		return errors.New("host-approved endpoint or artifact mismatch")
	}
	return nil
}
func readPolicy() (HostPolicy, error) {
	var p HostPolicy
	if e := protected(policyFile, false); e != nil {
		return p, e
	}
	f, e := os.Open(policyFile)
	if e != nil {
		return p, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errors.New("host policy invalid")
	}
	return p, nil
}

// Install requires root and a pre-created isolated account, root-controlled
// directories, and reviewed sshd policy. Every destination is a compiled constant.
func Install(ctx context.Context, r Request) error {
	if os.Geteuid() != 0 {
		return errors.New("privileged bootstrap helper required")
	}
	if e := Validate(r); e != nil {
		return e
	}
	policy, e := readPolicy()
	if e != nil {
		return errors.New("root-controlled host policy required")
	}
	if e = policy.Check(r); e != nil {
		return e
	}
	if r.Plan.Architecture != runtime.GOARCH {
		return errors.New("host architecture mismatch")
	}
	for _, p := range []string{home, "/usr/local/libexec", "/etc/ssh/authorized_keys", "/var/lib/garm-capacity-bootstrap"} {
		if e := protected(p, true); e != nil {
			return fmt.Errorf("prepared host prerequisite unavailable: %s", p)
		}
	}
	u, e := user.Lookup("garm-inventory")
	if e != nil || u.Uid == "0" || u.Gid == "0" || u.HomeDir != home {
		return errors.New("isolated inventory account required")
	}
	passwd, e := os.ReadFile("/etc/passwd")
	if e != nil {
		return errors.New("local account shell unavailable")
	}
	shellOK := false
	for _, line := range strings.Split(string(passwd), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 7 && parts[0] == "garm-inventory" {
			shellOK = parts[6] == "/bin/sh"
		}
	}
	if !shellOK || trustedShell("/bin/sh", 0) != nil {
		return errors.New("root-controlled /bin/sh account shell required")
	}
	groups, e := u.GroupIds()
	if e != nil || len(groups) != 1 || groups[0] != u.Gid {
		return errors.New("inventory account must have no supplementary groups")
	}
	// No account-writable startup paths: both the fixed home and its existing
	// descendants must be root controlled. Refuse anything unexpected rather than repair it.
	e = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != home {
			return errors.New("inventory home must be empty")
		}
		return nil
	})
	if e != nil {
		return e
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(check, "/usr/sbin/sshd", "-T", "-C", "user=garm-inventory,host="+r.Plan.ControllerIP+",addr="+r.Plan.ControllerIP+",laddr="+policy.LocalAddress+",lport="+strconv.Itoa(int(policy.LocalPort)))
	var out limitWriter
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || out.exceeded {
		return errors.New("effective SSH policy unavailable")
	}
	if e = CheckPolicy(out.String()); e != nil {
		return e
	}
	key, e := AuthorizedKey(r.Plan)
	if e != nil {
		return e
	}
	// The durable intent binds every retry. A different artifact/key/host plan
	// cannot replace a partial or completed installation under this helper.
	if e = installOnce(journal, []byte(r.ReviewDigest+"\n"), 0600); e != nil {
		return fmt.Errorf("bootstrap intent conflict: %w", e)
	}
	if e = installOnce(binaryPath, r.Artifact, 0755); e != nil {
		return fmt.Errorf("intent recorded; binary installation incomplete: %w", e)
	}
	if e = installOnce(keyfile, key, 0644); e != nil {
		return fmt.Errorf("binary installed; key installation incomplete: %w", e)
	}
	return nil
}
