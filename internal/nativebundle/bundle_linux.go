//go:build linux

// Package nativebundle builds the exact private input consumed by nativeprepare.
// It executes no artifact and grants no host or activation authority.
package nativebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
)

var ErrBundle = errors.New("native bundle unavailable; preserve any partial output and inspect private inputs")

type Configuration struct{ ExportDirectory, ExportManifestSHA256, PrivateDirectory, OutputDirectory, ID, Architecture string }
type Result struct {
	ID                   string `json:"id"`
	ManifestDigest       string `json:"manifest_digest"`
	Architecture         string `json:"architecture"`
	State                string `json:"state"`
	ActivationAuthorized bool   `json:"activation_authorized"`
}

func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func hash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == v
}
func protectedDirectory(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return ErrBundle
	}
	for x := p; ; x = filepath.Dir(x) {
		s, e := os.Lstat(x)
		if e != nil || !s.IsDir() {
			return ErrBundle
		}
		v, ok := s.Sys().(*syscall.Stat_t)
		if !ok || (v.Uid != 0 && v.Uid != uint32(os.Getuid())) {
			return ErrBundle
		}
		if s.Mode().Perm()&0022 != 0 && !(v.Uid == 0 && s.Mode()&os.ModeSticky != 0 && x != p) {
			return ErrBundle
		}
		if x == "/" {
			return nil
		}
	}
}
func read(r *os.Root, name string, limit int64, secret bool) ([]byte, error) {
	s, e := r.Lstat(name)
	if e != nil || !s.Mode().IsRegular() || s.Size() < 1 || s.Size() > limit || s.Mode().Perm()&0022 != 0 || (secret && s.Mode().Perm() != 0600) {
		return nil, ErrBundle
	}
	v, ok := s.Sys().(*syscall.Stat_t)
	if !ok || v.Nlink != 1 || (v.Uid != 0 && v.Uid != uint32(os.Getuid())) {
		return nil, ErrBundle
	}
	f, e := r.Open(name)
	if e != nil {
		return nil, ErrBundle
	}
	defer f.Close()
	current, e := f.Stat()
	if e != nil || !os.SameFile(s, current) {
		return nil, ErrBundle
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrBundle
	}
	return b, nil
}
func write(r *os.Root, name string, b []byte) error {
	f, e := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return ErrBundle
	}
	e = f.Chmod(0600)
	if e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrBundle
	}
	return nil
}
func syncDirectory(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return ErrBundle
	}
	defer f.Close()
	return f.Sync()
}
func checksumManifest(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	allow := map[string]bool{}
	for _, n := range nativeprepare.ExecutableNames() {
		allow[n] = true
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if len(line) < 67 || line[64:66] != "  " {
			return nil, ErrBundle
		}
		h, n := line[:64], line[66:]
		if !hash(h) || !allow[n] || out[n] != "" {
			return nil, ErrBundle
		}
		out[n] = h
	}
	if len(out) != len(allow) {
		return nil, ErrBundle
	}
	return out, nil
}
func executable(raw []byte, arch string) bool {
	f, e := elf.NewFile(bytes.NewReader(raw))
	if e != nil {
		return false
	}
	defer f.Close()
	return f.Class == elf.ELFCLASS64 && f.Data == elf.ELFDATA2LSB && (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN) && (arch == "amd64" && f.Machine == elf.EM_X86_64 || arch == "arm64" && f.Machine == elf.EM_AARCH64)
}

