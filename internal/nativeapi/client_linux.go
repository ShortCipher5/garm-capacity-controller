//go:build linux

package nativeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
)

type Client struct {
	domain   *nativedomain.Domain
	policy   Policy
	http     *http.Client
	identity Identity
}

// Identity is minted by an actual private Unix root read, authenticated by the
// domain's birth pidfd and socket peer. It is NOT a TLS handshake observation,
// host hardware attestation, native execution authorization or release proof.
type Identity struct {
	domain, key, origin, scope, server, version string
	observed                                    time.Time
}

func (i Identity) Valid() bool {
	return hash(i.domain) && hash(i.key) && hash(i.origin) && hash(i.scope) && label(i.server) && i.version == NativeVersion && !i.observed.IsZero()
}
func (i Identity) DomainID() string      { return i.domain }
func (i Identity) ServerKey() string     { return i.key }
func (i Identity) Origin() string        { return i.origin }
func (i Identity) Scope() string         { return i.scope }
func (i Identity) ServerName() string    { return i.server }
func (i Identity) ServerVersion() string { return i.version }
func (i Identity) ObservedAt() time.Time { return i.observed }
func (c *Client) PolicyDigest() string {
	if c == nil {
		return ""
	}
	return c.policy.Digest()
}
func New(ctx context.Context, domain *nativedomain.Domain, policy Policy) (*Client, error) {
	if domain == nil || !hash(policy.Digest()) {
		return nil, ErrPolicy
	}
	birth := domain.Birth()
	if birth.ID != policy.spec.DomainID || birth.BinarySHA256 != policy.spec.NativeSHA256 || birth.LauncherSHA256 != policy.spec.LauncherSHA256 {
		return nil, ErrPolicy
	}
	c := &Client{domain: domain, policy: policy, http: &http.Client{Transport: domain, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	identity, err := c.Identity(ctx)
	if err != nil {
		return nil, err
	}
	c.identity = identity
	return c, nil
}
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	if c == nil || c.domain == nil || c.http == nil {
		return Identity{}, ErrNative
	}
	raw, err := c.read(ctx, "/1.0")
	if err != nil {
		return Identity{}, err
	}
	identity, err := parseIdentity(raw, c.domain.Birth().ID, time.Now().UTC())
	if err != nil {
		return Identity{}, err
	}
	if c.identity.Valid() && (identity.domain != c.identity.domain || identity.key != c.identity.key || identity.origin != c.identity.origin || identity.scope != c.identity.scope || identity.server != c.identity.server || identity.version != c.identity.version) {
		return Identity{}, ErrNative
	}
	return identity, nil
}
func (c *Client) read(ctx context.Context, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://native.invalid"+path, nil)
	if err != nil {
		return nil, ErrNative
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrNative
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrNative
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, ErrNative
	}
	return raw, nil
}
func object(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrNative
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok {
			return nil, ErrNative
		}
		if _, exists := values[key]; exists {
			return nil, ErrNative
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrNative
		}
		values[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrNative
	}
	return values, nil
}
func field[T any](m map[string]json.RawMessage, k string) (T, error) {
	var out T
	raw, ok := m[k]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &out) != nil {
		return out, ErrNative
	}
	return out, nil
}
func parseIdentity(raw []byte, domain string, now time.Time) (Identity, error) {
	if !hash(domain) || now.IsZero() {
		return Identity{}, ErrNative
	}
	envelope, err := object(raw)
	if err != nil {
		return Identity{}, err
	}
	kind, err := field[string](envelope, "type")
	if err != nil || kind != "sync" {
		return Identity{}, ErrNative
	}
	status, err := field[int](envelope, "status_code")
	if err != nil || status != 200 {
		return Identity{}, ErrNative
	}
	metadata, err := object(envelope["metadata"])
	if err != nil {
		return Identity{}, err
	}
	auth, err := field[string](metadata, "auth")
	if err != nil || auth != "trusted" {
		return Identity{}, ErrNative
	}
	api, err := field[string](metadata, "api_version")
	if err != nil || api != "1.0" {
		return Identity{}, ErrNative
	}
	stable, err := field[string](metadata, "api_status")
	if err != nil || stable != "stable" {
		return Identity{}, ErrNative
	}
	extensions, err := field[[]string](metadata, "api_extensions")
	if err != nil {
		return Identity{}, err
	}
	supports := false
	for _, e := range extensions {
		if e == "instance_create_start" {
			supports = true
		}
	}
	if !supports {
		return Identity{}, ErrNative
	}
	env, err := object(metadata["environment"])
	if err != nil {
		return Identity{}, err
	}
	cluster, err := field[bool](env, "server_clustered")
	if err != nil || cluster {
		return Identity{}, ErrNative
	}
	server, err := field[string](env, "server_name")
	if err != nil || !label(server) {
		return Identity{}, ErrNative
	}
	version, err := field[string](env, "server_version")
	if err != nil || version != NativeVersion {
		return Identity{}, ErrNative
	}
	text, err := field[string](env, "certificate")
	if err != nil || len(text) > 16384 || !strings.HasPrefix(strings.TrimSpace(text), "-----BEGIN CERTIFICATE-----") {
		return Identity{}, ErrNative
	}
	block, rest := pem.Decode([]byte(strings.TrimSpace(text)))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return Identity{}, ErrNative
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Identity{}, ErrNative
	}
	certHash := sha256.Sum256(cert.Raw)
	fingerprint, err := field[string](env, "certificate_fingerprint")
	if err != nil || fingerprint != fmt.Sprintf("%x", certHash) {
		return Identity{}, ErrNative
	}
	key := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	origin := sha256.Sum256([]byte("garm-owned-domain/v1\x00" + domain))
	scope := sha256.Sum256([]byte("garm-owned-domain/v1\x00" + domain + "\x00default"))
	return Identity{domain: domain, key: fmt.Sprintf("%x", key), origin: fmt.Sprintf("%x", origin), scope: fmt.Sprintf("%x", scope), server: server, version: version, observed: now}, nil
}
