//go:build linux

package protectedinit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunnerLayoutSpec describes an already-installed verified image bundle. No
// network download, package installation, sudo, or arbitrary JIT filename is
// accepted. The caller is the protected guest init, never a job or API request.
type RunnerLayoutSpec struct {
	Bundle, Destination, JITFile, RunnerName string
	UID, GID                                 uint32
	// Exact file inventory below bin/ and externals/, pinned by the image builder.
	Files map[string]string
}

// RunnerLayout holds actual root-created mounts and an open listener descriptor.
// It is not native ownership or completion authority. No adopted path constructor.
type RunnerLayout struct {
	listener *os.File
	root     string
}

func (l *RunnerLayout) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	e := l.listener.Close()
	l.listener = nil
	return e
}

// PrepareRunnerLayout is intentionally not wired into the stock bootstrap.
// It performs actual root mount/file setup when called as dedicated guest PID1.
// Failed preparation leaves a consumed destination for explicit operator recovery;
// never unmount/retry while ownership or accepted work might be uncertain.
func PrepareRunnerLayout(s RunnerLayoutSpec) (*RunnerLayout, error) {
	if os.Geteuid() != 0 || os.Getpid() != 1 || s.UID < 1000 || s.GID < 1000 || s.RunnerName == "" || len(s.Files) == 0 || len(s.Files) > 20000 {
		return nil, ErrRefused
	}
	if !filepath.IsAbs(s.Bundle) || filepath.Clean(s.Bundle) != s.Bundle || !filepath.IsAbs(s.Destination) || filepath.Clean(s.Destination) != s.Destination {
		return nil, ErrRefused
	}
	if strings.HasPrefix(s.Destination, s.Bundle+"/") || strings.HasPrefix(s.Bundle, s.Destination+"/") || s.Bundle == s.Destination {
		return nil, ErrRefused
	}
	marker, e := rootFile(filepath.Join(filepath.Dir(s.Destination), ".protected-runner-layout"), false)
	if e != nil {
		return nil, e
	}
	marker.Close()
	jit, e := rootFile(s.JITFile, false)
	if e != nil {
		return nil, e
	}
	st, e := jit.Stat()
	if e != nil || st.Mode().Perm()&0077 != 0 {
		jit.Close()
		return nil, ErrRefused
	}
	raw, e := io.ReadAll(io.LimitReader(jit, (2<<20)+1))
	jit.Close()
	if e != nil || len(raw) > 2<<20 {
		return nil, ErrRefused
	}
	files, e := decodeJIT(raw, s.RunnerName)
	if e != nil {
		return nil, e
	}
	// The source tree must contain exactly pinned ordinary root-owned files;
	// mounted descendants and symlinks are refused, not trusted by name.
	mountinfo, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return nil, e
	}
	for _, line := range strings.Split(string(mountinfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 {
			p := fields[4]
			if strings.Contains(p, "\\") {
				return nil, ErrRefused
			}
			if strings.HasPrefix(p, s.Bundle+"/") {
				return nil, ErrRefused
			}
		}
	}
	seen := map[string]bool{}
	var total int64
	for _, dir := range []string{"bin", "externals"} {
		e = filepath.WalkDir(filepath.Join(s.Bundle, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			st, err := os.Lstat(path)
			if err != nil {
				return err
			}
			u, ok := st.Sys().(*syscall.Stat_t)
			if !ok || u.Uid != 0 || st.Mode().Perm()&0022 != 0 || st.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
				return ErrRefused
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(s.Bundle, path)
			if err != nil || !digest(s.Files[rel]) || !st.Mode().IsRegular() || st.Size() > 512<<20 {
				return ErrRefused
			}
			total += st.Size()
			if total > 4<<30 {
				return ErrRefused
			}
			f, err := rootFile(path, false)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil || hex.EncodeToString(h.Sum(nil)) != s.Files[rel] {
				return ErrRefused
			}
			seen[rel] = true
			return nil
		})
		if e != nil {
			return nil, ErrRefused
		}
	}
	if len(seen) != len(s.Files) || !seen["bin/Runner.Listener"] || !seen["bin/Runner.Listener.dll"] {
		return nil, ErrRefused
	}
	if e = os.Mkdir(s.Destination, 0700); e != nil {
		return nil, ErrRefused
	}
	parent, e := os.Open(filepath.Dir(s.Destination))
	if e != nil {
		return nil, e
	}
	e = parent.Sync()
	parent.Close()
	if e != nil {
		return nil, e
	}
	// This is only valid inside the dedicated guest. Prevent propagation of new
	// mounts to another namespace; no recursive propagation changes outside it.
	if e = unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); e != nil {
		return nil, ErrRefused
	}
	for _, dir := range []string{"bin", "externals"} {
		dest := filepath.Join(s.Destination, dir)
		if e = os.Mkdir(dest, 0755); e != nil {
			return nil, e
		}
		if e = unix.Mount(filepath.Join(s.Bundle, dir), dest, "", unix.MS_BIND, ""); e != nil {
			return nil, ErrRefused
		}
		if e = unix.Mount("", dest, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); e != nil {
			return nil, ErrRefused
		}
		var fs unix.Statfs_t
		if e = unix.Statfs(dest, &fs); e != nil || fs.Flags&(unix.ST_RDONLY|unix.ST_NOSUID|unix.ST_NODEV) != (unix.ST_RDONLY|unix.ST_NOSUID|unix.ST_NODEV) {
			return nil, ErrRefused
		}
	}
	for _, dir := range []string{"_diag", "_work"} {
		p := filepath.Join(s.Destination, dir)
		if e = os.Mkdir(p, 0700); e != nil {
			return nil, e
		}
		if e = os.Chown(p, int(s.UID), int(s.GID)); e != nil {
			return nil, e
		}
	}
	for name, data := range files {
		f, e := os.OpenFile(filepath.Join(s.Destination, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(data); e == nil {
			e = f.Chown(int(s.UID), int(s.GID))
		}
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return nil, e
		}
	}
	// Open while the destination root is still root-owned. Only afterwards give
	// the unprivileged runner ownership of its writable config directory. Mounted
	// binary directories cannot be replaced/renamed/unmounted without privilege.
	listener, e := rootFile(filepath.Join(s.Destination, "bin/Runner.Listener"), true)
	if e != nil {
		return nil, e
	}
	magic := make([]byte, 4)
	if _, e = listener.ReadAt(magic, 0); e != nil || string(magic) != "\x7fELF" {
		listener.Close()
		return nil, ErrRefused
	}
	root, e := os.Open(s.Destination)
	if e != nil {
		listener.Close()
		return nil, e
	}
	if e = root.Chown(int(s.UID), int(s.GID)); e == nil {
		e = root.Sync()
	}
	root.Close()
	if e != nil {
		listener.Close()
		return nil, e
	}
	return &RunnerLayout{listener: listener, root: s.Destination}, nil
}

