package securefiles

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestServerPinPreservesVerifiedTrustAndDoesNotMutateConfig(t *testing.T) {
	leaf := &x509.Certificate{RawSubjectPublicKeyInfo: []byte("disposable public fixture key")}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	fingerprint := fmt.Sprintf("%x", sum)
	calls := 0
	source := &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS13, VerifyConnection: func(tls.ConnectionState) error { calls++; return nil }}
	pinned, err := WithServerKeyPin(source, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	if err = pinned.VerifyConnection(state); err != nil || calls != 1 {
		t.Fatal("verified matching key rejected", err, calls)
	}
	if pinned == source || pinned.RootCAs == source.RootCAs || pinned.MinVersion != tls.VersionTLS13 || pinned.InsecureSkipVerify {
		t.Fatal("config changed or trust shared")
	}
	for _, invalid := range []tls.ConnectionState{{}, {PeerCertificates: []*x509.Certificate{leaf}}, {VerifiedChains: state.VerifiedChains}, {PeerCertificates: []*x509.Certificate{{RawSubjectPublicKeyInfo: []byte("different CA-trusted key")}}, VerifiedChains: state.VerifiedChains}} {
		if !errors.Is(pinned.VerifyConnection(invalid), ErrServerPin) {
			t.Fatal("invalid peer accepted")
		}
	}
	source.VerifyConnection = func(tls.ConnectionState) error { return errors.New("private callback detail") }
	rejected, err := WithServerKeyPin(source, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err = rejected.VerifyConnection(state); err != ErrServerPin {
		t.Fatal("prior callback bypassed or leaked detail", err)
	}
}
func TestServerPinRejectsInvalidOrUnverifiedConfiguration(t *testing.T) {
	config := &tls.Config{RootCAs: x509.NewCertPool()}
	for _, pin := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("z", 64), "sha256:" + strings.Repeat("a", 64), strings.Repeat("a", 64) + "\n"} {
		if _, err := WithServerKeyPin(config, pin); err == nil {
			t.Fatal("invalid pin accepted")
		}
	}
	for _, c := range []*tls.Config{nil, {}, {RootCAs: x509.NewCertPool(), InsecureSkipVerify: true}} {
		if _, err := WithServerKeyPin(c, strings.Repeat("a", 64)); err == nil {
			t.Fatal("unverified trust accepted")
		}
	}
}
