package incusapi

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// identityTransport switches once to a FRESH connection pool when pinned.
// Merely changing VerifyConnection would leave already-pooled unpinned TLS
// connections usable, and mutating an in-use tls.Config would race.
type identityTransport struct {
	initial  *http.Transport
	selected atomic.Pointer[http.Transport]
}

func (t *identityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	transport := t.selected.Load()
	if transport == nil {
		transport = t.initial
	}
	return transport.RoundTrip(r)
}
func (t *identityTransport) CloseIdleConnections() {
	t.initial.CloseIdleConnections()
	if selected := t.selected.Load(); selected != nil {
		selected.CloseIdleConnections()
	}
}
func newIncusTransport(config *tls.Config) *http.Transport {
	return &http.Transport{TLSClientConfig: config, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConnsPerHost: 4}
}

type identityPinState struct {
	mu        sync.Mutex
	observed  atomic.Pointer[PhysicalServerIdentity]
	config    *tls.Config
	transport *identityTransport
}

// PinPhysicalIdentity is immutable/idempotent. It binds later TLS handshakes
// BEFORE any HTTP request body, and fresh trusted-root reads bind name/project.
// Caller must first compare this authenticated observation with the durable
// ledger binding; a key alone is not hardware or whole-host scope proof.
func (c *Client) PinPhysicalIdentity(value PhysicalServerIdentity) error {
	if !value.Valid() || c.pin == nil {
		return ErrConfiguration
	}
	origin, err := c.canonicalOrigin()
	if err != nil {
		return err
	}
	originHash := sha256.Sum256([]byte(origin))
	scopeHash := sha256.Sum256([]byte(origin + "\x00" + c.project))
	if value.origin != fmt.Sprintf("%x", originHash) || value.scope != fmt.Sprintf("%x", scopeHash) {
		return ErrConfiguration
	}
	c.pin.mu.Lock()
	defer c.pin.mu.Unlock()
	if old := c.pin.observed.Load(); old != nil {
		if *old == value {
			return nil
		}
		return ErrUnsupported
	}
	config := c.pin.config.Clone()
	previous := config.VerifyConnection
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if previous != nil {
			if err := previous(state); err != nil {
				return ErrTransport
			}
		}
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return ErrTransport
		}
		key := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if fmt.Sprintf("%x", key) != value.key {
			return ErrTransport
		}
		return nil
	}
	// The new pool is pinned from construction, never reuses discovery sockets,
	// and is selected before Pin returns to any gateway creator.
	c.pin.transport.selected.Store(newIncusTransport(config))
	c.pin.observed.Store(&value)
	c.pin.transport.initial.CloseIdleConnections()
	return nil
}
func (c *Client) checkPinnedRoot(response envelope) error {
	if c.pin == nil {
		return nil
	}
	expected := c.pin.observed.Load()
	if expected == nil {
		return nil
	}
	actual, err := c.parsePhysicalIdentity(response)
	if err != nil || actual != *expected {
		return ErrUnsupported
	}
	return nil
}
