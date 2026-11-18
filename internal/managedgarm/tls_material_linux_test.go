package managedgarm

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"testing"
	"time"
)

func TestPreparedStockTLSIdentity(t *testing.T) {
	now := time.Now()
	cert, key, pin, err := stockTLSMaterial(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tls.X509KeyPair(cert, key); err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(cert)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(c)
	if _, err = c.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", CurrentTime: now}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(c.Raw)
	if pin != hex.EncodeToString(sum[:]) {
		t.Fatal("pin mismatch")
	}
	if _, err = c.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.2", CurrentTime: now}); err == nil {
		t.Fatal("wrong peer accepted")
	}
	cert2, _, pin2, err := stockTLSMaterial(now)
	if err != nil || string(cert2) == string(cert) || pin2 == pin {
		t.Fatal("identity reused")
	}
}