// decodeJIT implements a bounded exact known-file allowlist, not the stock
// listener's generic Path.Combine(file-name) writer. No secret is logged.
func decodeJIT(raw []byte, name string) (map[string][]byte, error) {
	decoded, e := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(raw)))
	if e != nil || len(decoded) > 1<<20 {
		return nil, ErrRefused
	}
	d := json.NewDecoder(bytes.NewReader(decoded))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, ErrRefused
	}
	out := map[string][]byte{}
	for d.More() {
		tok, e = d.Token()
		k, ok := tok.(string)
		if e != nil || !ok || (k != ".runner" && k != ".credentials" && k != ".credentials_rsaparams") || out[k] != nil {
			return nil, ErrRefused
		}
		var v string
		if d.Decode(&v) != nil {
			return nil, ErrRefused
		}
		b, e := base64.StdEncoding.Strict().DecodeString(v)
		if e != nil || len(b) == 0 || len(b) > 1<<19 {
			return nil, ErrRefused
		}
		out[k] = b
	}
	tok, e = d.Token()
	if e != nil || tok != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(out) != 3 {
		return nil, ErrRefused
	}
	// Reject duplicate/case-alias settings; Newtonsoft accepts case-insensitive
	// fields, so ambiguous JSON must never cross this root preparation boundary.
	d = json.NewDecoder(bytes.NewReader(out[".runner"]))
	tok, e = d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, ErrRefused
	}
	settings := map[string]json.RawMessage{}
	for d.More() {
		tok, e = d.Token()
		k, ok := tok.(string)
		if e != nil || !ok {
			return nil, ErrRefused
		}
		k = strings.ToLower(k)
		if _, ok = settings[k]; ok {
			return nil, ErrRefused
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrRefused
		}
		settings[k] = value
	}
	tok, e = d.Token()
	if e != nil || tok != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrRefused
	}
	var actual, work, monitor string
	var ephemeral, disable bool
	if json.Unmarshal(settings["agentname"], &actual) != nil || actual != name || json.Unmarshal(settings["workfolder"], &work) != nil || work != "_work" || json.Unmarshal(settings["ephemeral"], &ephemeral) != nil || !ephemeral || json.Unmarshal(settings["disableupdate"], &disable) != nil || !disable {
		return nil, ErrRefused
	}
	if value, ok := settings["monitorsocketaddress"]; ok && (json.Unmarshal(value, &monitor) != nil || monitor != "") {
		return nil, ErrRefused
	}
	return out, nil
}
