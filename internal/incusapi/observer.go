package incusapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

// NewObserver constructs a direct, project-scoped client for an independently
// enrolled, read-only Incus identity. It adds an exact server SPKI pin to the
// normal TLS hostname and chain checks and refuses unlisted outbound requests
// before any network I/O. The server must independently enforce the client's
// read-only authorization; this client does not infer permissions from TLS.
func NewObserver(endpoint, project, serverKeyPin string, credentials *tls.Config) (*Client, error) {
	trust, err := pinnedObserverTLS(serverKeyPin, credentials)
	if err != nil {
		return nil, err
	}
	client, err := New(endpoint, project, trust)
	if err != nil {
		return nil, err
	}
	client.observer = true
	return client, nil
}

func pinnedObserverTLS(serverKeyPin string, credentials *tls.Config) (*tls.Config, error) {
	if credentials == nil || !fingerprint(serverKeyPin) {
		return nil, ErrConfiguration
	}
	pin, err := hex.DecodeString(serverKeyPin)
	if err != nil {
		return nil, ErrConfiguration
	}
	trust := credentials.Clone()
	prior := trust.VerifyConnection
	trust.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return ErrTransport
		}
		key := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(key[:], pin) != 1 {
			return ErrTransport
		}
		if prior != nil {
			return prior(state)
		}
		return nil
	}
	return trust, nil
}

func observerRoute(method, path string, query url.Values) bool {
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/1.0", "/1.0/resources":
		return len(query) == 0
	case "/1.0/instances", "/1.0/operations":
		return len(query) == 1 && len(query["recursion"]) == 1 && query.Get("recursion") == "1"
	}
	if !strings.HasPrefix(path, "/1.0/instances/") || len(query) != 0 {
		return false
	}
	n := strings.TrimPrefix(path, "/1.0/instances/")
	return name(strings.TrimSuffix(n, "/state"))
}
