package managedgarm

// This file is a separate, currently uncalled read-only pilot observer. It does
// not construct managed Specification/Observation/Session or grant authority.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stockReadProfileDirectory = "/etc/capacity-stock-readonly"
const stockReadImage = "sha256:cb7c076ddbf24a67a9aef4a4e61ace495e9ffc7bcb69df3bf5e89275a26b79f6"
const stockReadExecutableHash = "1fd77ef68b67f7afb45208473bcbb984ceb3d25e50721ed5104fcb20c930b95d"
const stockReadReference = "ghcr.io/cloudbase/garm:v0.2.1@sha256:5ccdc29c31084ed4f30ec85e09a57b5105cf3dcc5fa8c1aba9e68de461e7e635"

// Only loaded from the fixed protected file. Gateway/ingress measurements must
// be independently reviewed from the actual v2 artifact; no defaults invent them.
type stockReadProfile struct {
	Version, Reference, Platform, Directory string
	IDs, Images, ExecutableSHA256           [3]string // stock, gateway, ingress
}
type stockReadSample struct {
	start    uint64
	net, pid string
}

// A bounded sampled diagnostic fact, not atomic isolation or a reusable permit.
// No exported fields, namespace handles, accessors, or conversion are provided.
type stockReadObservation struct {
	at      time.Time
	samples [3]stockReadSample
	profile stockReadProfile
	pids    [3]int
}

