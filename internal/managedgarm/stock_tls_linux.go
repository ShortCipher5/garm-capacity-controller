package managedgarm

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"os"
	"strings"
	"syscall"
)

func stockTrust(raw []byte, pin string) (*tls.Config, error) {
	expected, e := hex.DecodeString(pin)
	if e != nil || len(expected) != 32 || hex.EncodeToString(expected) != pin {
		return nil, ErrBoundary
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, ErrBoundary
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, ErrBoundary
	}
	hash := sha256.Sum256(cert.Raw)
	if subtle.ConstantTimeCompare(hash[:], expected) != 1 {
		return nil, ErrBoundary
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1", VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return ErrBoundary
		}
		hash := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(hash[:], expected) != 1 {
			return ErrBoundary
		}
		return nil
	}}, nil
}
func readStockTrust(spec Specification) (*tls.Config, error) {
	files, e := openBootstrapFiles(spec.AdminSecretDirectory, 0)
	if e != nil {
		return nil, ErrBoundary
	}
	defer files.root.Close()
	f, e := files.root.OpenFile("api-cert.pem", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrBoundary
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !files.private(info, false) {
		return nil, ErrBoundary
	}
	raw, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil || len(raw) > 16384 {
		return nil, ErrBoundary
	}
	return stockTrust(raw, spec.GarmServerFingerprint)
}
