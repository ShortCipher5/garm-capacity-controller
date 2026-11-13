package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal("fixture key")
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Generated probe root"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, e := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if e != nil {
		t.Fatal("fixture root")
	}
	parsed, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal("fixture root parse")
	}
	leafKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal("fixture leaf key")
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Generated probe leaf"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, e := x509.CreateCertificate(rand.Reader, leaf, parsed, &leafKey.PublicKey, key)
	if e != nil {
		t.Fatal("fixture leaf")
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, der}, PrivateKey: leafKey}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func child(t *testing.T, port int, ca []byte, wrong bool, inventoryFile string) string {
	t.Helper()
	root := t.TempDir()
	cert := filepath.Join(root, "generated-ca.pem")
	if os.WriteFile(cert, ca, 0600) != nil {
		t.Fatal("fixture CA")
	}
	dir := filepath.Join(root, "empty-roots")
	if os.Mkdir(dir, 0700) != nil {
		t.Fatal("fixture roots directory")
	}
	mode := "health"
	if inventoryFile != "" {
		mode = "assets"
	}
	c := exec.Command(os.Args[0], "-test.run=^TestTLSChild$")
	// Unit child override is deliberate, NEW generated trust only. This does NOT
	// qualify the future container default path; actual CLI refuses these env vars.
	c.Env = []string{"PATH=/usr/bin:/bin", "PROFILE_PROBE_TEST_CHILD=1", "PROFILE_PROBE_TEST_MODE=" + mode, "PROFILE_PROBE_TEST_PORT=" + strconv.Itoa(port), "PROFILE_PROBE_TEST_WRONG=" + strconv.FormatBool(wrong), "PROFILE_PROBE_TEST_INVENTORY=" + inventoryFile, "SSL_CERT_FILE=" + cert, "SSL_CERT_DIR=" + dir, "GORACE=atexit_sleep_ms=0"}
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatal("native child failed before outcome")
	}
	var result struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(b, &result) != nil {
		t.Fatal("native child malformed outcome")
	}
	return result.Kind
}
func TestTLSChild(t *testing.T) {
	if os.Getenv("PROFILE_PROBE_TEST_CHILD") != "1" {
		return
	}
	p, e := strconv.Atoi(os.Getenv("PROFILE_PROBE_TEST_PORT"))
	if e != nil {
		os.Exit(2)
	}
	kind := "invalid-input"
	if os.Getenv("PROFILE_PROBE_TEST_MODE") == "health" {
		kind = health(p, "generated-probe-response", os.Getenv("PROFILE_PROBE_TEST_WRONG") == "true")
	} else if os.Getenv("PROFILE_PROBE_TEST_MODE") == "assets" {
		b, e := readLimit(os.Getenv("PROFILE_PROBE_TEST_INVENTORY"), 65536)
		if e == nil {
			kind = assets(p, b)
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"kind": kind})
	os.Exit(0)
}
func startServer(t *testing.T, h http.Handler) (*httptest.Server, int, []byte) {
	t.Helper()
	cert, ca := fixtureCert(t)
	s := httptest.NewUnstartedServer(h)
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	s.StartTLS()
	t.Cleanup(s.Close)
	_, p, e := net.SplitHostPort(strings.TrimPrefix(s.URL, "https://"))
	if e != nil {
		t.Fatal("fixture port")
	}
	port, e := strconv.Atoi(p)
	if e != nil {
		t.Fatal("fixture port")
	}
	return s, port, ca
}
func TestTypedTLSOutcomesAndPositiveControls(t *testing.T) {
	var redirect atomic.Bool
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		_, _ = io.WriteString(w, "must not follow")
	}))
	defer other.Close()
	s, p, ca := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, other.URL, 302)
			return
		}
		_, _ = io.WriteString(w, "generated-probe-response")
	}))
	_, wrongCA := fixtureCert(t)
	for _, test := range []struct {
		roots []byte
		wrong bool
		want  string
	}{{ca, false, ok}, {wrongCA, false, "unknown-authority"}, {nil, false, "unknown-authority"}, {ca, true, "hostname"}, {ca, false, ok}} {
		if got := child(t, p, test.roots, test.wrong, ""); got != test.want {
			t.Fatalf("typed outcome mismatch: got %s want %s", got, test.want)
		}
	}
	redirect.Store(true)
	if got := child(t, p, ca, false, ""); got != "http-status" {
		t.Fatal("redirect was not refused")
	}
	if reached.Load() != 0 {
		t.Fatal("redirect target reached")
	}
	redirect.Store(false)
	if child(t, p, ca, false, "") != ok {
		t.Fatal("positive after redirect")
	}
	s.Close()
	if got := child(t, p, ca, false, ""); got != "transport" {
		t.Fatal("transport failure confused with TLS proof")
	}
}
func fixtureInventory() ([]byte, map[string][]byte) {
	files := map[string][]byte{"index.html": []byte("<html>generated UI</html>"), "bundle.js": []byte("// generated JS")}
	for n := 0; n < 20; n++ {
		files[fmt.Sprintf("asset%d.txt", n)] = []byte(fmt.Sprintf("generated-%d", n))
	}
	wanted := map[string]string{}
	for name, b := range files {
		wanted[name] = sum(b)
	}
	data, _ := json.Marshal(wanted)
	return data, files
}
func TestActualHTTPSAssetBytesFallbackAndMIME(t *testing.T) {
	data, files := fixtureInventory()
	var bad atomic.Bool
	var badMIME atomic.Bool
	_, p, ca := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/ui/")
		if name == "" || name == "disposable-generated-route" {
			name = "index.html"
		}
		b, known := files[name]
		if !known {
			http.NotFound(w, r)
			return
		}
		if name == "bundle.js" {
			if bad.Load() {
				b = files["index.html"]
			}
			if !badMIME.Load() {
				w.Header().Set("Content-Type", "application/javascript")
			}
		}
		_, _ = w.Write(b)
	}))
	inventoryFile := filepath.Join(t.TempDir(), "public-inventory.json")
	if os.WriteFile(inventoryFile, data, 0600) != nil {
		t.Fatal("fixture inventory")
	}
	if child(t, p, ca, false, inventoryFile) != ok {
		t.Fatal("asset positive")
	}
	bad.Store(true)
	if child(t, p, ca, false, inventoryFile) != "asset-mismatch" {
		t.Fatal("HTTP200 index fallback falsely accepted as JS")
	}
	bad.Store(false)
	badMIME.Store(true)
	if child(t, p, ca, false, inventoryFile) != "asset-mismatch" {
		t.Fatal("JS MIME mismatch accepted")
	}
	badMIME.Store(false)
	if child(t, p, ca, false, inventoryFile) != ok {
		t.Fatal("asset repeated positive")
	}
}
func TestInventoryRefusesMalformedDuplicateAndTraversal(t *testing.T) {
	valid, _ := fixtureInventory()
	if _, e := inventory(valid); e != nil {
		t.Fatal("valid fixture")
	}
	for _, b := range [][]byte{[]byte(`{"index.html":"x","index.html":"x"}`), []byte(`{"../secret":"x"}`), []byte(`{"bad":NaN}`), append(valid, []byte(` {}`)...), make([]byte, 65537)} {
		if _, e := inventory(b); e == nil {
			t.Fatal("invalid inventory accepted")
		}
	}
}
func TestProcParserNoHostReadAndCapabilityNegatives(t *testing.T) {
	good := "Uid:\t65531 65531 65531 65531\nGid:\t65531 65531 65531 65531\nGroups:\t\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\n"
	if status([]byte(good), 65531) != ok {
		t.Fatal("synthetic status positive")
	}
	for _, bad := range []string{strings.Replace(good, "65531", "0", 1), strings.Replace(good, "Groups:\t\n", "Groups:\t0\n", 1), strings.Replace(good, "CapEff:\t0000000000000000", "CapEff:\t0000000000000001", 1), strings.Replace(good, "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1), good + "Uid:\t65531 65531 65531 65531\n", strings.Replace(good, "CapBnd:\t0000000000000000\n", "", 1)} {
		if status([]byte(bad), 65531) != "identity-mismatch" {
			t.Fatal("unsafe status accepted")
		}
	}
}
func TestELFMetadataNegativesUseOnlySyntheticFilesystem(t *testing.T) {
	m := elfMetadata{interpreter: "/lib64/ld-linux-x86-64.so.2", needed: []string{"libc.so.6"}}
	if checkELF(m, func(string) (bool, error) { return true, nil }) != ok {
		t.Fatal("ELF structural positive")
	}
	if checkELF(m, func(string) (bool, error) { return false, nil }) != "missing-loader" {
		t.Fatal("missing loader")
	}
	if checkELF(m, func(p string) (bool, error) { return p == m.interpreter, nil }) != "missing-library" {
		t.Fatal("missing library")
	}
	if checkELF(m, func(string) (bool, error) { return false, os.ErrPermission }) != "filesystem" {
		t.Fatal("permission error misattributed")
	}
	badName := m
	badName.needed = []string{".."}
	if checkELF(badName, func(p string) (bool, error) {
		if p != m.interpreter {
			t.Fatal("library traversal reached filesystem")
		}
		return true, nil
	}) != "unsupported-elf" {
		t.Fatal("library traversal accepted")
	}
	m.rpath = true
	if checkELF(m, func(string) (bool, error) { t.Fatal("must reject before filesystem"); return false, nil }) != "unsupported-elf" {
		t.Fatal("unsupported RPATH")
	}
	if _, e := parseELF([]byte("not an ELF binary")); e == nil {
		t.Fatal("malformed ELF")
	}
}
func TestRuntimeEnvironmentOverridesRefused(t *testing.T) {
	if envPolicy(func(string) (string, bool) { return "", false }) != ok {
		t.Fatal("closed environment")
	}
	for _, key := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "http_proxy", "GODEBUG", "LD_PRELOAD"} {
		if envPolicy(func(name string) (string, bool) { return "", name == key }) != "environment-override" {
			t.Fatal("override accepted")
		}
	}
}
func TestLoopbackInputLimitsAndNoRawError(t *testing.T) {
	for _, p := range []int{-1, 0, 443, 65536} {
		if health(p, "generated", false) != "invalid-input" {
			t.Fatal("port accepted")
		}
	}
	if health(1024, "https://private.example.invalid", false) != "invalid-input" {
		t.Fatal("arbitrary content accepted")
	}
	if tlsKind(fmt.Errorf("do not expose raw private error")) != "transport" {
		t.Fatal("raw error classification")
	}
}

func TestGeneratedCanaryAndTypedPermissionNotMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "value")
	body := []byte("generated-canary")
	if os.WriteFile(p, body, 0600) != nil {
		t.Fatal("fixture canary")
	}
	if canaryOpen(string(body), func() (*os.File, error) { return os.OpenFile(p, os.O_RDWR, 0) }) != ok {
		t.Fatal("generated canary read-write")
	}
	after, e := os.ReadFile(p)
	if e != nil || string(after) != string(body) {
		t.Fatal("canary bytes changed")
	}
	if canaryOpen(string(body), func() (*os.File, error) { return nil, os.ErrPermission }) != "permission-denied" {
		t.Fatal("permission attribution")
	}
	if canaryOpen(string(body), func() (*os.File, error) { return nil, os.ErrNotExist }) != "filesystem" {
		t.Fatal("missing path masqueraded as denial")
	}
	if network("not-a-network-namespace") != "invalid-input" {
		t.Fatal("invalid namespace accepted")
	}
}
