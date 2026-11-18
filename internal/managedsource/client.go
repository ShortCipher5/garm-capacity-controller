// Package managedsource binds read provenance to a verified private supervisor.
// It does not grant capacity or mutation authority.
package managedsource

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const SourceHeader = "X-Capacity-Source-ID"
const ControllerHeader = "X-Capacity-Controller-ID"

var ErrSource = errors.New("managed source unavailable or changed")

type Client struct {
	mu                                      sync.Mutex
	base, pin, controller, identity, source string
	revoked                                 bool
	transport                               *http.Transport
}

func New(endpoint, pin, controller string, trust *tls.Config) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || !uuid(controller) || trust == nil || len(trust.Certificates) != 1 {
		return nil, ErrSource
	}
	t, e := securefiles.WithServerKeyPin(trust, pin)
	if e != nil {
		return nil, ErrSource
	}
	t.MinVersion = tls.VersionTLS13
	t.KeyLogWriter = nil
	t.ServerName = u.Hostname()
	return &Client{base: strings.TrimSuffix(u.String(), "/"), pin: pin, controller: controller, transport: &http.Transport{TLSClientConfig: t, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}}, nil
}
func (c *Client) Identity() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revoked {
		return ""
	}
	return c.identity
}
func (c *Client) ControllerID() string  { return c.controller }
func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }
func (c *Client) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil || r.Method != http.MethodGet || r.Body != nil || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.Fragment != "" || r.URL.User != nil || r.URL.Scheme+"://"+r.URL.Host != c.base || !readPath(r.URL.Path) {
		return nil, ErrSource
	}
	c.mu.Lock()
	blocked := c.revoked
	c.mu.Unlock()
	if blocked {
		return nil, ErrSource
	}
	q := r.Clone(r.Context())
	q.Header = r.Header.Clone()
	q.Header.Del("Authorization")
	q.Header.Del(SourceHeader)
	q.Header.Del(ControllerHeader)
	res, e := c.transport.RoundTrip(q)
	if e != nil {
		return nil, e
	}
	if res.StatusCode != http.StatusOK {
		return res, nil
	}
	source := res.Header.Get(SourceHeader)
	controller := res.Header.Get(ControllerHeader)
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, e := hex.DecodeString(source)
	if c.revoked || e != nil || len(raw) != 32 || hex.EncodeToString(raw) != source || len(res.Header.Values(SourceHeader)) != 1 || len(res.Header.Values(ControllerHeader)) != 1 || controller != c.controller || (c.source != "" && c.source != source) {
		c.revoked = true
		res.Body.Close()
		return nil, ErrSource
	}
	c.source = source
	digest := sha256.Sum256([]byte("managed-stock-source/v1\x00" + c.base + "\x00" + c.pin + "\x00" + source + "\x00" + controller))
	c.identity = hex.EncodeToString(digest[:])
	return res, nil
}
func readPath(p string) bool {
	if p == "/api/v1/instances" || p == "/api/v1/pools" || p == "/api/v1/jobs" {
		return true
	}
	return strings.HasPrefix(p, "/api/v1/pools/") && uuid(strings.TrimPrefix(p, "/api/v1/pools/"))
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
