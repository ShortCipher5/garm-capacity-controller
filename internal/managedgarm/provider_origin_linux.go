package managedgarm

import (
	"context"
	"crypto/x509"
	"net"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// ProviderOriginConfiguration locates the actual root writer's authenticated
// descriptor. It contains no expected source hash; identity comes from a fresh
// pinned-TLS response and must match the enrolled profiles.
type ProviderOriginConfiguration struct {
	Endpoint, ServerKeyPin string
	TLS                    securefiles.TLSFiles
}

func observeProviderOrigin(ctx context.Context, s *Session, c *ProviderOriginConfiguration) (string, error) {
	if s == nil || c == nil || c.Endpoint != "https://127.0.0.1:9445" || s.Verify(ctx) != nil {
		return "", ErrBoundary
	}
	for _, p := range []string{c.TLS.CA, c.TLS.Certificate, c.TLS.Key} {
		if ValidateSupervisorFilePath(p) != nil {
			return "", ErrBoundary
		}
	}
	trust, err := securefiles.LoadTLS(c.TLS)
	if err != nil || len(trust.Certificates) != 1 || len(trust.Certificates[0].Certificate) == 0 {
		return "", ErrBoundary
	}
	leaf, err := x509.ParseCertificate(trust.Certificates[0].Certificate[0])
	if err != nil || hashBytes(leaf.RawSubjectPublicKeyInfo) == c.ServerKeyPin {
		return "", ErrBoundary
	}
	topology, err := Observe(ctx, s.spec)
	if err != nil {
		return "", ErrBoundary
	}
	namespace, err := topology.OpenNetworkNamespace()
	if err != nil {
		return "", ErrBoundary
	}
	defer namespace.Close()
	client, err := managedsource.NewWithDialer(c.Endpoint, c.ServerKeyPin, s.ControllerID(), trust, func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialNamespaceAddress(ctx, namespace, "127.0.0.1:9445")
	})
	if err != nil {
		return "", ErrBoundary
	}
	defer client.CloseIdleConnections()
	observation, err := client.ObserveSource(ctx)
	if err != nil || s.Verify(ctx) != nil {
		return "", ErrBoundary
	}
	if !observation.Valid() || observation.StockSourceIdentity() != s.Identity() || observation.ControllerID() != s.ControllerID() || observation.SourceIdentity() == "" || time.Since(observation.ObservedAt()) > 5*time.Second {
		return "", ErrBoundary
	}
	return observation.SourceIdentity(), nil
}
