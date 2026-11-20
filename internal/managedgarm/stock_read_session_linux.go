package managedgarm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Private existing-record-only client; no production constructor caller exists.
// No RoundTripper, arbitrary request, namespace/listener accessor or renewal.
type stockReadSession struct {
	mu       sync.Mutex
	life     *stockReadLifecycle
	trust    *tls.Config
	token    string
	identity string
	ready    bool
	closed   bool
}

func stockReadTLS(der []byte, expected string) (*tls.Config, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil || !digest.MatchString(expected) || stockBindingDigest(der) != expected {
		return nil, ErrBoundary
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "127.0.0.1", RootCAs: roots, VerifyConnection: func(cs tls.ConnectionState) error {
		// Standard chain, time and hostname verification runs first (no bypass).
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || stockBindingDigest(cs.PeerCertificates[0].Raw) != expected {
			return ErrBoundary
		}
		return nil
	}}, nil
}

func newStockReadSession(ctx context.Context) (*stockReadSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrBoundary
	}
	life, err := newStockReadLifecycle(ctx)
	if err != nil {
		return nil, ErrBoundary
	}
	s := &stockReadSession{life: life}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	life.mu.Lock()
	if life.closed || life.binding == nil {
		life.mu.Unlock()
		return nil, ErrBoundary
	}
	profile := life.binding.observed.profile
	expected := life.auth
	life.mu.Unlock()
	material, err := loadStockReadAuthMaterial(ctx, profile)
	defer material.clear()
	if err != nil || material.binding != expected {
		return nil, ErrBoundary
	}
	s.trust, err = stockReadTLS(material.der, expected.review.LeafSHA256)
	if err != nil {
		return nil, ErrBoundary
	}
	// Existing generated credential protocol, exactly login then controller GET;
	// no first-run, record creation, repair or token renewal branch.
	record, token, err := authenticateBootstrapCredential(ctx, material.user, expected.record.bootstrapAttempt, s.authenticate)
	if err != nil || record != expected.record || life.recheck(ctx) != nil {
		return nil, ErrBoundary
	}
	after, err := loadStockReadAuthBinding(ctx, profile)
	if err != nil || after != expected {
		return nil, ErrBoundary
	}
	s.token = token
	s.identity = stockBindingDigest([]byte("stock-readonly-existing/v1\x00" + expected.review.GarmID + "\x00" + record.ControllerID + "\x00" + expected.review.LeafSHA256 + "\x00" + expected.review.RecordSHA256))
	s.ready = true
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-life.done:
			s.Close()
		}
	}()
	if ctx.Err() != nil {
		return nil, ErrBoundary
	}
	ok = true
	return s, nil
}

// Constructor-only route gate. The supplied protocol has no means of creating
// authority: every exchange independently requires the actual owned lifecycle.
func (s *stockReadSession) authenticate(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if s.ready || s.closed || !((method == http.MethodPost && path == "/api/v1/auth/login" && token == "" && body != nil) || (method == http.MethodGet && path == "/api/v1/controller" && token != "" && body == nil)) {
		return nil, ErrBoundary
	}
	return s.exchange(ctx, method, path, token, body)
}

// Called only with lifecycle/holder locked; never accepts a namespace argument.
func (s *stockReadSession) dial(ctx context.Context) (net.Conn, error) {
	o := s.life
	if o == nil {
		return nil, ErrBoundary
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.binding == nil || ctx.Err() != nil {
		return nil, ErrBoundary
	}
	return o.binding.dialStockAPI(ctx)
}

func (s *stockReadSession) exchange(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || s.life == nil || s.trust == nil || s.life.recheck(ctx) != nil {
		return nil, ErrBoundary
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tr := &http.Transport{Proxy: nil, TLSClientConfig: s.trust, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "127.0.0.1:9998" {
			return nil, ErrBoundary
		}
		return s.dial(ctx)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
	var input []byte
	var err error
	if body != nil {
		input, err = json.Marshal(body)
		if err != nil {
			return nil, ErrBoundary
		}
		defer clear(input)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://127.0.0.1:9998"+path, bytes.NewReader(input))
	if err != nil {
		return nil, ErrBoundary
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrBoundary
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 || response.StatusCode != http.StatusOK || s.life.recheck(ctx) != nil || ctx.Err() != nil {
		clear(raw)
		return nil, ErrBoundary
	}
	return raw, nil
}
func (s *stockReadSession) read(ctx context.Context, path string) ([]byte, error) {
	if s == nil {
		return nil, ErrBoundary
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.ready || s.token == "" || !stockReadDataPath(path) {
		s.closeLocked()
		return nil, ErrBoundary
	}
	raw, err := s.exchange(ctx, http.MethodGet, path, s.token, nil)
	if err != nil {
		s.closeLocked()
		return nil, ErrBoundary
	}
	return raw, nil
}
func stockReadDataPath(path string) bool {
	if path == "/api/v1/instances" || path == "/api/v1/pools" || path == "/api/v1/jobs" {
		return true
	}
	const prefix = "/api/v1/pools/"
	return strings.HasPrefix(path, prefix) && stockUUID(strings.TrimPrefix(path, prefix))
}
func (s *stockReadSession) Instances(ctx context.Context) ([]byte, error) {
	return s.read(ctx, "/api/v1/instances")
}
func (s *stockReadSession) Pools(ctx context.Context) ([]byte, error) {
	return s.read(ctx, "/api/v1/pools")
}
func (s *stockReadSession) Jobs(ctx context.Context) ([]byte, error) {
	return s.read(ctx, "/api/v1/jobs")
}
func (s *stockReadSession) Pool(ctx context.Context, id string) ([]byte, error) {
	if !stockUUID(id) {
		return nil, ErrBoundary
	}
	return s.read(ctx, "/api/v1/pools/"+id)
}
func (s *stockReadSession) Identity() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.ready {
		return ""
	}
	select {
	case <-s.life.done:
		s.closeLocked()
		return ""
	default:
	}
	return s.identity
}
func (s *stockReadSession) closeLocked() {
	s.closed = true
	s.ready = false
	s.token = ""
	s.identity = ""
	if s.life != nil {
		s.life.close()
	}
}
func (s *stockReadSession) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}
