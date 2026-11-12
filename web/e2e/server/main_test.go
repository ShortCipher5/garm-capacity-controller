package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net"
	"testing"
)

func TestDisposableCertificateFingerprintAndFreshKey(t *testing.T) {
	first, fingerprint, err := testCertificate()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if fingerprint != base64.StdEncoding.EncodeToString(hash[:]) {
		t.Fatal("fingerprint does not match server key")
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatal("fixture certificate escaped loopback scope")
	}
	_, second, err := testCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == second {
		t.Fatal("fixture reused server key")
	}
}
