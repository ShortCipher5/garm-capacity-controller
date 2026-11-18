//go:build linux

// Package nativeprepare stages a fresh, disabled native-owner installation.
// Preparation is not runtime authority; failures permanently reserve the ID.
package nativeprepare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const Parent = "/var/lib/garm-capacity-prepared"
const Version = "native-owner-preparation/v1"

var ErrPreparation = errors.New("native owner preparation unavailable; preserve existing preparation directory and inspect privately")
var executables = []string{"capacity-native-domain", "capacity-capsule-launch", "capacity-native-stage", "capacity-native-image", "capacity-native-zfs-exec", "capacity-native-egress", "capacity-quota-read", "capacity-protected-boot", "capacity-runner-init", "capacity-stock-ingress", "capacity-managed-garm", "capacity-managed-writer", "capacity-gateway", "capacity-provider", "capacity-ledger", "capacity-native-prepare", "capacity-native-preflight", "capacity-native-memory-plan"}
var privateFiles = []string{"capsule.json", "owner-server.crt", "owner-server.key", "owner-client.crt", "owner-client.key", "controller-ca.crt"}

type Manifest struct {
	Version string            `json:"version"`
	ID      string            `json:"id"`
	Files   map[string]string `json:"files"`
}
type Receipt struct {
	Version              string `json:"version"`
	ID                   string `json:"id"`
	State                string `json:"state"`
	ManifestDigest       string `json:"manifest_digest"`
	UnitDigest           string `json:"unit_digest"`
	ActivationAuthorized bool   `json:"activation_authorized"`
}

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func ExecutableNames() []string { return append([]string{}, executables...) }
func names() []string           { n := append([]string{}, executables...); return append(n, privateFiles...) }
func Validate(m Manifest) error {
	if m.Version != Version || !validHash(m.ID) || len(m.Files) != len(names()) {
		return ErrPreparation
	}
	for _, n := range names() {
		if !validHash(m.Files[n]) {
			return ErrPreparation
		}
	}
	return nil
}
func regular(f *os.File, uid uint32, limit int64) error {
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() || s.Size() < 1 || s.Size() > limit || s.Mode().Perm()&0022 != 0 {
		return ErrPreparation
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uid || st.Nlink != 1 {
		return ErrPreparation
	}
	return nil
}
func controlled(path string, uid uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrPreparation
	}
	for p := path; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode().Perm()&0022 != 0 {
			return ErrPreparation
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uid {
			return ErrPreparation
		}
		if p == "/" {
			return nil
		}
	}
}
func read(root *os.Root, name string, uid uint32, limit int64) ([]byte, error) {
	s, e := root.Lstat(name)
	if e != nil || s.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPreparation
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, ErrPreparation
	}
	defer f.Close()
	if regular(f, uid, limit) != nil {
		return nil, ErrPreparation
	}
	if filepath.Base(name) == "capsule.json" || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".crt") {
		st, _ := f.Stat()
		if st.Mode().Perm() != 0600 {
			return nil, ErrPreparation
		}
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrPreparation
	}
	return b, nil
}
func write(root *os.Root, name string, data []byte, mode os.FileMode) error {
	f, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return ErrPreparation
	}
	e = f.Chmod(mode)
	if e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return ErrPreparation
	}
	return nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return ErrPreparation
	}
	defer f.Close()
	return f.Sync()
}

