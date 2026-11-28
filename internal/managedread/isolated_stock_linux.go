package managedread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// isolatedStockReads is a local-only candidate for a separate inventory
// process. The stock bearer must never be accessible to the dashboard.
var tlsName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,251}[A-Za-z0-9]$`)

type isolatedStockReads struct {
	base, bearer, controller, identity, host string
	client                                   *http.Client
	revoked                                  atomic.Bool
	login                                    *stockTokenCache
}

func newIsolatedStockReads(ctx context.Context, endpoint, controller, serverPin, bearer string, trust *tls.Config) (*isolatedStockReads, error) {
	return newStockReads(ctx, endpoint, controller, serverPin, bearer, "", "", trust)
}

var errStockUnauthorized = errors.New("stock inventory authentication rejected")

func newStockReads(ctx context.Context, endpoint, controller, serverPin, bearer, user, passwordFile string, trust *tls.Config) (*isolatedStockReads, error) {
	if (bearer != "") == (passwordFile != "") || (user != "") != (passwordFile != "") || len(user) > 64 {
		return nil, ErrConfiguration
	}
	u, err := url.Parse(endpoint)
	if ctx == nil || err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") ||
		!uuid.MatchString(controller) || len(bearer) > 16384 ||
		strings.ContainsAny(bearer, " \t\r\n") || trust == nil || trust.VerifyConnection != nil ||
		trust.VerifyPeerCertificate != nil || trust.KeyLogWriter != nil ||
		(trust.MaxVersion != 0 && trust.MaxVersion < tls.VersionTLS13) {
		return nil, ErrConfiguration
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Zone() != "" || !(ip.IsLoopback() || ip.IsPrivate()) {
		return nil, ErrConfiguration
	}
	pinned, err := securefiles.WithServerKeyPin(trust, serverPin)
	if err != nil {
		return nil, ErrConfiguration
	}
	pinned.MinVersion = tls.VersionTLS13
	// The socket destination stays fixed to a private literal address. A
	// separately configured name is only for normal SAN verification.
	if pinned.ServerName == "" {
		pinned.ServerName = u.Hostname()
	}
	if trust.ServerName != "" && !tlsName.MatchString(trust.ServerName) {
		return nil, ErrConfiguration
	}
	pinned.ClientSessionCache = nil
	base := strings.TrimSuffix(u.String(), "/")
	id := sha256.Sum256([]byte("isolated-stock-inventory/v1\x00" + base + "\x00" + pinned.ServerName + "\x00" + serverPin + "\x00" + controller))
	s := &isolatedStockReads{base: base, bearer: bearer, controller: controller, identity: hex.EncodeToString(id[:]), host: trust.ServerName,
		client: &http.Client{Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{TLSClientConfig: pinned, DisableKeepAlives: true, ResponseHeaderTimeout: 4 * time.Second}}}
	if passwordFile != "" {
		if !ip.IsLoopback() {
			return nil, ErrConfiguration
		}
		s.login = &stockTokenCache{passwordFile: passwordFile, username: user, request: s.loginRequest}
	}
	token, err := s.token(ctx, "")
	if err != nil || s.verify(ctx, token) != nil {
		return nil, ErrConfiguration
	}
	return s, nil
}

func (s *isolatedStockReads) Identity() string {
	if s.revoked.Load() {
		return ""
	}
	return s.identity
}
func (s *isolatedStockReads) ControllerID() string { return s.controller }

func (s *isolatedStockReads) token(ctx context.Context, rejected string) (string, error) {
	if s.revoked.Load() {
		return "", ErrConfiguration
	}
	if s.login == nil {
		return s.bearer, nil
	}
	return s.login.current(ctx, rejected)
}

func (s *isolatedStockReads) loginRequest(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return nil, ErrConfiguration
	}
	if s.host != "" {
		req.Host = s.host
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return s.client.Do(req)
}

func (s *isolatedStockReads) get(ctx context.Context, path, token string) ([]byte, error) {
	if s.revoked.Load() {
		return nil, ErrConfiguration
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, ErrConfiguration
	}
	if s.host != "" {
		req.Host = s.host
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	r, err := s.client.Do(req)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusUnauthorized && s.login != nil {
		return nil, errStockUnauthorized
	}
	if r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusForbidden {
		s.revoked.Store(true)
	}
	if r.StatusCode != http.StatusOK {
		return nil, ErrConfiguration
	}
	const max = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil || len(raw) > max {
		return nil, ErrConfiguration
	}
	return raw, nil
}

func (s *isolatedStockReads) verify(ctx context.Context, token string) error {
	raw, err := s.get(ctx, "/api/v1/controller", token)
	if err != nil {
		return err
	}
	var marker struct {
		ID string `json:"controller_id"`
	}
	if json.Unmarshal(raw, &marker) != nil || marker.ID != s.controller {
		s.revoked.Store(true)
		return ErrConfiguration
	}
	return nil
}

func (s *isolatedStockReads) read(ctx context.Context, path string) ([]byte, error) {
	return s.readInventory(ctx, func(ctx context.Context, token string) ([]byte, error) {
		return s.get(ctx, path, token)
	})
}

func (s *isolatedStockReads) readInventory(ctx context.Context, fetch func(context.Context, string) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	token, err := s.token(ctx, "")
	if err != nil {
		return nil, ErrConfiguration
	}
	raw, err := s.readWithToken(ctx, token, fetch)
	if errors.Is(err, errStockUnauthorized) && s.login != nil && !s.revoked.Load() {
		token, err = s.token(ctx, token)
		if err == nil {
			raw, err = s.readWithToken(ctx, token, fetch)
		}
	}
	if err != nil {
		return nil, ErrConfiguration
	}
	return raw, nil
}

func (s *isolatedStockReads) readWithToken(ctx context.Context, token string, fetch func(context.Context, string) ([]byte, error)) ([]byte, error) {
	if err := s.verify(ctx, token); err != nil {
		return nil, err
	}
	raw, err := fetch(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.verify(ctx, token); err != nil {
		return nil, err
	}
	if s.revoked.Load() || ctx.Err() != nil {
		return nil, ErrConfiguration
	}
	return raw, nil
}

func (s *isolatedStockReads) Instances(ctx context.Context) ([]byte, error) {
	return s.read(ctx, "/api/v1/instances")
}
func (s *isolatedStockReads) Pools(ctx context.Context) ([]byte, error) {
	return s.read(ctx, "/api/v1/pools")
}
func (s *isolatedStockReads) Jobs(ctx context.Context) ([]byte, error) {
	return s.readInventory(ctx, s.fetchJobs)
}
func (s *isolatedStockReads) Pool(ctx context.Context, pool string) ([]byte, error) {
	if !uuid.MatchString(pool) {
		return nil, ErrConfiguration
	}
	return s.read(ctx, "/api/v1/pools/"+pool)
}
