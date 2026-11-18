// Package managedgarm verifies an owned stock-GARM container boundary. It never
// adopts arbitrary remote endpoints or treats a manifest as observed authority.
package managedgarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrBoundary = errors.New("managed stock GARM boundary not established")
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Specification is an installation-time pin set, not evidence. The root-owned
// observer must inspect the actual daemon and kernel namespace on every check.
type Specification struct {
	GarmServerFingerprint                    string
	DockerSocket                             string
	GarmID, ControllerID, IngressID          string
	GarmImage, ControllerImage, IngressImage string
	AdminSecretDirectory                     string
}
type container struct {
	ID, Image string
	Path      string
	Args      []string
	Config    struct {
		User       string
		WorkingDir string
		Env        []string
		Entrypoint []string
		Cmd        []string
	}
	State struct {
		Running bool
		Pid     int
	}
	HostConfig struct {
		NetworkMode, PidMode, IpcMode string
		Privileged, ReadonlyRootfs    bool
		CapAdd, CapDrop, SecurityOpt  []string
		PortBindings                  map[string][]struct{ HostIP, HostPort string }
		Devices                       []json.RawMessage
	}
	Mounts []struct {
		Source, Destination string
		RW                  bool
	}
}

// Observation describes a checked isolation snapshot, not a reusable write
// permit. Admin-account bootstrap/rotation and ongoing generation fencing must
// also be qualified before management writes can be enabled.
type Observation struct {
	namespace     string
	at            time.Time
	ids           [3]string
	garmPID       int
	processStart  uint64
	namespaceDev  uint64
	namespaceIno  uint64
	specification Specification
}

func (o Observation) ObservedAt() time.Time { return o.at }
func (o Observation) Namespace() string     { return o.namespace }

func validSpec(s Specification) bool {
	ids := []string{s.GarmID, s.ControllerID, s.IngressID}
	seen := map[string]bool{}
	for _, id := range ids {
		if !digest.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, image := range []string{s.GarmImage, s.ControllerImage, s.IngressImage} {
		if !strings.HasPrefix(image, "sha256:") || !digest.MatchString(strings.TrimPrefix(image, "sha256:")) {
			return false
		}
	}
	return digest.MatchString(s.GarmServerFingerprint) && filepath.IsAbs(s.DockerSocket) && filepath.Clean(s.DockerSocket) == s.DockerSocket && filepath.IsAbs(s.AdminSecretDirectory) && filepath.Clean(s.AdminSecretDirectory) == s.AdminSecretDirectory
}
func contains(v []string, w string) bool {
	for _, s := range v {
		if s == w {
			return true
		}
	}
	return false
}
func validateContainers(s Specification, c [3]container) error {
	ids := []string{s.GarmID, s.ControllerID, s.IngressID}
	images := []string{s.GarmImage, s.ControllerImage, s.IngressImage}
	users := []string{"65530:65530", "65532:65532", "65531:65531"}
	commands := [][]string{{"/usr/bin/garm", "-config", "/etc/garm/garm.toml"}, {"/usr/local/bin/capacity-gateway", "-config", "/run/controlplane/config/gateway.json"}, {"/usr/local/bin/capacity-stock-ingress", "/run/controlplane/ingress/ingress.json"}}
	sources := map[string]string{"/etc/garm": "garm-config", "/var/lib/garm": "garm-state", "/run/controlplane/config": "controller-config", "/var/lib/capacity-gateway": "controller-state", "/run/controlplane/ingress": "ingress"}
	mounts := []map[string]bool{{"/etc/garm": false, "/var/lib/garm": true}, {"/run/controlplane/config": false, "/var/lib/capacity-gateway": true}, {"/run/controlplane/ingress": false}}
	for i, v := range c {
		if !reflect.DeepEqual(v.Config.Env, []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}) || v.Config.WorkingDir != "/" || !reflect.DeepEqual(v.Config.Cmd, commands[i]) || len(v.Config.Entrypoint) != 0 || v.Path != commands[i][0] || !reflect.DeepEqual(v.Args, commands[i][1:]) {
			return ErrBoundary
		}
		seenMounts := map[string]bool{}
		if v.ID != ids[i] || v.Image != images[i] || v.Config.User != users[i] || !v.State.Running || v.State.Pid <= 1 || v.HostConfig.Privileged || !v.HostConfig.ReadonlyRootfs || len(v.HostConfig.CapAdd) != 0 || !contains(v.HostConfig.CapDrop, "ALL") || !contains(v.HostConfig.SecurityOpt, "no-new-privileges:true") || v.HostConfig.PidMode != "" || v.HostConfig.IpcMode == "host" || len(v.HostConfig.Devices) != 0 {
			return ErrBoundary
		}
		if i == 0 {
			if v.HostConfig.NetworkMode == "host" || v.HostConfig.NetworkMode == "none" || strings.HasPrefix(v.HostConfig.NetworkMode, "container:") {
				return ErrBoundary
			}
		} else if v.HostConfig.NetworkMode != "container:"+s.GarmID {
			return ErrBoundary
		}
		for port := range v.HostConfig.PortBindings {
			if i != 0 || port != "8443/tcp" {
				return ErrBoundary
			}
		}
		for _, m := range v.Mounts {
			if m.Destination == "/tmp" && m.Source == "" {
				continue
			}
			rw, allowed := mounts[i][m.Destination]
			if !allowed || seenMounts[m.Destination] || m.RW != rw || !filepath.IsAbs(m.Source) || filepath.Clean(m.Source) != m.Source {
				return ErrBoundary
			}
			// Root-controlled sibling mount names prevent a controller config
			// mount from exposing stock JWT signing material or its database.
			if m.Source != filepath.Join(filepath.Dir(s.AdminSecretDirectory), sources[m.Destination]) {
				return ErrBoundary
			}
			seenMounts[m.Destination] = true
			if m.Source == "/" || m.Destination == "/" || strings.Contains(m.Source, "docker.sock") || strings.Contains(m.Source, "containerd.sock") || strings.HasPrefix(m.Destination, "/proc") || strings.HasPrefix(m.Destination, "/sys") {
				return ErrBoundary
			}
			if m.Source == s.AdminSecretDirectory || strings.HasPrefix(s.AdminSecretDirectory, m.Source+"/") || strings.HasPrefix(m.Source, s.AdminSecretDirectory+"/") {
				return ErrBoundary
			}
		}
		if len(seenMounts) != len(mounts[i]) {
			return ErrBoundary
		}
	}
	return nil
}

