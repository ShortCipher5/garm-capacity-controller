// Exported disposable test probe. Never included in a runtime image or installer.
package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const ok = "ok"

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var simpleNonce = regexp.MustCompile(`^[a-z0-9-]{1,128}$`)
var safeAsset = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)
var libraryName = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)

func envPolicy(lookup func(string) (string, bool)) string {
	for _, k := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "GODEBUG", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT"} {
		if _, set := lookup(k); set {
			return "environment-override"
		}
	}
	return ok
}
func client(wrongName bool) *http.Client {
	t := &tls.Config{MinVersion: tls.VersionTLS13} // RootCAs intentionally NIL: actual default roots.
	if wrongName {
		t.ServerName = "wrong.example.invalid"
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: t, DisableKeepAlives: true}, Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
func tlsKind(err error) string {
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &authority) {
		return "unknown-authority"
	}
	if errors.As(err, &hostname) {
		return "hostname"
	}
	return "transport"
}
func response(c *http.Client, url string, max int64) ([]byte, string, string) {
	r, e := c.Get(url)
	if e != nil {
		return nil, "", tlsKind(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, "", "http-status"
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, max+1))
	if e != nil {
		return nil, "", "transport"
	}
	if int64(len(b)) > max {
		return nil, "", "response-too-large"
	}
	return b, r.Header.Get("Content-Type"), ok
}
func endpoint(port int) string { return "https://127.0.0.1:" + strconv.Itoa(port) }
func health(port int, nonce string, wrongName bool) string {
	if port < 1024 || port > 65535 || !simpleNonce.MatchString(nonce) {
		return "invalid-input"
	}
	b, _, k := response(client(wrongName), endpoint(port)+"/health", 1024)
	if k != ok {
		return k
	}
	if string(b) != nonce {
		return "body-mismatch"
	}
	return ok
}
func inventory(data []byte) (map[string]string, error) {
	if len(data) > 65536 {
		return nil, errors.New("inventory")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, errors.New("inventory")
	}
	out := map[string]string{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, errors.New("inventory")
		}
		name, isString := k.(string)
		if !isString {
			return nil, errors.New("inventory")
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("inventory")
		}
		var sum string
		if d.Decode(&sum) != nil || !safeAsset.MatchString(name) || name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "..") || !hexDigest.MatchString(sum) {
			return nil, errors.New("inventory")
		}
		out[name] = sum
		if len(out) > 256 {
			return nil, errors.New("inventory")
		}
	}
	token, e = d.Token()
	if e != nil || token != json.Delim('}') {
		return nil, errors.New("inventory")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("inventory")
	}
	if len(out) < 20 || out["index.html"] == "" {
		return nil, errors.New("inventory")
	}
	return out, nil
}
func sum(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func assetMIME(name, header string) bool {
	expected := "text/plain"
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		expected = "text/html"
	case ".js":
		expected = "application/javascript"
	case ".css":
		expected = "text/css"
	case ".json":
		expected = "application/json"
	case ".svg":
		expected = "image/svg+xml"
	case ".png":
		expected = "image/png"
	}
	actual, _, e := mime.ParseMediaType(header)
	return e == nil && actual == expected
}
func assets(port int, data []byte) string {
	if port < 1024 || port > 65535 {
		return "invalid-input"
	}
	wanted, e := inventory(data)
	if e != nil {
		return "invalid-input"
	}
	c := client(false)
	// Establish actual root UI before attributing any bundle-specific failure.
	for _, route := range []string{"/ui/", "/ui/disposable-generated-route"} {
		b, contentType, k := response(c, endpoint(port)+route, 16<<20)
		if k != ok {
			return k
		}
		if sum(b) != wanted["index.html"] || !assetMIME("index.html", contentType) {
			return "asset-mismatch"
		}
	}
	for name, expected := range wanted {
		r, e := c.Get(endpoint(port) + "/ui/" + name)
		if e != nil {
			return tlsKind(e)
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, (16<<20)+1))
		r.Body.Close()
		if e != nil {
			return "transport"
		}
		if r.StatusCode != 200 || len(b) > 16<<20 || sum(b) != expected {
			return "asset-mismatch"
		}
		if !assetMIME(name, r.Header.Get("Content-Type")) {
			return "asset-mismatch"
		}
	}
	return ok
}
func status(data []byte, uid int) string {
	if uid < 1 || uid > 65535 || len(data) > 65536 {
		return "invalid-input"
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if _, duplicate := fields[k]; duplicate {
			return "identity-mismatch"
		}
		fields[k] = strings.TrimSpace(v)
	}
	for _, k := range []string{"Uid", "Gid"} {
		f := strings.Fields(fields[k])
		if len(f) != 4 {
			return "identity-mismatch"
		}
		for _, value := range f {
			n, e := strconv.Atoi(value)
			if e != nil || n != uid {
				return "identity-mismatch"
			}
		}
	}
	groups, present := fields["Groups"]
	if !present {
		return "identity-mismatch"
	}
	groupList := strings.Fields(groups)
	if len(groupList) > 1 {
		return "identity-mismatch"
	}
	for _, value := range groupList {
		n, e := strconv.Atoi(value)
		if e != nil || n != uid {
			return "identity-mismatch"
		}
	}
	for _, k := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		v := fields[k]
		if len(v) < 1 || len(v) > 16 {
			return "identity-mismatch"
		}
		n, e := strconv.ParseUint(v, 16, 64)
		if e != nil || n != 0 {
			return "identity-mismatch"
		}
	}
	if fields["NoNewPrivs"] != "1" {
		return "identity-mismatch"
	}
	return ok
}
func readLimit(name string, maximum int64) ([]byte, error) {
	f, e := os.Open(name)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, maximum+1))
	if e != nil || int64(len(b)) > maximum {
		return nil, errors.New("bounded read")
	}
	return b, nil
}
func identity(role string) string {
	base, uid, exe := "/proc/1", 65531, "garm"
	if role == "tools" {
		base, uid, exe = "/proc/self", 65533, "profile-probe"
	} else if role != "garm" {
		return "invalid-input"
	}
	b, e := readLimit(base+"/status", 65536)
	if e != nil {
		return "filesystem"
	}
	k := status(b, uid)
	if k != ok {
		return k
	}
	target, e := os.Readlink(base + "/exe")
	if e != nil {
		return "filesystem"
	}
	if filepath.Base(target) != exe {
		return "identity-mismatch"
	}
	return ok
}

