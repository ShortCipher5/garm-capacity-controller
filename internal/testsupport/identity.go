// Package testsupport supplies disposable loopback credentials for tests only.
// No application executable imports this package or contains these generated keys.
package testsupport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
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
	"path/filepath"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type IdentityFixture struct {
	Client        *incusapi.Client
	Configuration string
}
type identityConfiguration struct {
	Endpoint string               `json:"endpoint"`
	TLS      securefiles.TLSFiles `json:"tls"`
}

func Identity(t testing.TB, serverName string) IdentityFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-identity-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pair := func(serial int64, client bool) (tls.Certificate, []byte) {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "disposable-identity-leaf"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if client {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, parsed, &private.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		privateDER, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: private}, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	}
	serverPair, _ := pair(2, false)
	clientPair, clientKey := pair(3, true)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	hash := sha256.Sum256(serverPair.Certificate[0])
	root := map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": serverName, "server_version": "6.0.4", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverPair.Certificate[0]})), "certificate_fingerprint": fmt.Sprintf("%x", hash)}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status_code": 200, "metadata": root})
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cfg := identityConfiguration{Endpoint: server.URL, TLS: securefiles.TLSFiles{CA: write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), Certificate: write("client.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientPair.Certificate[0]})), Key: write("client.key", clientKey)}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := write("fixture.json", raw)
	return IdentityFixture{Client: LoadIdentityClient(t, path), Configuration: path}
}
func LoadIdentityClient(t testing.TB, path string) *incusapi.Client {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg identityConfiguration
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	credentials, err := securefiles.LoadTLS(cfg.TLS)
	if err != nil {
		t.Fatal(err)
	}
	client, err := incusapi.New(cfg.Endpoint, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