// Prepare requires root-owned protected source and destination ancestors. The
// source is deployment-loaded, never an archive or browser-supplied file path.
func Prepare(ctx context.Context, source string) (Receipt, error) {
	if os.Geteuid() != 0 || ctx == nil || ctx.Err() != nil || controlled(source, 0) != nil || controlled(Parent, 0) != nil {
		return Receipt{}, ErrPreparation
	}
	return prepareAt(ctx, source, Parent, 0, true)
}
func prepareAt(ctx context.Context, source, parent string, uid uint32, check bool) (Receipt, error) {
	src, e := os.OpenRoot(source)
	if e != nil {
		return Receipt{}, ErrPreparation
	}
	defer src.Close()
	raw, e := read(src, "manifest.json", uid, 16384)
	if e != nil {
		return Receipt{}, e
	}
	var m Manifest
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || Validate(m) != nil {
		return Receipt{}, ErrPreparation
	}
	// Exact source inventory: no archives, extra executables or unbound files.
	entries, e := os.ReadDir(source)
	if e != nil || len(entries) != len(m.Files)+1 {
		return Receipt{}, ErrPreparation
	}
	for _, v := range entries {
		if v.Name() != "manifest.json" {
			if _, ok := m.Files[v.Name()]; !ok {
				return Receipt{}, ErrPreparation
			}
		}
	}
	canonical, _ := json.Marshal(m)
	digest := hash(canonical)
	dest := filepath.Join(parent, m.ID)
	// Exclusive directory creation is permanent consumed intent, including failures.
	if os.Mkdir(dest, 0700) != nil {
		return Receipt{}, ErrPreparation
	}
	if os.Chmod(dest, 0700) != nil {
		return Receipt{}, ErrPreparation
	}
	if syncDir(parent) != nil {
		return Receipt{}, ErrPreparation
	}
	dst, e := os.OpenRoot(dest)
	if e != nil {
		return Receipt{}, ErrPreparation
	}
	defer dst.Close()
	if write(dst, "intent.json", []byte(`{"state":"preparing","manifest_digest":"`+digest+`"}`), 0600) != nil || syncDir(dest) != nil {
		return Receipt{}, ErrPreparation
	}
	if write(dst, "manifest.json", canonical, 0600) != nil {
		return Receipt{}, ErrPreparation
	}
	if dst.Mkdir("bin", 0700) != nil || dst.Mkdir("private", 0700) != nil {
		return Receipt{}, ErrPreparation
	}
	if dst.Chmod("bin", 0700) != nil || dst.Chmod("private", 0700) != nil {
		return Receipt{}, ErrPreparation
	}
	ordered := names()
	sort.Strings(ordered)
	var total int64
	for _, name := range ordered {
		if ctx.Err() != nil {
			return Receipt{}, ErrPreparation
		}
		binary := strings.HasPrefix(name, "capacity-")
		limit := int64(1 << 20)
		mode := os.FileMode(0600)
		sub := "private/"
		if binary {
			limit = 64 << 20
			mode = 0555
			sub = "bin/"
		}
		b, e := read(src, name, uid, limit)
		total += int64(len(b))
		if e != nil || total > 512<<20 || hash(b) != m.Files[name] || (binary && !validELF(b)) {
			clear(b)
			return Receipt{}, ErrPreparation
		}
		e = write(dst, sub+name, b, mode)
		clear(b)
		if e != nil {
			return Receipt{}, ErrPreparation
		}
	}
	if syncDir(filepath.Join(dest, "bin")) != nil || syncDir(filepath.Join(dest, "private")) != nil {
		return Receipt{}, ErrPreparation
	}
	if check {
		configBytes, e := read(dst, "private/capsule.json", uid, 1<<20)
		if e != nil || validateConfigPaths(configBytes, dest) != nil || validateHelperPaths(configBytes, dest, m) != nil {
			clear(configBytes)
			return Receipt{}, ErrPreparation
		}
		clear(configBytes)
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		owner, e := dst.Open("bin/capacity-native-domain")
		if e != nil {
			return Receipt{}, ErrPreparation
		}
		defer owner.Close()
		cmd := exec.CommandContext(bounded, "/proc/self/exe", "--internal-check-owner", m.Files["capacity-native-domain"], filepath.Join(dest, "private/capsule.json"))
		cmd.ExtraFiles = []*os.File{owner}
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		cmd.Dir = dest
		if cmd.Run() != nil {
			return Receipt{}, ErrPreparation
		}
	}
	unit := stagedUnit(dest)
	if write(dst, "garm-capacity-native-domain.service.staged", unit, 0600) != nil {
		return Receipt{}, ErrPreparation
	}
	result := Receipt{Version, m.ID, "prepared_not_activated", digest, hash(unit), false}
	out, _ := json.Marshal(result)
	if write(dst, "receipt.json", out, 0600) != nil || syncDir(dest) != nil {
		return Receipt{}, ErrPreparation
	}
	return result, nil
}

