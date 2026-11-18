package managedsource

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActualResponseIdentityAndDrift(t *testing.T) {
	controller := "11111111-1111-1111-1111-111111111111"
	source := strings.Repeat("a", 64)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential forwarded")
		}
		w.Header().Set(SourceHeader, source)
		w.Header().Set(ControllerHeader, controller)
		w.Write([]byte("[]"))
	}))
	s.StartTLS()
	defer s.Close()
	cert := s.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	c, e := New(s.URL, hex.EncodeToString(hash[:]), controller, &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{s.TLS.Certificates[0]}})
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseIdleConnections()
	if c.Identity() != "" {
		t.Fatal("configuration minted provenance")
	}
	client := &http.Client{Transport: c}
	read := func() error {
		r, _ := http.NewRequest("GET", s.URL+"/api/v1/pools", nil)
		r.Header.Set("Authorization", "Bearer secret")
		res, e := client.Do(r)
		if e == nil {
			res.Body.Close()
		}
		return e
	}
	if e = read(); e != nil || c.Identity() == "" {
		t.Fatalf("valid read: %v", e)
	}
	source = strings.Repeat("b", 64)
	if read() == nil || c.Identity() != "" {
		t.Fatal("drift accepted")
	}
	source = strings.Repeat("a", 64)
	if read() == nil {
		t.Fatal("revocation did not latch")
	}
}

func TestMutualTLSRejectsMissingAndUntrustedClient(t *testing.T) {
	clientCert, clientRoot := clientIdentity(t)
	unrelated, _ := clientIdentity(t)
	controller := "11111111-1111-1111-1111-111111111111"
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unverified client reached handler")
		}
		w.Header().Set(SourceHeader, strings.Repeat("a", 64))
		w.Header().Set(ControllerHeader, controller)
		w.Write([]byte("[]"))
	}))
	roots := x509.NewCertPool()
	roots.AddCert(clientRoot)
	s.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS13}
	s.StartTLS()
	defer s.Close()
	serverRoots := x509.NewCertPool()
	serverRoots.AddCert(s.Certificate())
	sum := sha256.Sum256(s.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name string
		cert *tls.Certificate
		want bool
	}{{"valid", &clientCert, true}, {"wrong", &unrelated, false}, {"missing", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			trust := &tls.Config{RootCAs: serverRoots}
			if tc.cert != nil {
				trust.Certificates = []tls.Certificate{*tc.cert}
			}
			c, e := New(s.URL, pin, controller, trust)
			if tc.cert == nil {
				if e == nil {
					t.Fatal("missing credential accepted by constructor")
				}
				// Also exercise the actual TLS server rejection, independently of constructor.
				h := &http.Client{Transport: &http.Transport{TLSClientConfig: trust}}
				if res, e := h.Get(s.URL); e == nil {
					res.Body.Close()
					t.Fatal("server accepted missing client")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			defer c.CloseIdleConnections()
			h := &http.Client{Transport: c}
			res, e := h.Get(s.URL + "/api/v1/pools")
			if res != nil {
				res.Body.Close()
			}
			if (e == nil) != tc.want {
				t.Fatalf("success %v want %v: %v", e == nil, tc.want, e)
			}
			if !tc.want && c.Identity() != "" {
				t.Fatal("untrusted client minted identity")
			}
		})
	}
}
func clientIdentity(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, cert
}
