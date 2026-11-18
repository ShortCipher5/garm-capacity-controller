package securefiles

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrServerPin = errors.New("server trust pin unavailable or rejected")

// WithServerKeyPin adds an explicit out-of-band SHA-256 SPKI expectation to
// normal certificate-chain and hostname validation. It never discovers or
// silently accepts a key, writes credentials, or mutates the caller's config.
// Install it before constructing the transport; changing pins requires a new
// transport and explicit operator review. It is not physical host attestation.
func WithServerKeyPin(config *tls.Config, fingerprint string) (*tls.Config, error) {
	expected, err := hex.DecodeString(fingerprint)
	if err != nil || len(expected) != sha256.Size || fingerprint != strings.ToLower(fingerprint) || config == nil || config.InsecureSkipVerify || config.RootCAs == nil {
		return nil, ErrServerPin
	}
	pinned := config.Clone()
	pinned.RootCAs = config.RootCAs.Clone()
	previous := config.VerifyConnection
	pinned.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return ErrServerPin
		}
		if previous != nil {
			if err := previous(state); err != nil {
				return ErrServerPin
			}
		}
		actual := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
			return ErrServerPin
		}
		return nil
	}
	return pinned, nil
}