func validateConfigPaths(raw []byte, dest string) error {
	var c struct {
		Control struct {
			ServerCertificate string `json:"server_certificate"`
			ServerKey         string `json:"server_key"`
			OwnerCertificate  string `json:"owner_certificate"`
			OwnerKey          string `json:"owner_key"`
			ControllerCA      string `json:"controller_ca"`
		} `json:"control"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ErrPreparation
	}
	v := c.Control
	for _, pair := range [][2]string{{v.ServerCertificate, "owner-server.crt"}, {v.ServerKey, "owner-server.key"}, {v.OwnerCertificate, "owner-client.crt"}, {v.OwnerKey, "owner-client.key"}, {v.ControllerCA, "controller-ca.crt"}} {
		if pair[0] != filepath.Join(dest, "private", pair[1]) {
			return ErrPreparation
		}
	}
	return nil
}

// Inspect returns only an exact completed receipt and rechecks all staged bytes.
// A partial directory has no successful receipt and is never retried or adopted.
func Inspect(ctx context.Context, id string) (Receipt, error) {
	if os.Geteuid() != 0 || ctx == nil || ctx.Err() != nil || !validHash(id) || controlled(Parent, 0) != nil {
		return Receipt{}, ErrPreparation
	}
	return inspectAt(ctx, Parent, id, 0)
}
func inspectAt(ctx context.Context, parent, id string, uid uint32) (Receipt, error) {
	dest := filepath.Join(parent, id)
	for _, p := range []string{dest, filepath.Join(dest, "bin"), filepath.Join(dest, "private")} {
		if !exactDirectory(p, uid) {
			return Receipt{}, ErrPreparation
		}
	}
	root, e := os.OpenRoot(dest)
	if e != nil {
		return Receipt{}, ErrPreparation
	}
	defer root.Close()
	raw, e := read(root, "manifest.json", uid, 16384)
	if e != nil {
		return Receipt{}, e
	}
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || Validate(m) != nil || m.ID != id {
		return Receipt{}, ErrPreparation
	}
	canonical, _ := json.Marshal(m)
	intent, e := read(root, "intent.json", uid, 16384)
	if e != nil || string(intent) != `{"state":"preparing","manifest_digest":"`+hash(canonical)+`"}` {
		return Receipt{}, ErrPreparation
	}
	out, e := read(root, "receipt.json", uid, 16384)
	if e != nil {
		return Receipt{}, e
	}
	var r Receipt
	if json.Unmarshal(out, &r) != nil || r.Version != Version || r.ID != id || r.State != "prepared_not_activated" || r.ActivationAuthorized || r.ManifestDigest != hash(canonical) {
		return Receipt{}, ErrPreparation
	}
	for _, name := range names() {
		if ctx.Err() != nil {
			return Receipt{}, ErrPreparation
		}
		p := "private/"
		limit := int64(1 << 20)
		if strings.HasPrefix(name, "capacity-") {
			p = "bin/"
			limit = 64 << 20
		}
		info, se := root.Lstat(p + name)
		mode := os.FileMode(0600)
		if p == "bin/" {
			mode = 0555
		}
		if se != nil || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return Receipt{}, ErrPreparation
		}
		b, e := read(root, p+name, uid, limit)
		ok := e == nil && hash(b) == m.Files[name]
		clear(b)
		if !ok {
			return Receipt{}, ErrPreparation
		}
	}
	unit, e := read(root, "garm-capacity-native-domain.service.staged", uid, 16384)
	if e != nil || hash(unit) != r.UnitDigest || !bytes.Equal(unit, stagedUnit(dest)) {
		return Receipt{}, ErrPreparation
	}
	return r, nil
}

func stagedUnit(dest string) []byte {
	return []byte("# PREPARED ONLY: not installed, enabled, or qualified for activation.\n[Unit]\nDescription=Prepared native owner (activation refused)\n[Service]\nType=exec\nUser=root\nGroup=root\nUMask=0077\nExecStart=" + dest + "/bin/capacity-native-domain --capsule-config=" + dest + "/private/capsule.json\nRestart=no\nProtectSystem=strict\nStandardOutput=null\nStandardError=null\n")
}

func FileNames() []string { return names() }
func ManifestDigest(m Manifest) (string, error) {
	if Validate(m) != nil {
		return "", ErrPreparation
	}
	b, e := json.Marshal(m)
	if e != nil {
		return "", ErrPreparation
	}
	return hash(b), nil
}
func ValidateReceipt(m Manifest, r Receipt) error {
	digest, e := ManifestDigest(m)
	if e != nil || r.Version != Version || r.ID != m.ID || r.State != "prepared_not_activated" || r.ActivationAuthorized || r.ManifestDigest != digest || r.UnitDigest != hash(stagedUnit(filepath.Join(Parent, m.ID))) {
		return ErrPreparation
	}
	return nil
}

func validateHelperPaths(raw []byte, dest string, m Manifest) error {
	type helper struct{ Helper, HelperSHA256 string }
	var c struct {
		Launch       struct{ Launcher, LauncherSHA256, StageSHA256 string }
		Storage      helper
		Artifacts    helper
		NetworkTools helper `json:"network_tools"`
		QuotaReader  helper `json:"quota_reader"`
		Control      struct {
			PoolReader helper `json:"pool_reader"`
		}
		RuntimeQuotaHelperSHA256 string `json:"runtime_quota_helper_sha256"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ErrPreparation
	}
	if c.Launch.Launcher != filepath.Join(dest, "bin/capacity-capsule-launch") || c.Launch.LauncherSHA256 != m.Files["capacity-capsule-launch"] || c.Launch.StageSHA256 != m.Files["capacity-native-stage"] || c.RuntimeQuotaHelperSHA256 != m.Files["capacity-quota-read"] {
		return ErrPreparation
	}
	for _, pair := range []struct {
		h    helper
		name string
	}{{c.Storage, "capacity-native-zfs-exec"}, {c.Artifacts, "capacity-native-zfs-exec"}, {c.NetworkTools, "capacity-native-egress"}, {c.QuotaReader, "capacity-quota-read"}, {c.Control.PoolReader, "capacity-quota-read"}} {
		if pair.h.Helper != filepath.Join(dest, "bin", pair.name) || pair.h.HelperSHA256 != m.Files[pair.name] {
			return ErrPreparation
		}
	}
	return nil
}