func validStockReadProfile(p stockReadProfile) bool {
	if p.Version != "stock-readonly-profile/v1" || p.Reference != stockReadReference || p.Platform != "linux/amd64" || p.Images[0] != stockReadImage || p.ExecutableSHA256[0] != stockReadExecutableHash || p.Directory == "/" || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory {
		return false
	}
	ids, images := map[string]bool{}, map[string]bool{}
	for i := range p.IDs {
		if !digest.MatchString(p.IDs[i]) || ids[p.IDs[i]] || !strings.HasPrefix(p.Images[i], "sha256:") || !digest.MatchString(strings.TrimPrefix(p.Images[i], "sha256:")) || images[p.Images[i]] || !digest.MatchString(p.ExecutableSHA256[i]) {
			return false
		}
		ids[p.IDs[i]] = true
		images[p.Images[i]] = true
	}
	return true
}
func loadStockReadProfile() (stockReadProfile, error) {
	var p stockReadProfile
	if os.Geteuid() != 0 || rootControlledAncestors(stockReadProfileDirectory) != nil {
		return p, ErrBoundary
	}
	root, err := os.OpenRoot(stockReadProfileDirectory)
	if err != nil {
		return p, ErrBoundary
	}
	defer root.Close()
	raw, _, err := stockReadProfileFile(root)
	if err != nil {
		return p, ErrBoundary
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !validStockReadProfile(p) {
		return stockReadProfile{}, ErrBoundary
	}
	return p, nil
}
func stockReadCommands() [3][]string {
	return [3][]string{
		{"/bin/garm", "-config", "/run/garm-private/config.toml"},
		{"/usr/local/bin/capacity-gateway", "-config", "/run/capacity-private/gateway.local.json"},
		{"/usr/local/bin/capacity-stock-ingress", "/run/joined-ingress/ingress.json"},
	}
}
func stockReadMounts(p stockReadProfile) [3]map[string]string {
	return [3]map[string]string{
		{"/run/garm-private": filepath.Join(p.Directory, "fixture/private"), "/var/lib/garm": filepath.Join(p.Directory, "fixture/state")},
		{"/run/capacity-private": filepath.Join(p.Directory, "joined/gateway-private"), "/var/lib/capacity-gateway": filepath.Join(p.Directory, "joined/state")},
		{"/run/joined-ingress": filepath.Join(p.Directory, "joined/ingress-private")},
	}
}
func validateStockReadContainers(p stockReadProfile, c [3]container) error {
	if !validStockReadProfile(p) {
		return ErrBoundary
	}
	commands := stockReadCommands()
	mounts := stockReadMounts(p)
	uids := []string{"65530:65530", "65532:65532", "65531:65531"}
	pids := map[int]bool{}
	for i, v := range c {
		if v.ID != p.IDs[i] || v.Image != p.Images[i] || !v.State.Running || v.State.Pid <= 1 || pids[v.State.Pid] || v.Config.User != uids[i] || v.Config.WorkingDir != "/" || !reflect.DeepEqual(v.Config.Env, []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}) || v.HostConfig.Privileged || !v.HostConfig.ReadonlyRootfs || len(v.HostConfig.CapAdd) != 0 || !reflect.DeepEqual(v.HostConfig.CapDrop, []string{"ALL"}) || !contains(v.HostConfig.SecurityOpt, "no-new-privileges:true") || v.HostConfig.PidMode != "" || v.HostConfig.IpcMode == "host" || len(v.HostConfig.Devices) != 0 || len(v.HostConfig.PortBindings) != 0 {
			return ErrBoundary
		}
		pids[v.State.Pid] = true
		if i == 0 {
			args := []string{"-ec", `umask 077; exec "$1" -config /run/garm-private/config.toml`, "probe", "/bin/garm"}
			if v.Path != "/bin/sh" || !reflect.DeepEqual(v.Args, args) || !reflect.DeepEqual(v.Config.Entrypoint, []string{"/bin/sh"}) || !reflect.DeepEqual(v.Config.Cmd, args) || v.HostConfig.NetworkMode != "none" {
				return ErrBoundary
			}
		} else {
			if v.Path != commands[i][0] || !reflect.DeepEqual(v.Args, commands[i][1:]) || v.HostConfig.NetworkMode != "container:"+p.IDs[0] {
				return ErrBoundary
			}
			if i == 1 && (!reflect.DeepEqual(v.Config.Entrypoint, commands[i][:1]) || !reflect.DeepEqual(v.Config.Cmd, commands[i][1:])) {
				return ErrBoundary
			}
			if i == 2 && (len(v.Config.Entrypoint) != 0 || !reflect.DeepEqual(v.Config.Cmd, commands[i])) {
				return ErrBoundary
			}
		}
		seen := map[string]bool{}
		for _, m := range v.Mounts {
			if m.Destination == "/tmp" && m.Source == "" && i != 2 {
				continue
			}
			source, ok := mounts[i][m.Destination]
			rw := m.Destination == "/var/lib/garm" || m.Destination == "/var/lib/capacity-gateway"
			if !ok || seen[m.Destination] || source != m.Source || m.RW != rw {
				return ErrBoundary
			}
			seen[m.Destination] = true
		}
		if len(seen) != len(mounts[i]) {
			return ErrBoundary
		}
	}
	return nil
}
func stockReadStatus(raw []byte, uid string) bool {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			if _, dup := values[k]; dup {
				return false
			}
			values[k] = strings.Join(strings.Fields(v), " ")
		}
	}
	if values["Uid"] != strings.TrimSpace(strings.Repeat(uid+" ", 4)) || values["Gid"] != strings.TrimSpace(strings.Repeat(uid+" ", 4)) || values["NoNewPrivs"] != "1" {
		return false
	}
	for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if values[k] != "0000000000000000" {
			return false
		}
	}
	return true
}
func sampleStockReadProcess(pid int) (stockReadSample, error) {
	start, err := processStartTime(pid)
	if err != nil {
		return stockReadSample{}, ErrBoundary
	}
	net, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
	if e != nil {
		return stockReadSample{}, ErrBoundary
	}
	ns, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
	if e != nil {
		return stockReadSample{}, ErrBoundary
	}
	return stockReadSample{start, net, ns}, nil
}
func stockReadListeners(tables [2][]byte) bool {
	return stockReadListenerSet(tables, nil)
}
func stockReadPrebootListeners(tables [2][]byte) bool {
	seen := map[uint64]bool{}
	for i, raw := range tables {
		lines := strings.Split(string(raw), "\n")
		if len(lines) == 0 || !strings.Contains(lines[0], "local_address") {
			return false
		}
		for _, line := range lines[1:] {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if len(f) < 4 {
				return false
			}
			if f[3] != "0A" {
				continue
			}
			a, p, ok := strings.Cut(f[1], ":")
			port, e := strconv.ParseUint(p, 16, 16)
			if !ok || e != nil || i != 0 || a != "0100007F" || seen[port] || (port != 8443 && port != 9443 && port != 9444 && port != 9998) {
				return false
			}
			seen[port] = true
		}
	}
	return len(seen) == 4
}

