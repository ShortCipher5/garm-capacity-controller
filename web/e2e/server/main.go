// Disposable localhost-only browser test fixture. Never contacts infrastructure.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"log"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// testCertificate creates a fresh, in-memory key for this disposable server.
// Only its public-key fingerprint is written for the test browser allowlist.
func testCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "disposable-admin-browser-test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	hash := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, base64.StdEncoding.EncodeToString(hash[:]), nil
}

func main() {
	dir, err := os.MkdirTemp("", "admin-browser-test-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	now := time.Now()
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: now}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Hour}
	store, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), config)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	fence, err := store.BeginReconciliation(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	now = time.Now()
	err = store.CompleteReconciliation(context.Background(), fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host-a", ObservedAt: now, Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}, Resources: []admission.InventoryResource{{ID: "test-only-instance", Host: "host-a", Pool: "pool-a", Resources: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, State: "unknown"}}})
	if err != nil {
		log.Fatal(err)
	}
	root, err := os.OpenRoot("dist")
	if err != nil {
		log.Fatal(err)
	}
	defer root.Close()
	handler, err := adminview.New(store, strings.Repeat("a", 64), root, false)
	if err != nil {
		log.Fatal(err)
	}
	if err := handler.EnableManagement(strings.Repeat("c", 64)); err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:4173")
	if err != nil {
		log.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	certificate, spki, err := testCertificate()
	if err != nil {
		log.Fatal(err)
	}
	// Worker fixtures read this only after Playwright's readiness probe succeeds.
	if err := os.WriteFile(".admin-test-spki", []byte(spki+"\n"), 0600); err != nil {
		log.Fatal(err)
	}
	defer os.Remove(".admin-test-spki")
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	log.Print("Disposable browser fixture ready on https://127.0.0.1:4173")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()
}
