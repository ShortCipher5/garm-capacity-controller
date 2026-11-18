package testsupport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NativeTLS is a disposable loopback CA with separate controller and owner client
// leaves. It is used only by tests; application executables do not import it.
func NativeTLS(t testing.TB, handler http.Handler) (*httptest.Server, *tls.Config, *tls.Config) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-native-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, e := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	ca, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	mint := func(id int64, client bool) tls.Certificate {
		p, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		usage := x509.ExtKeyUsageServerAuth
		if client {
			usage = x509.ExtKeyUsageClientAuth
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(id), Subject: pkix.Name{CommonName: "disposable-native-leaf"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, e := x509.CreateCertificate(rand.Reader, c, ca, &p.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: p}
	}
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{mint(2, false)}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	controller := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{mint(3, true)}, MinVersion: tls.VersionTLS13}
	owner := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{mint(4, true)}, MinVersion: tls.VersionTLS13}
	return server, controller, owner
}
