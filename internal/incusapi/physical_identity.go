package incusapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// PhysicalServerIdentity is an authenticated observation, not a hardware identity,
// lifetime lease or permission to allocate. It is comparable but opaque: external
// callers cannot manufacture a nonzero value from operator JSON. Caller policy
// must reject repeated key/origin fingerprints across budget aliases and reject
// identity changes pending requalification. A restart needs durable binding.
// Raw addresses, certificates and credentials are deliberately not exposed.
type PhysicalServerIdentity struct {
	origin        string
	scope         string
	key           string
	serverName    string
	serverVersion string
	verified      bool
}

func (p PhysicalServerIdentity) Valid() bool {
	return p.verified && fingerprint(p.origin) && fingerprint(p.scope) && fingerprint(p.key) && label(p.serverName) && strings.TrimSpace(p.serverName) == p.serverName
}
func (p PhysicalServerIdentity) ServerKeyFingerprint() string {
	if !p.Valid() {
		return ""
	}
	return p.key
}
func (p PhysicalServerIdentity) OriginFingerprint() string {
	if !p.Valid() {
		return ""
	}
	return p.origin
}

// ScopeFingerprint binds exact configured project without creating a new host budget.
func (p PhysicalServerIdentity) ScopeFingerprint() string {
	if !p.Valid() {
		return ""
	}
	return p.scope
}
func (p PhysicalServerIdentity) ServerName() string {
	if !p.Valid() {
		return ""
	}
	return p.serverName
}
func (p PhysicalServerIdentity) ServerVersion() string {
	if !p.Valid() {
		return ""
	}
	return p.serverVersion
}

// canonicalOrigin excludes project: two projects on one authority are NOT two
// independent physical budgets. Equivalent host case, DNS trailing dot, default
// HTTPS port and IP spellings normalize alike. Ambiguous Unicode/zone authorities
// are unsupported instead of guessed; same-daemon DNS aliases also share a key.
func (c *Client) canonicalOrigin() (string, error) {
	host := strings.ToLower(strings.TrimSuffix(c.base.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		if host == "" {
			return "", ErrConfiguration
		}
		for _, r := range host {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.') {
				return "", ErrUnsupported
			}
		}
	}
	port := c.base.Port()
	if port == "" {
		port = "443"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", ErrConfiguration
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// PhysicalIdentity performs a fresh verified root read. It requires trusted API
// v1.0, explicit standalone topology and bounded server_name. Incus v6.0.4 exposes
// environment.certificate and certificate_fingerprint; their public certificate
// must match the ACTUAL TLS peer's SPKI. This excludes accidental TLS-terminating
// proxy aliases with unrelated front keys. Direct daemon HTTPS is the only
// qualified topology. A compromised server/admin can lie; this is not attestation.
func (c *Client) PhysicalIdentity(ctx context.Context) (PhysicalServerIdentity, error) {
	response, err := c.request(ctx, http.MethodGet, "/1.0", nil, nil)
	if err != nil {
		return PhysicalServerIdentity{}, err
	}
	return c.parsePhysicalIdentity(response)
}

// parsePhysicalIdentity reuses the same root response without another request.
// A transport pin can apply it at every trusted-root boundary before further IO.
func (c *Client) parsePhysicalIdentity(response envelope) (PhysicalServerIdentity, error) {
	if err := validateTrustedRoot(response, false); err != nil {
		return PhysicalServerIdentity{}, err
	}
	fields, err := object(response.metadata)
	if err != nil {
		return PhysicalServerIdentity{}, err
	}
	environment, err := object(fields["environment"])
	if err != nil {
		return PhysicalServerIdentity{}, ErrResponse
	}
	serverName, err := field[string](environment, "server_name")
	if err != nil || !label(serverName) || strings.TrimSpace(serverName) != serverName {
		return PhysicalServerIdentity{}, ErrResponse
	}
	certText, err := field[string](environment, "certificate")
	if err != nil || len(certText) == 0 || len(certText) > 16384 {
		return PhysicalServerIdentity{}, ErrResponse
	}
	trimmedCert := bytes.TrimSpace([]byte(certText))
	if !bytes.HasPrefix(trimmedCert, []byte("-----BEGIN CERTIFICATE-----")) {
		return PhysicalServerIdentity{}, ErrResponse
	}
	block, rest := pem.Decode(trimmedCert)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return PhysicalServerIdentity{}, ErrResponse
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return PhysicalServerIdentity{}, ErrResponse
	}
	reportedFingerprint, err := field[string](environment, "certificate_fingerprint")
	if err != nil || !fingerprint(reportedFingerprint) {
		return PhysicalServerIdentity{}, ErrResponse
	}
	certHash := sha256.Sum256(certificate.Raw)
	keyHash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	key := fmt.Sprintf("%x", keyHash)
	if reportedFingerprint != fmt.Sprintf("%x", certHash) || !fingerprint(response.serverKeyFingerprint) || key != response.serverKeyFingerprint {
		return PhysicalServerIdentity{}, ErrResponse
	}
	origin, err := c.canonicalOrigin()
	if err != nil {
		return PhysicalServerIdentity{}, err
	}
	originHash := sha256.Sum256([]byte(origin))
	scopeHash := sha256.Sum256([]byte(origin + "\x00" + c.project))
	serverVersion := ""
	if _, ok := environment["server_version"]; ok {
		v, e := field[string](environment, "server_version")
		if e != nil || !label(v) {
			return PhysicalServerIdentity{}, ErrResponse
		}
		serverVersion = v
	}
	return PhysicalServerIdentity{origin: fmt.Sprintf("%x", originHash), scope: fmt.Sprintf("%x", scopeHash), key: key, serverName: serverName, serverVersion: serverVersion, verified: true}, nil
}