// Observe is a root-supervisor-only, read-only Docker/kernel inspection. Never
// mount the Docker socket into the controller or public ingress. It refuses
// missing/inaccessible/changed evidence, extra namespace members or admin ports.
func Observe(ctx context.Context, s Specification) (Observation, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || !validSpec(s) {
		return Observation{}, ErrBoundary
	}
	info, e := os.Lstat(s.DockerSocket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0002 != 0 {
		return Observation{}, ErrBoundary
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return Observation{}, ErrBoundary
	}
	if rootControlledAncestors(s.AdminSecretDirectory) != nil {
		return Observation{}, ErrBoundary
	}
	secretPath, e := filepath.EvalSymlinks(s.AdminSecretDirectory)
	if e != nil || secretPath != s.AdminSecretDirectory {
		return Observation{}, ErrBoundary
	}
	secret, e := os.Lstat(s.AdminSecretDirectory)
	if e != nil || !secret.IsDir() || secret.Mode().Perm()&0077 != 0 {
		return Observation{}, ErrBoundary
	}
	owner, ok := secret.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return Observation{}, ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, e := (&net.Dialer{}).DialContext(ctx, "unix", s.DockerSocket)
		if e != nil {
			return nil, e
		}
		u, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			return nil, ErrBoundary
		}
		raw, e := u.SyscallConn()
		if e != nil {
			conn.Close()
			return nil, e
		}
		trusted := false
		err := raw.Control(func(fd uintptr) {
			cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			trusted = e == nil && cred.Uid == 0
		})
		if err != nil || !trusted {
			conn.Close()
			return nil, ErrBoundary
		}
		return conn, nil
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	get := func(path string, out any) error {
		r, e := http.NewRequestWithContext(ctx, "GET", "http://docker"+path, nil)
		if e != nil {
			return e
		}
		res, e := client.Do(r)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return ErrBoundary
		}
		raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
		if e != nil || len(raw) > 4<<20 {
			return ErrBoundary
		}
		return json.Unmarshal(raw, out)
	}
	var c [3]container
	for i, id := range []string{s.GarmID, s.ControllerID, s.IngressID} {
		if get("/containers/"+id+"/json", &c[i]) != nil {
			return Observation{}, ErrBoundary
		}
	}
	// Docker reports mount source paths; reject aliases that could conceal the
	// supervisor credential directory behind an otherwise allowed mount.
	for _, v := range c {
		for _, m := range v.Mounts {
			if m.Source == "" && m.Destination == "/tmp" {
				continue
			}
			real, err := filepath.EvalSymlinks(m.Source)
			if err != nil || real != m.Source {
				return Observation{}, ErrBoundary
			}
		}
	}
	if validateContainers(s, c) != nil {
		return Observation{}, ErrBoundary
	}
	start, e := processStartTime(c[0].State.Pid)
	if e != nil {
		return Observation{}, ErrBoundary
	}
	ns, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", c[0].State.Pid))
	self, _ := os.Readlink("/proc/self/ns/net")
	if e != nil || ns == "" || ns == self {
		return Observation{}, ErrBoundary
	}
	for _, v := range c[1:] {
		n, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", v.State.Pid))
		if e != nil || n != ns {
			return Observation{}, ErrBoundary
		}
	}
	// Enumerate all running containers, not just the expected three, so joining
	// this namespace cannot silently expand the administration surface.
	var all []struct{ Id string }
	if get("/containers/json", &all) != nil || len(all) > 1000 {
		return Observation{}, ErrBoundary
	}
	for _, row := range all {
		if row.Id == s.GarmID || row.Id == s.ControllerID || row.Id == s.IngressID {
			continue
		}
		if !digest.MatchString(row.Id) {
			return Observation{}, ErrBoundary
		}
		var v container
		if get("/containers/"+row.Id+"/json", &v) != nil {
			return Observation{}, ErrBoundary
		}
		if v.State.Running && v.State.Pid > 1 {
			n, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", v.State.Pid))
			if e != nil || n == ns {
				return Observation{}, ErrBoundary
			}
		}
	}
	if checkListeners(c[0].State.Pid) != nil {
		return Observation{}, ErrBoundary
	}
	for _, v := range c {
		n, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", v.State.Pid))
		if e != nil || n != ns {
			return Observation{}, ErrBoundary
		}
	}
	endStart, err := processStartTime(c[0].State.Pid)
	info, statErr := os.Stat(fmt.Sprintf("/proc/%d/ns/net", c[0].State.Pid))
	if err != nil || statErr != nil || start != endStart {
		return Observation{}, ErrBoundary
	}
	nsStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Observation{}, ErrBoundary
	}
	return Observation{processStart: start, namespaceDev: uint64(nsStat.Dev), namespaceIno: nsStat.Ino, namespace: ns, at: time.Now().UTC(), ids: [3]string{s.GarmID, s.ControllerID, s.IngressID}, garmPID: c[0].State.Pid, specification: s}, nil
}
func checkListeners(pid int) error {
	admin := false
	for _, name := range []string{"tcp", "tcp6"} {
		raw, e := boundedRead(fmt.Sprintf("/proc/%d/net/%s", pid, name), 4<<20)
		if e != nil {
			return ErrBoundary
		}
		if len(raw) > 4<<20 {
			return ErrBoundary
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) < 4 {
				return ErrBoundary
			}
			if fields[3] != "0A" {
				continue
			}
			parts := strings.Split(fields[1], ":")
			if len(parts) != 2 {
				return ErrBoundary
			}
			switch parts[1] {
			case "270E":
				if name != "tcp" || parts[0] != "0100007F" {
					return ErrBoundary
				}
				admin = true
			case "20FB": // 8443: strict public ingress.
			case "24E3", "24E4", "24E5": // 9443/9444: gateway/admin loopback only.
				if name != "tcp" || parts[0] != "0100007F" {
					return ErrBoundary
				}
			default: // Docker's embedded DNS may listen on an ephemeral local address.
				if name != "tcp" || parts[0] != "0B00007F" {
					return ErrBoundary
				}
			}
		}
	}
	if !admin {
		return ErrBoundary
	}
	return nil
}

