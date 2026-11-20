//go:build linux && stock_read_pilot_harness

package managedgarm

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Validate the closed prelaunch snapshot in memory before a disposable
// consumer uses any of its bytes. The manifest pin is supplied independently;
// this does not approve it, observe a topology, or parse/execute the overlay.
func checkStockPilotPreparedBytes(manifest []byte, expected string, files map[string][]byte, now time.Time) error {
	names := stockPilotPreparedNames()
	if len(manifest) == 0 || len(manifest) > 16384 || len(files) != len(names) || !digest.MatchString(expected) || stockBindingDigest(manifest) != expected {
		return ErrBoundary
	}
	for _, name := range names {
		if n := len(files[name]); n == 0 || n > 16384 {
			return ErrBoundary
		}
	}
	prepared, err := decodeStockPilotPrepared(manifest, files["prelaunch.json"], now)
	if err != nil {
		return ErrBoundary
	}
	for _, name := range names {
		if stockBindingDigest(files[name]) != prepared.Files[name] {
			return ErrBoundary
		}
	}
	user, err := normalizeStockPilotFixtureCredential(files["bootstrap-user.json"])
	if err != nil {
		return ErrBoundary
	}
	defer clear(user)
	if stockBindingDigest(user) != prepared.Review.CredentialDigest {
		return ErrBoundary
	}
	seen := map[string]bool{}
	for _, name := range []string{"stock-api.der", "gateway-api.der", "ingress-api.der"} {
		c, e := x509.ParseCertificate(files[name])
		if e != nil {
			return ErrBoundary
		}
		pin := stockBindingDigest(c.RawSubjectPublicKeyInfo)
		if seen[pin] {
			return ErrBoundary
		}
		seen[pin] = true
	}
	pair, e := tls.X509KeyPair(files["read-serve-server.pem"], files["read-serve-server.key"])
	if e != nil || len(pair.Certificate) != 1 {
		return ErrBoundary
	}
	server, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil || seen[prepared.ServerSPKI] || stockBindingDigest(server.RawSubjectPublicKeyInfo) != prepared.ServerSPKI {
		return ErrBoundary
	}
	launch := files["stock-launch.json"]
	var proposed stockPilotLaunchDescriptor
	if !stockBindingJSON(launch, &proposed) || !reflect.DeepEqual(proposed, stockPilotFixtureStockLaunch()) {
		return ErrBoundary
	}
	return nil
}

func TestStockPilotPreparedBytesRequireExactCommitments(t *testing.T) {
	reviewRaw, manifestRaw, now := pilotStageJSONFixture(t)
	var m stockPilotPreparedManifest
	if json.Unmarshal(manifestRaw, &m) != nil {
		t.Fatal("fixture manifest")
	}
	files := map[string][]byte{}
	for _, name := range stockPilotPreparedNames() {
		files[name] = []byte("synthetic opaque " + name)
	}
	files["prelaunch.json"] = reviewRaw
	_, boot := stockAuthFixture(t)
	_, _, gateway := serveMaterialFixture(t)
	_, _, ingress := serveMaterialFixture(t)
	roles, e := prepareStockPilotTLSRoles(time.Now(), boot[1], gateway, ingress)
	if e != nil {
		t.Fatal("synthetic TLS material unavailable")
	}
	defer roles.clear()
	files["stock-api.der"], files["gateway-api.der"], files["ingress-api.der"] = boot[1], gateway, ingress
	files["read-serve-server.pem"], files["read-serve-server.key"] = roles.serverPEM, roles.serverKey
	server, e := x509.ParseCertificate(roles.serverDER)
	if e != nil {
		t.Fatal("synthetic TLS leaf unavailable")
	}
	m.ServerSPKI = stockBindingDigest(server.RawSubjectPublicKeyInfo)
	// Raw file identity differs from the canonical semantic credential hash.
	files["bootstrap-user.json"] = append([]byte(" \n"), boot[4]...)
	files["stock-launch.json"], _ = json.Marshal(stockPilotFixtureStockLaunch())
	m.Review.CredentialDigest = stockBindingDigest(boot[4])
	m.Review.StockLeafSHA256 = stockBindingDigest(files["stock-api.der"])
	m.Review.GatewayLeafSHA256 = stockBindingDigest(files["gateway-api.der"])
	m.Review.IngressLeafSHA256 = stockBindingDigest(files["ingress-api.der"])
	seal := func() ([]byte, string) {
		files["prelaunch.json"], _ = json.Marshal(m.Review)
		m.ReviewSHA256 = stockBindingDigest(files["prelaunch.json"])
		for name, b := range files {
			m.Files[name] = stockBindingDigest(b)
		}
		b, _ := json.Marshal(m)
		return b, stockBindingDigest(b)
	}
	raw, pin := seal()
	if checkStockPilotPreparedBytes(raw, pin, files, now) != nil {
		t.Fatal("matched snapshot refused")
	}
	if checkStockPilotPreparedBytes(raw, stockBindingDigest([]byte("different")), files, now) == nil {
		t.Fatal("unreviewed manifest passed")
	}
	for _, name := range stockPilotPreparedNames() {
		keep := files[name]
		files[name] = []byte("tampered")
		if checkStockPilotPreparedBytes(raw, pin, files, now) == nil {
			t.Fatal("unbound bytes accepted", name)
		}
		files[name] = keep
	}
	files["extra"] = []byte("extra")
	if checkStockPilotPreparedBytes(raw, pin, files, now) == nil {
		t.Fatal("extra file accepted")
	}
	delete(files, "extra")
	pinOriginal := m.ServerSPKI
	m.ServerSPKI = stockBindingDigest([]byte("wrong"))
	badRaw, badPin := seal()
	if checkStockPilotPreparedBytes(badRaw, badPin, files, now) == nil {
		t.Fatal("wrong server pin passed")
	}
	m.ServerSPKI = pinOriginal
	m.Review.CredentialDigest = stockBindingDigest(files["bootstrap-user.json"])
	raw, pin = seal()
	if checkStockPilotPreparedBytes(raw, pin, files, now) == nil {
		t.Fatal("raw credential hash substituted for canonical commitment")
	}
	m.Review.CredentialDigest = stockBindingDigest(boot[4])
	changed := stockPilotFixtureStockLaunch()
	changed.Network = "host"
	files["stock-launch.json"], _ = json.Marshal(changed)
	raw, pin = seal()
	if checkStockPilotPreparedBytes(raw, pin, files, now) == nil {
		t.Fatal("changed launch passed with recomputed hashes")
	}
}
