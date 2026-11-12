package incusapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func physicalRoot(server *httptest.Server) map[string]any {
	root := rootMetadata().(map[string]any)
	leaf := server.Certificate()
	sum := sha256.Sum256(leaf.Raw)
	root["environment"] = map[string]any{"server_clustered": false, "server_name": "fixture-daemon", "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})), "certificate_fingerprint": fmt.Sprintf("%x", sum)}
	return root
}

func TestPhysicalIdentityIsActualTLSBoundAndProjectIndependent(t *testing.T) {
	var roots atomic.Int32
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/1.0" {
			t.Error("identity made nonroot/write request")
			http.NotFound(w, r)
			return
		}
		roots.Add(1)
		writeSync(w, physicalRoot(server))
	}))
	a, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(server.URL, "another-project", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	first, err := a.PhysicalIdentity(context.Background())
	if err != nil || !first.Valid() {
		t.Fatalf("identity unavailable: %+v %v", first, err)
	}
	second, err := b.PhysicalIdentity(context.Background())
	if err != nil || first.ServerKeyFingerprint() != second.ServerKeyFingerprint() || first.OriginFingerprint() != second.OriginFingerprint() || first.ServerName() != second.ServerName() || first.ScopeFingerprint() == second.ScopeFingerprint() {
		t.Fatalf("project split manufactured a host: %v", err)
	}
	actual := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	canonical, err := a.canonicalOrigin()
	if err != nil {
		t.Fatal(err)
	}
	expectedScope := sha256.Sum256([]byte(canonical + "\x00runners"))
	if first.ScopeFingerprint() != fmt.Sprintf("%x", expectedScope) {
		t.Fatal("scope does not bind exact canonical authority and project")
	}
	if first.ServerKeyFingerprint() != fmt.Sprintf("%x", actual) || !fingerprint(first.OriginFingerprint()) || !fingerprint(first.ScopeFingerprint()) || first.ServerName() != "fixture-daemon" || first.ServerVersion() != "6.0.4" || roots.Load() != 2 {
		t.Fatal("identity not bound to fresh actual TLS root")
	}
	encoded, _ := json.Marshal(first)
	if string(encoded) != "{}" {
		t.Fatal("opaque identity accidentally exposed raw evidence")
	}
	var forged PhysicalServerIdentity
	if err := json.Unmarshal([]byte(`{"verified":true,"key":"`+strings.Repeat("a", 64)+`","serverName":"fake","origin":"`+strings.Repeat("b", 64)+`"}`), &forged); err != nil || forged.Valid() {
		t.Fatal("operator JSON manufactured authenticated identity")
	}
}

func TestPhysicalIdentityRejectsUnknownTopologyAndCertificateBinding(t *testing.T) {
	for _, mode := range []string{"untrusted", "clustered", "missing-name", "null-name", "blank-name", "bad-name", "missing-cert", "null-cert", "malformed-cert", "private-key", "trailing-cert", "leading-cert", "wrong-cert", "missing-fingerprint", "wrong-fingerprint", "null-version"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/1.0" || r.Method != http.MethodGet {
					t.Error("unsafe identity route")
					http.NotFound(w, r)
					return
				}
				root := physicalRoot(server)
				env := root["environment"].(map[string]any)
				switch mode {
				case "untrusted":
					root["auth"] = "untrusted"
				case "clustered":
					env["server_clustered"] = true
				case "missing-name":
					delete(env, "server_name")
				case "null-name":
					env["server_name"] = nil
				case "blank-name":
					env["server_name"] = " "
				case "bad-name":
					env["server_name"] = "fixture\nsecret-must-not-echo"
				case "missing-cert":
					delete(env, "certificate")
				case "null-cert":
					env["certificate"] = nil
				case "malformed-cert":
					env["certificate"] = "secret-must-not-echo"
				case "private-key":
					env["certificate"] = "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
				case "leading-cert":
					env["certificate"] = "secret-must-not-echo" + env["certificate"].(string)
				case "trailing-cert":
					env["certificate"] = env["certificate"].(string) + "secret-must-not-echo"
				case "wrong-cert":
					raw := server.TLS.ClientCAs.Subjects()
					_ = raw
					env["certificate"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.TLS.Certificates[0].Certificate[1]}))
					sum := sha256.Sum256(server.TLS.Certificates[0].Certificate[1])
					env["certificate_fingerprint"] = fmt.Sprintf("%x", sum)
				case "missing-fingerprint":
					delete(env, "certificate_fingerprint")
				case "wrong-fingerprint":
					env["certificate_fingerprint"] = strings.Repeat("f", 64)
				case "null-version":
					env["server_version"] = nil
				}
				writeSync(w, root)
			}))
			c, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			identity, err := c.PhysicalIdentity(context.Background())
			if err == nil || identity.Valid() {
				t.Fatal("unqualified identity accepted")
			}
			want := ErrResponse
			if mode == "untrusted" || mode == "clustered" {
				want = ErrUnsupported
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "secret-must-not-echo") {
				t.Fatalf("unsafe identity error: %v", err)
			}
		})
	}
}