// Build validates the explicitly pinned checksum manifest and private inputs,
// then writes a fresh protected flat bundle. Partial output is never reused.
func Build(ctx context.Context, c Configuration) (Result, error) {
	if ctx == nil || ctx.Err() != nil || !hash(c.ID) || !hash(c.ExportManifestSHA256) || (c.Architecture != "amd64" && c.Architecture != "arm64") || protectedDirectory(c.ExportDirectory) != nil || protectedDirectory(c.PrivateDirectory) != nil || protectedDirectory(filepath.Dir(c.OutputDirectory)) != nil || !filepath.IsAbs(c.OutputDirectory) || filepath.Clean(c.OutputDirectory) != c.OutputDirectory {
		return Result{}, ErrBundle
	}
	export, e := os.OpenRoot(c.ExportDirectory)
	if e != nil {
		return Result{}, ErrBundle
	}
	defer export.Close()
	private, e := os.OpenRoot(c.PrivateDirectory)
	if e != nil {
		return Result{}, ErrBundle
	}
	defer private.Close()
	sums, e := read(export, "SHA256SUMS", 16<<10, false)
	if e != nil || digest(sums) != c.ExportManifestSHA256 {
		return Result{}, ErrBundle
	}
	expected, e := checksumManifest(sums)
	if e != nil {
		return Result{}, e
	}
	raw, e := read(private, "capsule.json", 1<<20, true)
	if e != nil {
		return Result{}, e
	}
	config, e := decodeObject(raw)
	clear(raw)
	if e != nil {
		return Result{}, e
	}
	secret := map[string][]byte{}
	defer func() {
		for _, b := range secret {
			clear(b)
		}
	}()
	for _, n := range []string{"owner-server.crt", "owner-server.key", "owner-client.crt", "owner-client.key", "controller-ca.crt"} {
		b, e := read(private, n, 1<<20, true)
		if e != nil {
			return Result{}, e
		}
		secret[n] = b
	}
	if rebind(config, c.ID, expected, secret) != nil {
		return Result{}, ErrBundle
	}
	configRaw, e := json.Marshal(config)
	if e != nil || len(configRaw) > 1<<20 {
		return Result{}, ErrBundle
	}
	defer clear(configRaw)
	// Output creation is exclusive; failures leave an incomplete quarantined bundle.
	if os.Mkdir(c.OutputDirectory, 0700) != nil {
		return Result{}, ErrBundle
	}
	if os.Chmod(c.OutputDirectory, 0700) != nil || syncDirectory(filepath.Dir(c.OutputDirectory)) != nil {
		return Result{}, ErrBundle
	}
	out, e := os.OpenRoot(c.OutputDirectory)
	if e != nil {
		return Result{}, ErrBundle
	}
	defer out.Close()
	m := nativeprepare.Manifest{Version: nativeprepare.Version, ID: c.ID, Files: map[string]string{}}
	total := len(configRaw)
	for _, n := range nativeprepare.ExecutableNames() {
		if ctx.Err() != nil {
			return Result{}, ErrBundle
		}
		b, e := read(export, n, 64<<20, false)
		total += len(b)
		if e != nil || total > 512<<20 || digest(b) != expected[n] || !executable(b, c.Architecture) {
			clear(b)
			return Result{}, ErrBundle
		}
		e = write(out, n, b)
		clear(b)
		if e != nil {
			return Result{}, e
		}
		m.Files[n] = expected[n]
	}
	for n, b := range secret {
		total += len(b)
		if total > 512<<20 || write(out, n, b) != nil {
			return Result{}, ErrBundle
		}
		m.Files[n] = digest(b)
	}
	if write(out, "capsule.json", configRaw) != nil {
		return Result{}, ErrBundle
	}
	m.Files["capsule.json"] = digest(configRaw)
	manifest, e := json.Marshal(m)
	if e != nil || nativeprepare.Validate(m) != nil || write(out, "manifest.json", manifest) != nil || syncDirectory(c.OutputDirectory) != nil {
		return Result{}, ErrBundle
	}
	md, e := nativeprepare.ManifestDigest(m)
	if e != nil {
		return Result{}, ErrBundle
	}
	return Result{c.ID, md, c.Architecture, "bundle_created_not_prepared", false}, nil
}

