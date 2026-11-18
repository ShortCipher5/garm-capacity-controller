package garmapi

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// NewManagedInventory is a read-only mTLS proxy client. No stock password or JWT
// exists in the controller. Its transport can send only the exact inventory GET.
// The proxy is loopback in the managed namespace and independently key-pinned.
func NewManagedInventory(endpoint, serverKeyPin string, trust *tls.Config, providers []string, scalesets map[uint64]string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || trust == nil || len(trust.Certificates) != 1 {
		return nil, ErrInventory
	}
	pinned, err := securefiles.WithServerKeyPin(trust, serverKeyPin)
	if err != nil {
		return nil, ErrInventory
	}
	pinned.MinVersion = tls.VersionTLS13
	c, err := New(endpoint, "managed-mtls-not-a-stock-token", pinned, providers, scalesets)
	if err != nil {
		return nil, err
	}
	c.http.Transport = &managedInventoryTransport{base: c.http.Transport, endpoint: c.endpoint}
	return c, nil
}

type managedInventoryTransport struct {
	base     http.RoundTripper
	endpoint string
}

func (t *managedInventoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil || r.Method != http.MethodGet || r.URL.String() != t.endpoint || r.Body != nil {
		return nil, ErrInventory
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Del("Authorization")
	return t.base.RoundTrip(copy)
}
func (t *managedInventoryTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