// OpenNetworkNamespace pins the observed kernel namespace, not a permission to
// write. The supervisor must re-observe topology for every operation and revoke
// its session on drift. A file descriptor does not extend this snapshot's life.
func (o Observation) OpenNetworkNamespace() (*os.File, error) {
	age := time.Since(o.at)
	if os.Geteuid() != 0 || o.garmPID <= 1 || o.processStart == 0 || o.namespaceIno == 0 || age < 0 || age > 5*time.Second {
		return nil, ErrBoundary
	}
	start, err := processStartTime(o.garmPID)
	if err != nil || start != o.processStart {
		return nil, ErrBoundary
	}
	path := fmt.Sprintf("/proc/%d/ns/net", o.garmPID)
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrBoundary
	}
	reject := func() (*os.File, error) { f.Close(); return nil, ErrBoundary }
	info, err := f.Stat()
	if err != nil {
		return reject()
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != o.namespaceDev || st.Ino != o.namespaceIno {
		return reject()
	}
	name, err := os.Readlink(path)
	after, afterErr := processStartTime(o.garmPID)
	if err != nil || afterErr != nil || after != o.processStart || name != o.namespace || time.Since(o.at) > 5*time.Second {
		return reject()
	}
	return f, nil
}
func boundedRead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrBoundary
	}
	return b, nil
}
func processStartTime(pid int) (uint64, error) {
	b, err := boundedRead(fmt.Sprintf("/proc/%d/stat", pid), 64<<10)
	if err != nil {
		return 0, err
	}
	return parseProcessStart(b)
}
func parseProcessStart(b []byte) (uint64, error) {
	// comm can contain spaces and parentheses. Fields after its final ')' start
	// at field3; starttime is field22, hence index19.
	end := strings.LastIndex(string(b), ") ")
	if end < 0 {
		return 0, ErrBoundary
	}
	fields := strings.Fields(string(b[end+2:]))
	if len(fields) < 20 {
		return 0, ErrBoundary
	}
	n, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || n == 0 {
		return 0, ErrBoundary
	}
	return n, nil
}

// All directory entries above a privileged file must be root-controlled; a
// secure leaf does not prevent rename/replacement through a writable ancestor.
func rootControlledAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrBoundary
	}
	for {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return ErrBoundary
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return ErrBoundary
		}
		if path == "/" {
			return nil
		}
		path = filepath.Dir(path)
	}
}