// CheckChild is the fixed self-reexec path. Close inherited descriptors in this
// child only, then execute the held hash-verified owner without a shell.
func CheckChild(args []string) error {
	if os.Geteuid() != 0 || len(args) != 2 || !validHash(args[0]) {
		return ErrPreparation
	}
	config := args[1]
	id := filepath.Base(filepath.Dir(filepath.Dir(config)))
	if !validHash(id) || config != filepath.Join(Parent, id, "private/capsule.json") {
		return ErrPreparation
	}
	if sanitizeChildDescriptors() != nil {
		return ErrPreparation
	}
	f := os.NewFile(3, "pinned-owner")
	if f == nil || regular(f, 0, 64<<20) != nil {
		return ErrPreparation
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if e != nil || len(raw) > 64<<20 || hash(raw) != args[0] {
		clear(raw)
		return ErrPreparation
	}
	clear(raw)
	unix.CloseOnExec(3)
	return unix.Exec("/proc/self/fd/3", []string{"capacity-native-domain", "--check-capsule-config", "--capsule-config", config}, []string{"PATH=/usr/bin:/bin", "LANG=C"})
}
func exactDirectory(path string, uid uint32) bool {
	s, e := os.Lstat(path)
	if e != nil || !s.IsDir() || s.Mode().Perm() != 0700 || s.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uid
}

func sanitizeChildDescriptors() error { return unix.CloseRange(4, ^uint(0), unix.CLOSE_RANGE_CLOEXEC) }
func validELF(raw []byte) bool {
	f, e := elf.NewFile(bytes.NewReader(raw))
	if e != nil {
		return false
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return false
	}
	return runtime.GOARCH == "amd64" && f.Machine == elf.EM_X86_64 || runtime.GOARCH == "arm64" && f.Machine == elf.EM_AARCH64
}
