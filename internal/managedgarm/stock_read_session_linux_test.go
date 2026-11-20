package managedgarm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStockReadSessionNoRuntimeAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if s, err := newStockReadSession(c); err == nil || s != nil {
			t.Fatal("invalid context accepted")
		}
	}
	s := &stockReadSession{}
	for _, request := range []struct{ method, path string }{
		{"POST", "/api/v1/first-run"}, {"PUT", "/api/v1/pools/11111111-1111-4111-8111-111111111111"}, {"GET", "/api/v1/instances"}, {"DELETE", "/api/v1/jobs"},
	} {
		if _, err := s.authenticate(ctx, request.method, request.path, "", nil); err == nil {
			t.Fatal("unauthorized constructor route")
		}
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/controller", "/api/v1/pools?all=true", "/api/v1/pools/../jobs", "https://example.invalid", "/api/v1/instances/1", "/api/v1/repositories"} {
		if stockReadDataPath(path) {
			t.Fatal("unexpected data path", path)
		}
	}
	for _, path := range []string{"/api/v1/instances", "/api/v1/jobs", "/api/v1/pools", "/api/v1/pools/11111111-1111-4111-8111-111111111111"} {
		if !stockReadDataPath(path) {
			t.Fatal("fixed data path refused")
		}
	}
	s = &stockReadSession{ready: true, token: "synthetic", identity: "synthetic"}
	if _, err := s.Instances(ctx); err == nil {
		t.Fatal("unowned session accepted")
	}
	if s.token != "" || s.Identity() != "" {
		t.Fatal("failed session retained token")
	}
	s.Close()
}
func TestStockReadTLSNormalVerificationAndExactLeaf(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		expired, wrongName, wrongPin bool
	}{
		{name: "valid"}, {name: "expired", expired: true}, {name: "wrong-name", wrongName: true}, {name: "wrong-pin", wrongPin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ip := net.ParseIP("127.0.0.1")
			if tc.wrongName {
				ip = net.ParseIP("127.0.0.2")
			}
			until := time.Now().Add(time.Hour)
			if tc.expired {
				until = time.Now().Add(-time.Minute)
			}
			cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: until, IPAddresses: []net.IP{ip}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
			if err != nil {
				t.Fatal(err)
			}
			pin := stockBindingDigest(der)
			trust, err := stockReadTLS(der, pin)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
			server.StartTLS()
			defer server.Close()
			if tc.wrongPin {
				// A different server key/leaf with the same trusted root pool cannot pass
				// the owned exact DER check. This is a direct TLS verifier unit negative.
				state := tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}, PeerCertificates: []*x509.Certificate{{Raw: []byte("other")}}}
				if trust.VerifyConnection(state) == nil {
					t.Fatal("different live leaf accepted")
				}
				if _, err := stockReadTLS(der, strings.Repeat("a", 64)); err == nil {
					t.Fatal("unreviewed trust accepted")
				}
				return
			}
			transport := &http.Transport{TLSClientConfig: trust, Proxy: nil}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			response, err := client.Get(server.URL)
			if tc.expired || tc.wrongName {
				if err == nil {
					response.Body.Close()
					t.Fatal("normal validation bypassed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

func TestStockReadFixedDialRefusesUnownedIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, b := range []*retainedStockRead{nil, {}} {
		for _, c := range []context.Context{nil, ctx, context.Background()} {
			if conn, err := b.dialStockAPI(c); err == nil || conn != nil {
				t.Fatal("unowned fixed dial accepted")
			}
		}
	}
}