// Duplicate (including case-aliased) object keys are rejected before rewriting.
func decodeObject(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, e := value(d, 0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrBundle
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrBundle
	}
	return m, nil
}
func value(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, ErrBundle
	}
	t, e := d.Token()
	if e != nil {
		return nil, ErrBundle
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	if delim == '{' {
		m := map[string]any{}
		seen := []string{}
		for d.More() {
			k, e := d.Token()
			s, ok := k.(string)
			if e != nil || !ok || len(seen) >= 1024 {
				return nil, ErrBundle
			}
			for _, prior := range seen {
				if strings.EqualFold(prior, s) {
					return nil, ErrBundle
				}
			}
			seen = append(seen, s)
			v, e := value(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[s] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, ErrBundle
		}
		return m, nil
	}
	if delim == '[' {
		a := []any{}
		for d.More() {
			v, e := value(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, ErrBundle
		}
		return a, nil
	}
	return nil, ErrBundle
}
func get(m map[string]any, k string) any {
	for name, v := range m {
		if strings.EqualFold(name, k) {
			return v
		}
	}
	return nil
}
func object(m map[string]any, k string) (map[string]any, error) {
	v, ok := get(m, k).(map[string]any)
	if !ok {
		return nil, ErrBundle
	}
	return v, nil
}
func set(m map[string]any, k string, v any) {
	for name := range m {
		if strings.EqualFold(name, k) {
			delete(m, name)
		}
	}
	m[k] = v
}
func rebind(c map[string]any, id string, sums map[string]string, secrets map[string][]byte) error {
	dest := filepath.Join(nativeprepare.Parent, id)
	control, e := object(c, "control")
	if e != nil {
		return e
	}
	policy, e := object(control, "policy")
	if e != nil {
		return e
	}
	if get(policy, "version") != "garm-native-policy/v5" || (get(policy, "domain_id") != nil && get(policy, "domain_id") != "") || (get(policy, "egress_digest") != nil && get(policy, "egress_digest") != "") {
		return ErrBundle
	}
	server, e := tls.X509KeyPair(secrets["owner-server.crt"], secrets["owner-server.key"])
	if e != nil {
		return ErrBundle
	}
	client, e := tls.X509KeyPair(secrets["owner-client.crt"], secrets["owner-client.key"])
	if e != nil {
		return ErrBundle
	}
	serverLeaf, e := x509.ParseCertificate(server.Certificate[0])
	if e != nil {
		return ErrBundle
	}
	clientLeaf, e := x509.ParseCertificate(client.Certificate[0])
	if e != nil || bytes.Equal(serverLeaf.RawSubjectPublicKeyInfo, clientLeaf.RawSubjectPublicKeyInfo) {
		return ErrBundle
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(secrets["controller-ca.crt"]) {
		return ErrBundle
	}
	seen := map[string]bool{}
	for _, v := range []any{digest(server.Certificate[0]), digest(client.Certificate[0]), get(control, "controller_client_leaf"), get(control, "controller_server_leaf")} {
		h, ok := v.(string)
		if !ok || !hash(h) || seen[h] {
			return ErrBundle
		}
		seen[h] = true
	}
	for key, name := range map[string]string{"server_certificate": "owner-server.crt", "server_key": "owner-server.key", "owner_certificate": "owner-client.crt", "owner_key": "owner-client.key", "controller_ca": "controller-ca.crt"} {
		set(control, key, filepath.Join(dest, "private", name))
	}
	launch, e := object(c, "launch")
	if e != nil {
		return e
	}
	for _, key := range []string{"Image", "Registry", "StateParent"} {
		if v := get(launch, key); v != nil && v != "" {
			return ErrBundle
		}
	}
	set(launch, "Launcher", filepath.Join(dest, "bin/capacity-capsule-launch"))
	set(launch, "LauncherSHA256", sums["capacity-capsule-launch"])
	set(launch, "StageSHA256", sums["capacity-native-stage"])
	set(policy, "launcher_sha256", sums["capacity-native-stage"])
	set(c, "runtime_quota_helper_sha256", sums["capacity-quota-read"])
	for _, pair := range [][2]string{{"storage", "capacity-native-zfs-exec"}, {"artifacts", "capacity-native-zfs-exec"}, {"network_tools", "capacity-native-egress"}, {"quota_reader", "capacity-quota-read"}} {
		m, e := object(c, pair[0])
		if e != nil {
			return e
		}
		set(m, "Helper", filepath.Join(dest, "bin", pair[1]))
		set(m, "HelperSHA256", sums[pair[1]])
	}
	pool, e := object(control, "pool_reader")
	if e != nil {
		return e
	}
	set(pool, "Helper", filepath.Join(dest, "bin/capacity-quota-read"))
	set(pool, "HelperSHA256", sums["capacity-quota-read"])
	return nil
}
