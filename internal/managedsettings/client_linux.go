// Package managedsettings reaches only the authenticated supervisor settings
// journal. It exposes strict reductions, never generic stock API writes.
package managedsettings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

var ErrSettings = errors.New("managed settings unavailable or changed")

type Configuration struct {
	Endpoint, ServerKeyPin, ControllerID string
	TLS                                  *tls.Config
	Providers, PoolIDs                   []string
	AllowReductions                      bool
}
type Client struct {
	base, controller, source, pin string
	transport                     *http.Transport
	gate                          chan struct{}
	providers, pools              map[string]bool
	writes, unknown, revoked      bool
}

func uuid(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil && len(b) == 16 && strings.ToLower(s) == s
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func New(c Configuration) (*Client, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || !uuid(c.ControllerID) || !digest(c.ServerKeyPin) || len(c.PoolIDs) == 0 || len(c.PoolIDs) > 1000 || len(c.Providers) == 0 || len(c.Providers) > 100 || c.TLS == nil || c.TLS.RootCAs == nil || len(c.TLS.Certificates) != 1 || c.TLS.InsecureSkipVerify || c.TLS.VerifyConnection != nil || c.TLS.VerifyPeerCertificate != nil || c.TLS.GetClientCertificate != nil {
		return nil, ErrSettings
	}
	// Normalize and own TLS configuration; callbacks and mutable private material
	// cannot change peer-role verification after construction.
	cert := c.TLS.Certificates[0]
	raw, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if e != nil {
		return nil, ErrSettings
	}
	key, e := x509.ParsePKCS8PrivateKey(raw)
	for i := range raw {
		raw[i] = 0
	}
	if e != nil || len(cert.Certificate) == 0 {
		return nil, ErrSettings
	}
	owned := tls.Certificate{PrivateKey: key}
	for _, der := range cert.Certificate {
		owned.Certificate = append(owned.Certificate, append([]byte(nil), der...))
	}
	trust, e := securefiles.WithServerKeyPin(&tls.Config{RootCAs: c.TLS.RootCAs.Clone(), Certificates: []tls.Certificate{owned}, MinVersion: tls.VersionTLS13, ServerName: u.Hostname()}, c.ServerKeyPin)
	if e != nil {
		return nil, ErrSettings
	}
	client := &Client{base: strings.TrimSuffix(u.String(), "/"), controller: c.ControllerID, pin: c.ServerKeyPin, gate: make(chan struct{}, 1), providers: map[string]bool{}, pools: map[string]bool{}, writes: c.AllowReductions, transport: &http.Transport{TLSClientConfig: trust, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second}}
	for _, p := range c.Providers {
		if p == "" || len(p) > 256 || client.providers[p] {
			return nil, ErrSettings
		}
		client.providers[p] = true
	}
	for _, p := range c.PoolIDs {
		if !uuid(p) || client.pools[p] {
			return nil, ErrSettings
		}
		client.pools[p] = true
	}
	return client, nil
}
func (c *Client) Close() { c.transport.CloseIdleConnections() }
func (c *Client) enter(ctx context.Context) error {
	if ctx == nil {
		return ErrSettings
	}
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrSettings
	}
}
func (c *Client) leave() { <-c.gate }
func (c *Client) exchange(ctx context.Context, method, path string, body any, out any) error {
	if c.revoked {
		return ErrSettings
	}
	var data []byte
	var e error
	if body != nil {
		data, e = json.Marshal(body)
		if e != nil || len(data) > 4096 {
			return ErrSettings
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if e != nil {
		return ErrSettings
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	// RoundTrip performs exactly one request. No redirects, cookies or retries.
	res, e := c.transport.RoundTrip(req)
	if e != nil {
		return ErrSettings
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ErrSettings
	}
	source := res.Header.Get(managedsource.SourceHeader)
	if len(res.Header.Values(managedsource.SourceHeader)) != 1 || !digest(source) || len(res.Header.Values(managedsource.ControllerHeader)) != 1 || res.Header.Get(managedsource.ControllerHeader) != c.controller || c.source != "" && c.source != source {
		c.revoked = true
		return ErrSettings
	}
	c.source = source
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return ErrSettings
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrSettings
	}
	return nil
}
func validStatus(v garmmanagement.Status) bool {
	if len(v.Pending) > 1 || v.WriteBlocked != (len(v.Pending) > 0) {
		return false
	}
	for _, p := range v.Pending {
		if !uuid(p.RequestID) || p.Kind != "pool" && p.Kind != "repository" || !uuid(p.Target) || p.State != "pending" && p.State != "unknown" {
			return false
		}
		if _, e := time.Parse(time.RFC3339Nano, p.CreatedAt); e != nil {
			return false
		}
	}
	return true
}
func (c *Client) validPool(p garmapi.ManagedPool) bool {
	return c.pools[p.ID] && c.providers[p.ProviderName] && digest(p.Fingerprint) && p.MaxRunners <= 1000000 && p.MinIdleRunners <= p.MaxRunners && p.Priority <= 1000000 && p.Generation <= 9007199254740991 && len(p.Tags) <= 100
}
func (c *Client) snapshot(ctx context.Context) (garmmanagement.Snapshot, error) {
	var v garmmanagement.Snapshot
	if e := c.exchange(ctx, http.MethodGet, "/v1/managed/settings", nil, &v); e != nil {
		return v, e
	}
	if !validStatus(v.Status) || len(v.Pools) > 1000 || len(v.Repositories) > 1000 {
		return v, ErrSettings
	}
	filtered := []garmapi.ManagedPool{}
	seen := map[string]bool{}
	for _, p := range v.Pools {
		if !c.pools[p.ID] {
			continue
		}
		if seen[p.ID] || !c.validPool(p) {
			return v, ErrSettings
		}
		seen[p.ID] = true
		filtered = append(filtered, p)
	}
	if len(filtered) != len(c.pools) {
		return v, ErrSettings
	}
	v.Pools = filtered
	v.Repositories = []garmapi.ManagedRepository{}
	v.WritesEnabled = c.writes && !c.unknown && !c.revoked && v.WritesEnabled
	c.overlayLocalWriteHold(&v.Status)
	return v, nil
}
func (c *Client) Snapshot(ctx context.Context) (garmmanagement.Snapshot, error) {
	if e := c.enter(ctx); e != nil {
		return garmmanagement.Snapshot{}, e
	}
	defer c.leave()
	return c.snapshot(ctx)
}
func (c *Client) Status(ctx context.Context) (garmmanagement.Status, error) {
	if e := c.enter(ctx); e != nil {
		return garmmanagement.Status{}, e
	}
	defer c.leave()
	var s garmmanagement.Status
	if e := c.exchange(ctx, http.MethodGet, "/v1/managed/status", nil, &s); e != nil || !validStatus(s) {
		return s, ErrSettings
	}
	c.overlayLocalWriteHold(&s)
	return s, nil
}

// overlayLocalWriteHold runs only after authenticating and validating supervisor
// status. A pending journal row, when present, remains the primary evidence.
func (c *Client) overlayLocalWriteHold(s *garmmanagement.Status) {
	if c.unknown {
		s.WriteBlocked = true
		s.ManagedClientWriteHold = len(s.Pending) == 0
	}
}
func (c *Client) ReduceMaximum(ctx context.Context, request, pool, expected string, target uint64, reason string) (garmapi.ManagedPool, error) {
	if e := c.enter(ctx); e != nil {
		return garmapi.ManagedPool{}, e
	}
	defer c.leave()
	if !c.writes || c.unknown || c.revoked {
		return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
	}
	if !uuid(request) || !c.pools[pool] || !digest(expected) || target == 0 || target > 1000000 || strings.TrimSpace(reason) == "" || len(reason) > 256 {
		return garmapi.ManagedPool{}, garmapi.ErrManagementUnsafe
	}
	before, e := c.snapshot(ctx)
	if e != nil {
		return garmapi.ManagedPool{}, e
	}
	if !before.WritesEnabled {
		return garmapi.ManagedPool{}, garmapi.ErrManagementDisabled
	}
	if before.Status.WriteBlocked {
		return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
	}
	var selected garmapi.ManagedPool
	for _, p := range before.Pools {
		if p.ID == pool {
			selected = p
		}
	}
	if selected.Fingerprint != expected || target >= selected.MaxRunners || target < selected.MinIdleRunners {
		return garmapi.ManagedPool{}, garmapi.ErrManagementConflict
	}
	body := struct {
		Request  string `json:"request_id"`
		Expected string `json:"expected_fingerprint"`
		Maximum  uint64 `json:"maximum"`
		Reason   string `json:"reason"`
	}{request, expected, target, reason}
	var out garmapi.ManagedPool
	if e = c.exchange(ctx, http.MethodPost, "/v1/managed/pools/"+pool+"/reduce-maximum", body, &out); e != nil || !c.validPool(out) || out.ID != pool || out.MaxRunners != target || out.MinIdleRunners != selected.MinIdleRunners || out.Enabled != selected.Enabled || out.Priority != selected.Priority || out.Generation != selected.Generation || out.ProviderName != selected.ProviderName || out.RepositoryID != selected.RepositoryID || !equalTags(out.Tags, selected.Tags) {
		c.unknown = true
		return garmapi.ManagedPool{}, garmapi.ErrManagementOutcomeUnknown
	}
	return out, nil
}
func equalTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ObservedSource returns only identity learned from authenticated responses.
// It is provenance for matching enrollment, never a capacity-increase permit.
func (c *Client) ObservedSource(ctx context.Context) (string, string, error) {
	if e := c.enter(ctx); e != nil {
		return "", "", e
	}
	defer c.leave()
	if c.revoked || c.source == "" {
		return "", "", ErrSettings
	}
	hash := sha256.Sum256([]byte("managed-stock-source/v1\x00" + c.base + "\x00" + c.pin + "\x00" + c.source + "\x00" + c.controller))
	return hex.EncodeToString(hash[:]), c.controller, nil
}