type elfMetadata struct {
	interpreter string
	needed      []string
	rpath       bool
}

func parseELF(data []byte) (elfMetadata, error) {
	f, e := elf.NewFile(bytes.NewReader(data))
	if e != nil {
		return elfMetadata{}, errors.New("elf")
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != elf.EM_X86_64 || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return elfMetadata{}, errors.New("elf")
	}
	var m elfMetadata
	interpreters := 0
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			interpreters++
			if interpreters != 1 {
				return m, errors.New("elf")
			}
			if p.Filesz > 4096 {
				return m, errors.New("elf")
			}
			b, e := io.ReadAll(p.Open())
			if e != nil {
				return m, errors.New("elf")
			}
			m.interpreter = strings.TrimRight(string(b), "\x00")
		}
	}
	m.needed, e = f.ImportedLibraries()
	if e != nil {
		return m, errors.New("elf")
	}
	for _, tag := range []elf.DynTag{elf.DT_RPATH, elf.DT_RUNPATH} {
		v, e := f.DynString(tag)
		if e != nil {
			return m, errors.New("elf")
		}
		if len(v) > 0 {
			m.rpath = true
		}
	}
	return m, nil
}
func checkELF(m elfMetadata, exists func(string) (bool, error)) string {
	if m.interpreter != "/lib64/ld-linux-x86-64.so.2" || len(m.needed) == 0 || m.rpath {
		return "unsupported-elf"
	}
	present, e := exists(m.interpreter)
	if e != nil {
		return "filesystem"
	}
	if !present {
		return "missing-loader"
	}
	dirs := []string{"/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "/lib64", "/usr/lib64", "/lib", "/usr/lib"}
	for _, name := range m.needed {
		if !libraryName.MatchString(name) || name == "." || name == ".." {
			return "unsupported-elf"
		}
		found := false
		for _, dir := range dirs {
			present, e := exists(dir + "/" + name)
			if e != nil {
				return "filesystem"
			}
			found = found || present
		}
		if !found {
			return "missing-library"
		}
	}
	return ok
}
func binary(kind, expected string) string {
	files := map[string]string{"garm": "/usr/local/bin/garm", "capacity-provider": "/usr/local/bin/capacity-provider", "garm-cli": "/usr/local/bin/garm-cli"}
	name, allowed := files[kind]
	if !allowed || !hexDigest.MatchString(expected) {
		return "invalid-input"
	}
	info, e := os.Lstat(name)
	if e != nil {
		return "filesystem"
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0555 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Size() > 128<<20 {
		return "unsupported-elf"
	}
	b, e := readLimit(name, 128<<20)
	if e != nil {
		return "filesystem"
	}
	if sum(b) != expected {
		return "artifact-mismatch"
	}
	infoBuild, e := buildinfo.Read(bytes.NewReader(b))
	if e != nil || infoBuild.GoVersion != "go1.26.10" {
		return "artifact-mismatch"
	}
	cgo := false
	for _, setting := range infoBuild.Settings {
		if setting.Key == "CGO_ENABLED" && setting.Value == "1" {
			cgo = true
		}
	}
	if !cgo {
		return "artifact-mismatch"
	}
	m, e := parseELF(b)
	if e != nil {
		return "unsupported-elf"
	}
	return checkELF(m, func(p string) (bool, error) {
		s, e := os.Stat(p)
		if os.IsNotExist(e) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		return s.Mode().IsRegular(), nil
	})
}
func network(expected string) string {
	if !regexp.MustCompile(`^net:\[[0-9]+\]$`).MatchString(expected) {
		return "invalid-input"
	}
	current, e := os.Readlink("/proc/self/ns/net")
	if e != nil {
		return "filesystem"
	}
	if current != expected {
		return "network-mismatch"
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		return "filesystem"
	}
	if len(interfaces) != 1 || interfaces[0].Name != "lo" || interfaces[0].Flags&net.FlagLoopback == 0 {
		return "network-mismatch"
	}
	addresses, e := interfaces[0].Addrs()
	if e != nil {
		return "filesystem"
	}
	if len(addresses) == 0 {
		return "network-mismatch"
	}
	for _, a := range addresses {
		ip, _, e := net.ParseCIDR(a.String())
		if e != nil || !ip.IsLoopback() {
			return "network-mismatch"
		}
	}
	return ok
}
func canary(expected string) string {
	return canaryOpen(expected, func() (*os.File, error) { return os.OpenFile("/probe/canary/value", os.O_RDWR, 0) })
}
func canaryOpen(expected string, opener func() (*os.File, error)) string {
	if !simpleNonce.MatchString(expected) {
		return "invalid-input"
	}
	f, e := opener()
	if errors.Is(e, os.ErrPermission) {
		return "permission-denied"
	}
	if e != nil {
		return "filesystem"
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 129))
	if e != nil || len(b) > 128 {
		return "filesystem"
	}
	if string(b) != expected {
		return "body-mismatch"
	}
	n, e := f.WriteAt(b, 0)
	if errors.Is(e, os.ErrPermission) {
		return "permission-denied"
	}
	if e != nil || n != len(b) {
		return "filesystem"
	}
	return ok
}
func emit(kind string) int {
	// Never emit paths, nonce, HTTP body, certificate material, raw errors or auth.
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": kind, "test_probe_only": true, "installed_stack_qualified": false, "runtime_profile_qualified": false, "startup_authority": false, "deployment_authorized": false})
	codes := map[string]int{ok: 0, "unknown-authority": 42, "hostname": 43, "missing-loader": 44, "asset-mismatch": 45, "identity-mismatch": 46, "body-mismatch": 47, "missing-library": 48, "unsupported-elf": 49, "artifact-mismatch": 50, "http-status": 51, "response-too-large": 52, "invalid-input": 64, "environment-override": 64, "transport": 65, "filesystem": 66, "permission-denied": 53, "network-mismatch": 54, "unapproved-context": 67}
	if n, known := codes[kind]; known {
		return n
	}
	return 68
}
func main() {
	// Convention only, NOT authorization/access control. The future harness must
	// first validate that this is its NEW disposable container, never a live host.
	if os.Getenv("PROFILE_DISPOSABLE_LAB") != "generated-image-qualification/v1" {
		os.Exit(emit("unapproved-context"))
	}
	if k := envPolicy(os.LookupEnv); k != ok {
		os.Exit(emit(k))
	}
	f := flag.NewFlagSet("profile-probe", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	netns := f.String("netns", "", "expected generated network namespace")
	action := f.String("action", "", "test action")
	port := f.Int("port", 0, "loopback port")
	nonce := f.String("nonce", "", "generated response")
	wrong := f.Bool("wrong-name", false, "negative TLS name")
	role := f.String("role", "", "generated role")
	name := f.String("binary", "", "public binary kind")
	digest := f.String("sha256", "", "public artifact digest")
	if f.Parse(os.Args[1:]) != nil || f.NArg() != 0 {
		os.Exit(emit("invalid-input"))
	}
	kind := "invalid-input"
	switch *action {
	case "health":
		kind = health(*port, *nonce, *wrong)
	case "assets":
		b, e := readLimit("/probe/public-assets.json", 65536)
		if e != nil {
			kind = "filesystem"
		} else {
			kind = assets(*port, b)
		}
	case "identity":
		kind = identity(*role)
	case "canary":
		kind = canary(*nonce)
	case "network":
		kind = network(*netns)
	case "elf":
		kind = binary(*name, *digest)
	}
	os.Exit(emit(kind))
}
