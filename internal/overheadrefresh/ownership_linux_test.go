package overheadrefresh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestOwnedCollectionTLSFreezesPrivateMaterial(t *testing.T) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	input := &tls.Config{RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{PrivateKey: key, Certificate: [][]byte{{1, 2, 3}}, OCSPStaple: []byte{4}, SignedCertificateTimestamps: [][]byte{{5}}}}}
	owned, e := ownedTLS(input)
	if e != nil {
		t.Fatal(e)
	}
	before := append([]byte(nil), owned.Certificates[0].PrivateKey.(ed25519.PrivateKey)...)
	key[0] ^= 1
	input.Certificates[0].Certificate[0][0] = 9
	if owned.Certificates[0].PrivateKey.(ed25519.PrivateKey)[0] != before[0] || owned.Certificates[0].Certificate[0][0] != 1 || owned.Certificates[0].OCSPStaple != nil || owned.Certificates[0].SignedCertificateTimestamps != nil {
		t.Fatal("caller retained TLS ownership")
	}
	input.VerifyConnection = func(tls.ConnectionState) error { return nil }
	if _, e = ownedTLS(input); e == nil {
		t.Fatal("caller TLS callback accepted")
	}
}
