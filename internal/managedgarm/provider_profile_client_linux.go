package managedgarm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type ProviderProfileConfiguration struct {
	Origin       *ProviderOriginConfiguration
	TLS          securefiles.TLSFiles
	ServerKeyPin string
}
type providerProfileClient struct {
	mu        sync.Mutex
	session   *Session
	trust     *tls.Config
	sequence  int64
	digest    string
	lineage   string
	revoked   bool
	clientKey string
}

func newProviderProfileClient(s *Session, c ProviderProfileConfiguration) (*providerProfileClient, error) {
	if s == nil {
		return nil, ErrBoundary
	}
	for _, path := range []string{c.TLS.CA, c.TLS.Certificate, c.TLS.Key} {
		if ValidateSupervisorFilePath(path) != nil {
			return nil, ErrBoundary
		}
	}
	trust, err := securefiles.LoadTLS(c.TLS)
	if err != nil {
		return nil, ErrBoundary
	}
	trust, err = securefiles.WithServerKeyPin(trust, c.ServerKeyPin)
	if err != nil || len(trust.Certificates) != 1 {
		return nil, ErrBoundary
	}
	leaf, err := x509.ParseCertificate(trust.Certificates[0].Certificate[0])
	if err != nil || hashBytes(leaf.RawSubjectPublicKeyInfo) == c.ServerKeyPin {
		return nil, ErrBoundary
	}
	trust.MinVersion = tls.VersionTLS13
	trust.ServerName = "127.0.0.1"
	trust.KeyLogWriter = nil
	return &providerProfileClient{session: s, trust: trust, sequence: -1, clientKey: hashBytes(leaf.RawSubjectPublicKeyInfo)}, nil
}
func (c *providerProfileClient) read(ctx context.Context) (profilesnapshot.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fail := func() (profilesnapshot.Snapshot, error) {
		c.revoked = true
		return profilesnapshot.Snapshot{}, ErrBoundary
	}
	if c.revoked || ctx == nil || ctx.Err() != nil || c.session.Verify(ctx) != nil {
		return fail()
	}
	o, err := Observe(ctx, c.session.spec)
	if err != nil {
		return fail()
	}
	fd, err := o.OpenNetworkNamespace()
	if err != nil {
		return fail()
	}
	defer fd.Close()
	nonce, err := randomHex(32)
	if err != nil {
		return fail()
	}
	tr := &http.Transport{TLSClientConfig: c.trust.Clone(), Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialNamespaceAddress(ctx, fd, "127.0.0.1:9443")
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://127.0.0.1:9443"+profilesnapshot.Path, nil)
	if err != nil {
		return fail()
	}
	req.Header.Set(profilesnapshot.NonceHeader, nonce)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fail()
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail()
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return fail()
	}
	s, err := profilesnapshot.Decode(raw, c.session.ControllerID(), nonce, time.Now())
	if err != nil || c.session.Verify(ctx) != nil {
		return fail()
	}
	if c.acceptSnapshot(s) != nil {
		return fail()
	}
	return s, nil
}

// Called under mu only after transport authentication, nonce/freshness decoding
// and the final topology check. This helper cannot manufacture an observation.
func (c *providerProfileClient) acceptSnapshot(s profilesnapshot.Snapshot) error {
	if c.revoked || s.Sequence < c.sequence || (c.lineage != "" && s.Lineage != c.lineage) || (c.digest != "" && s.ProfileDigest != c.digest) {
		c.revoked = true
		return ErrBoundary
	}
	c.sequence, c.lineage, c.digest = s.Sequence, s.Lineage, s.ProfileDigest
	return nil
}