// No arguments can supply an observation, transport, paths or credentials. No
// caller exists yet. Success is only a bracketed preboot snapshot: a privileged
// actor can change the world after return; no continuity/atomicity is claimed.
func observeStockReadOnly(ctx context.Context) (stockReadObservation, error) {
	return observeStockReadPhase(ctx, nil)
}

// The sole supervised caller is the private lifecycle holding its actual socket.
func observeStockReadPhase(ctx context.Context, held *stockReadListener) (stockReadObservation, error) {
	fail := func() (stockReadObservation, error) { return stockReadObservation{}, ErrBoundary }
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := loadStockReadProfile()
	if err != nil {
		return fail()
	}
	docker, err := newDockerInspectionReader("/var/run/docker.sock")
	if err != nil {
		return fail()
	}
	defer docker.close()
	var c [3]container
	var before [3]stockReadSample
	commands := stockReadCommands()
	uids := []uint32{65530, 65532, 65531}
	for i, id := range p.IDs {
		c[i], err = docker.inspect(ctx, id)
		if err != nil {
			return fail()
		}
	}
	if validateStockReadContainers(p, c) != nil {
		return fail()
	}
	mounts := stockReadMounts(p)
	for i := range c {
		for _, source := range mounts[i] {
			if rootControlledAncestors(filepath.Dir(source)) != nil {
				return fail()
			}
			fi, e := os.Lstat(source)
			if e != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 {
				return fail()
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != uids[i] {
				return fail()
			}
		}
		before[i], err = sampleStockReadProcess(c[i].State.Pid)
		if err != nil {
			return fail()
		}
		if i > 0 && (before[i].net != before[0].net || before[i].pid == before[0].pid) {
			return fail()
		}
		if i == 2 && before[i].pid == before[1].pid {
			return fail()
		}
		root, e := os.OpenRoot(fmt.Sprintf("/proc/%d/root", c[i].State.Pid))
		if e != nil {
			return fail()
		}
		info, e := stockReadExecutable(root, strings.TrimPrefix(commands[i][0], "/"), p.ExecutableSHA256[i])
		root.Close()
		if e != nil {
			return fail()
		}
		actual, e := os.Stat(fmt.Sprintf("/proc/%d/exe", c[i].State.Pid))
		if e != nil || !os.SameFile(actual, info) {
			return fail()
		}
		cmd, e := boundedRead(fmt.Sprintf("/proc/%d/cmdline", c[i].State.Pid), 8192)
		if e != nil || !bytes.Equal(cmd, []byte(strings.Join(commands[i], "\x00")+"\x00")) {
			return fail()
		}
		status, e := boundedRead(fmt.Sprintf("/proc/%d/status", c[i].State.Pid), 64<<10)
		if e != nil || !stockReadStatus(status, strconv.Itoa(int(uids[i]))) {
			return fail()
		}
		cwd, e := os.Readlink(fmt.Sprintf("/proc/%d/cwd", c[i].State.Pid))
		if e != nil || cwd != "/" {
			return fail()
		}
	}
	hostPIDNS, hostErr := os.Readlink("/proc/self/ns/pid")
	if hostErr != nil {
		return fail()
	}
	for _, sample := range before {
		if sample.pid == hostPIDNS {
			return fail()
		}
	}
	self, e := os.Readlink("/proc/self/ns/net")
	if e != nil || self == before[0].net {
		return fail()
	}
	all, e := docker.running(ctx)
	if e != nil {
		return fail()
	}
	seen := map[string]bool{}
	for _, row := range all {
		if !digest.MatchString(row.Id) || seen[row.Id] {
			return fail()
		}
		seen[row.Id] = true
		known := false
		for _, id := range p.IDs {
			if row.Id == id {
				known = true
			}
		}
		if known {
			continue
		}
		v, e := docker.inspect(ctx, row.Id)
		if e != nil {
			return fail()
		}
		if v.State.Running && v.State.Pid > 1 {
			ns, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", v.State.Pid))
			if e != nil || ns == before[0].net {
				return fail()
			}
		}
	}
	for _, id := range p.IDs {
		if !seen[id] {
			return fail()
		}
	}
	var tables [2][]byte
	for i, name := range []string{"tcp", "tcp6"} {
		tables[i], err = boundedRead(fmt.Sprintf("/proc/%d/net/%s", c[0].State.Pid, name), 4<<20)
		if err != nil {
			return fail()
		}
	}
	if !stockReadListenerSet(tables, held) {
		return fail()
	}
	for i, id := range p.IDs {
		after, e := sampleStockReadProcess(c[i].State.Pid)
		if e != nil || after != before[i] {
			return fail()
		}
		now, e := docker.inspect(ctx, id)
		if e != nil || !reflect.DeepEqual(now, c[i]) {
			return fail()
		}
	}
	// Repeat membership after process checks. Still a sampled check, not a seal.
	finalRows, finalErr := docker.running(ctx)
	if finalErr != nil || !reflect.DeepEqual(finalRows, all) {
		return fail()
	}
	again, e := loadStockReadProfile()
	if e != nil || again != p || ctx.Err() != nil {
		return fail()
	}
	return stockReadObservation{at: time.Now().UTC(), samples: before, profile: p, pids: [3]int{c[0].State.Pid, c[1].State.Pid, c[2].State.Pid}}, nil
}

func stockReadProfileFile(root *os.Root) ([]byte, os.FileInfo, error) {
	const path = "reviewed-profile.json"
	const limit int64 = 16384
	const uid uint32 = 0
	const executable = false
	if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") {
		return nil, nil, ErrBoundary
	}
	pathInfo, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !pathInfo.Mode().IsRegular() {
		return nil, nil, ErrBoundary
	}
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, ErrBoundary
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, ErrBoundary
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(pathInfo, info) || !info.Mode().IsRegular() || st.Uid != uid || st.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() <= 0 || info.Size() > limit || (executable && info.Mode().Perm()&0111 == 0) || (!executable && info.Mode().Perm()&0077 != 0) {
		return nil, nil, ErrBoundary
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, nil, ErrBoundary
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, nil, ErrBoundary
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || afterStat.Ctim != st.Ctim {
		return nil, nil, ErrBoundary
	}
	finalPath, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !os.SameFile(info, finalPath) {
		return nil, nil, ErrBoundary
	}
	return raw, info, nil
}

func stockReadExecutable(root *os.Root, path, expected string) (os.FileInfo, error) {
	pathInfo, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !pathInfo.Mode().IsRegular() {
		return nil, ErrBoundary
	}

	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrBoundary
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, ErrBoundary
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(pathInfo, before) || !before.Mode().IsRegular() || st.Uid != 0 || st.Nlink != 1 || before.Mode().Perm()&0022 != 0 || before.Mode().Perm()&0111 == 0 || before.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || before.Size() <= 0 || before.Size() > 128<<20 {
		return nil, ErrBoundary
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (128<<20)+1))
	if err != nil || n != before.Size() || hex.EncodeToString(h.Sum(nil)) != expected {
		return nil, ErrBoundary
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrBoundary
	}
	ast, ok := after.Sys().(*syscall.Stat_t)
	if !ok || ast.Ctim != st.Ctim {
		return nil, ErrBoundary
	}
	finalPath, pathErr := providerPathNoLinks(root, path)
	if pathErr != nil || !os.SameFile(after, finalPath) {
		return nil, ErrBoundary
	}
	return after, nil
}
