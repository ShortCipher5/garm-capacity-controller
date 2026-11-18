// Package nativeidentity observes an enrolled owner over an actual pinned mTLS
// exchange. It supplies no native execution, enrollment writer or release API.
package nativeidentity

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrObservation = errors.New("verified live native owner observation required")

const Path = "/v1/native/domain"

type Descriptor struct {
	DomainID            string `json:"domain_id"`
	PolicyDigest        string `json:"policy_digest"`
	OwnerClientLeaf     string `json:"owner_client_leaf"`
	OwnerServerLeaf     string `json:"owner_server_leaf"`
	NativeServerKey     string `json:"native_server_key"`
	NativeOrigin        string `json:"native_origin"`
	NativeScope         string `json:"native_scope"`
	NativeServerName    string `json:"native_server_name"`
	NativeServerVersion string `json:"native_server_version"`
}

// Observation has no constructor from caller JSON or operator flags. Only
// Observe mints one after its own pinned TLS request and exact response parsing.
type Observation struct {
	record Descriptor
	at     time.Time
	valid  bool
}

func (o Observation) Valid() bool { return o.valid && !o.at.IsZero() }
func (o Observation) Record() Descriptor {
	if !o.Valid() {
		return Descriptor{}
	}
	return o.record
}
func (o Observation) ObservedAt() time.Time {
	if !o.Valid() {
		return time.Time{}
	}
	return o.at
}

type Configuration struct {
	Endpoint        string
	TLS             *tls.Config
	OwnerServerLeaf string
	OwnerClientLeaf string
	PolicyDigest    string
	// DomainID, when configured, forbids enrolling a replacement at that endpoint.
	DomainID string
}

func Fingerprint(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func validName(s string) bool { return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n") }

func Observe(ctx context.Context, c Configuration) (Observation, error) {
	if ctx == nil || ctx.Err() != nil || c.TLS == nil || c.TLS.InsecureSkipVerify || c.TLS.RootCAs == nil || c.TLS.GetClientCertificate != nil || len(c.TLS.Certificates) != 1 || len(c.TLS.Certificates[0].Certificate) == 0 || !validHash(c.OwnerServerLeaf) || !validHash(c.OwnerClientLeaf) || !validHash(c.PolicyDigest) || c.OwnerServerLeaf == c.OwnerClientLeaf || (c.DomainID != "" && !validHash(c.DomainID)) {
		return Observation{}, ErrObservation
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Observation{}, ErrObservation
	}
	if clientLeaf := Fingerprint(c.TLS.Certificates[0].Certificate[0]); clientLeaf == c.OwnerServerLeaf || clientLeaf == c.OwnerClientLeaf {
		return Observation{}, ErrObservation
	}
	tlsConfig := c.TLS.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.KeyLogWriter = nil
	previous := tlsConfig.VerifyConnection
	tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if state.Version < tls.VersionTLS13 || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 || Fingerprint(state.PeerCertificates[0].Raw) != c.OwnerServerLeaf {
			return ErrObservation
		}
		if previous != nil {
			return previous(state)
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	u.Path = Path
	at := time.Now().UTC()
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return Observation{}, ErrObservation
	}
	response, e := client.Do(request)
	if e != nil {
		return Observation{}, ErrObservation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.TLS == nil || !response.TLS.HandshakeComplete {
		return Observation{}, ErrObservation
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 16385))
	if e != nil || len(data) > 16384 {
		return Observation{}, ErrObservation
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	opening, e := decoder.Token()
	if e != nil || opening != json.Delim('{') {
		return Observation{}, ErrObservation
	}
	var d Descriptor
	fields := map[string]*string{"domain_id": &d.DomainID, "policy_digest": &d.PolicyDigest, "owner_client_leaf": &d.OwnerClientLeaf, "owner_server_leaf": &d.OwnerServerLeaf, "native_server_key": &d.NativeServerKey, "native_origin": &d.NativeOrigin, "native_scope": &d.NativeScope, "native_server_name": &d.NativeServerName, "native_server_version": &d.NativeServerVersion}
	seen := map[string]bool{}
	for decoder.More() {
		key, e := decoder.Token()
		name, ok := key.(string)
		if e != nil || !ok || seen[name] || fields[name] == nil {
			return Observation{}, ErrObservation
		}
		seen[name] = true
		if decoder.Decode(fields[name]) != nil {
			return Observation{}, ErrObservation
		}
	}
	closing, e := decoder.Token()
	if e != nil || closing != json.Delim('}') || len(seen) != len(fields) {
		return Observation{}, ErrObservation
	}
	if _, e = decoder.Token(); e != io.EOF {
		return Observation{}, ErrObservation
	}
	if !validHash(d.DomainID) || !validHash(d.NativeServerKey) || !validName(d.NativeServerName) || d.NativeServerVersion != "6.0.4" || d.OwnerServerLeaf != c.OwnerServerLeaf || d.OwnerClientLeaf != c.OwnerClientLeaf || d.PolicyDigest != c.PolicyDigest || (c.DomainID != "" && d.DomainID != c.DomainID) || d.NativeOrigin != Fingerprint([]byte("garm-owned-domain/v1\x00"+d.DomainID)) || d.NativeScope != Fingerprint([]byte("garm-owned-domain/v1\x00"+d.DomainID+"\x00default")) {
		return Observation{}, ErrObservation
	}
	return Observation{record: d, at: at, valid: true}, nil
}