func TestPhysicalOriginNormalizationAndUnknownAuthority(t *testing.T) {
	for _, tc := range []struct{ a, b string }{{"https://EXAMPLE.invalid", "https://example.invalid:443"}, {"https://example.invalid.:0443", "https://example.invalid:443"}, {"https://[2001:0db8::1]", "https://[2001:db8::1]:443"}} {
		a, _ := url.Parse(tc.a)
		b, _ := url.Parse(tc.b)
		ca := &Client{base: *a}
		cb := &Client{base: *b}
		x, err := ca.canonicalOrigin()
		if err != nil {
			t.Fatal(err)
		}
		y, err := cb.canonicalOrigin()
		if err != nil || x != y {
			t.Fatalf("equivalent origins differ: %q %q %v", x, y, err)
		}
	}
	for _, address := range []string{"https://unicode-\u00e9.invalid", "https://[fe80::1%25eth0]", "https://example.invalid:99999"} {
		u, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&Client{base: *u}).canonicalOrigin(); err == nil {
			t.Fatal("ambiguous authority accepted")
		}
	}
}

func TestPhysicalIdentityIsFreshNotANameOrKeyLifetimeLease(t *testing.T) {
	var changed atomic.Bool
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		root := physicalRoot(server)
		if changed.Load() {
			root["environment"].(map[string]any)["server_name"] = "changed-daemon"
		}
		writeSync(w, root)
	}))
	c, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, err := c.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed.Store(true)
	second, err := c.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.ServerName() == second.ServerName() || first.ServerKeyFingerprint() != second.ServerKeyFingerprint() {
		t.Fatal("identity was cached or server-name change manufactured another key")
	}
}

func TestPhysicalIdentitySharedTLSKeyCannotBeSeparatedByAuthorityProjectOrReportedName(t *testing.T) {
	var firstServer *httptest.Server
	firstServer, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeSync(w, physicalRoot(firstServer)) }))
	var secondServer *httptest.Server
	secondServer = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		root := physicalRoot(secondServer)
		env := root["environment"].(map[string]any)
		env["server_name"] = "different-reported-name"
		env["server_version"] = "6.0.5"
		writeSync(w, root)
	}))
	secondServer.TLS = firstServer.TLS.Clone()
	secondServer.StartTLS()
	defer secondServer.Close()
	a, err := New(firstServer.URL, "project-a", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := New(secondServer.URL, "project-b", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	x, err := a.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.PhysicalIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !x.Valid() || !y.Valid() || x.ServerKeyFingerprint() != y.ServerKeyFingerprint() {
		t.Fatal("shared actual TLS key split by metadata/authority")
	}
	if x.OriginFingerprint() == y.OriginFingerprint() || x == y {
		t.Fatal("fixture did not distinguish authorities/metadata")
	}
	// Deduplication must compare key OR origin, never only whole struct equality.
}
