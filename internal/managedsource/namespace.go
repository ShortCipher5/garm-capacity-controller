package managedsource

import (
	"context"
	"crypto/tls"
	"net"
)

// NewWithDialer permits a root supervisor to enter an independently observed
// network namespace. Only TCP dialing is delegated: mandatory TLS, hostname and
// server-key validation, fixed read routes and response provenance stay here.
// The callback is not ownership or namespace evidence and cannot replace TLS.
func NewWithDialer(endpoint, pin, controller string, trust *tls.Config, dial func(context.Context, string, string) (net.Conn, error)) (*Client, error) {
	if dial == nil {
		return nil, ErrSource
	}
	c, e := New(endpoint, pin, controller, trust)
	if e != nil {
		return nil, e
	}
	c.transport.DialContext = dial
	return c, nil
}
