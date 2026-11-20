package remotedisktelemetry

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// HTTPPath is the one read-only observation exchange. POST carries a fresh
// challenge in the body, never a route, path, device or management request.
const HTTPPath = "/v1/runner-disk-observation"

const (
	httpChallengeKind = "runner-dir-disk-display-challenge/v1"
	maxChallengeBody  = 128
)

var ErrHTTPReader = errors.New("reviewed remote runner disk observation unavailable")

type httpChallenge struct {
	Kind      string `json:"kind"`
	Challenge string `json:"challenge"`
}

func encodeHTTPChallenge(challenge [32]byte) ([]byte, bool) {
	if challenge == ([32]byte{}) {
		return nil, false
	}
	raw, err := json.Marshal(httpChallenge{httpChallengeKind, hex.EncodeToString(challenge[:])})
	return raw, err == nil && len(raw) <= maxChallengeBody
}

// DecodeHTTPChallenge validates only the exact bounded request body. A future
// sender must separately authorize the actual authenticated caller and own the
// single startup descriptor, and must never sample from a request selector.
func DecodeHTTPChallenge(raw []byte) ([32]byte, bool) {
	var none [32]byte
	var msg httpChallenge
	if len(raw) == 0 || len(raw) > maxChallengeBody || json.Unmarshal(raw, &msg) != nil || msg.Kind != httpChallengeKind ||
		len(msg.Challenge) != 64 {
		return none, false
	}
	var id [32]byte
	if n, err := hex.Decode(id[:], []byte(msg.Challenge)); n != len(id) || err != nil {
		return none, false
	}
	canonical, ok := encodeHTTPChallenge(id)
	if !ok || !bytes.Equal(raw, canonical) {
		return none, false
	}
	return id, true
}

// HTTPReader binds one reviewed telemetry origin and mTLS peer, not an Incus
// management endpoint or a route-discovery transport. Only independently
// qualified read-only server authorization and existing startup TLS trust may
// be supplied. The client opens no listener and does not attach itself.
type HTTPReader struct {
	endpoint string
	address  string
	review   Review
	trust    *tls.Config
	client   tls.Certificate
	inFlight *atomic.Bool
}

// HTTPClientTrust carries an existing client-role credential and its separately
// reviewed public-key pin. The deployed server must independently enforce its
// read-only authorization; requesting a certificate does not prove that policy.
type HTTPClientTrust struct {
	TLS             *tls.Config
	ClientKeySHA256 [32]byte
}

func reviewedHTTPOrigin(origin string) (*url.URL, bool) {
	if len(origin) == 0 || len(origin) > 320 || strings.Contains(origin, "#") {
		return nil, false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.RawPath != "" || (u.Path != "" && u.Path != "/") || u.Host == "" || strings.HasSuffix(u.Host, ":") {
		return nil, false
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.String() != host {
			return nil, false
		}
	} else {
		if len(host) == 0 || len(host) > 253 {
			return nil, false
		}
		for label := range strings.SplitSeq(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, false
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return nil, false
				}
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n == 0 || n > 65535 || strconv.Itoa(n) != port {
			return nil, false
		}
	}
	// Reject alternate authority encodings or bracketed DNS names.
	hostPort := host
	if port != "" {
		hostPort = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		hostPort = "[" + host + "]"
	}
	if u.Host != hostPort {
		return nil, false
	}
	return u, true
}

// HTTPOriginIdentity does local shape validation only. All reads use a fixed
// path, so the canonical authority also identifies a duplicate read route.
// This does not resolve DNS, connect, or authenticate a peer.
func HTTPOriginIdentity(origin string) (string, bool) {
	u, ok := reviewedHTTPOrigin(origin)
	if !ok {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port), true
}

// Copy only conventional software credentials already supplied by the owner.
// In particular, RSA PKCS8 serialization may precompute on its input; do not
// pass the caller's mutable key object to it.
func cloneHTTPReaderKey(key any) (any, error) {
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		if k == nil || k.Curve == nil || k.D == nil || k.X == nil || k.Y == nil {
			return nil, ErrHTTPReader
		}
	case ed25519.PrivateKey:
		if len(k) != ed25519.PrivateKeySize {
			return nil, ErrHTTPReader
		}
	case *rsa.PrivateKey:
		if k == nil || k.N == nil || k.D == nil || k.N.BitLen() < 2048 || k.N.BitLen() > 8192 ||
			k.D.BitLen() > 8192 || len(k.Primes) < 2 || len(k.Primes) > 4 {
			return nil, ErrHTTPReader
		}
		copyKey := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: new(big.Int).Set(k.N), E: k.E}, D: new(big.Int).Set(k.D)}
		for _, prime := range k.Primes {
			if prime == nil || prime.Sign() <= 0 || prime.BitLen() > 8192 {
				return nil, ErrHTTPReader
			}
			copyKey.Primes = append(copyKey.Primes, new(big.Int).Set(prime))
		}
		key = copyKey
	default:
		return nil, ErrHTTPReader
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, ErrHTTPReader
	}
	defer clear(raw)
	return x509.ParsePKCS8PrivateKey(raw)
}

func ownHTTPReaderTrust(input HTTPClientTrust, hostname string, peer [32]byte, now time.Time) (*tls.Config, tls.Certificate, error) {
	var none tls.Certificate
	source := input.TLS
	if input.ClientKeySHA256 == ([32]byte{}) || input.ClientKeySHA256 == peer || source == nil || source.RootCAs == nil || source.InsecureSkipVerify || len(source.Certificates) != 1 ||
		source.GetClientCertificate != nil || source.VerifyConnection != nil || source.VerifyPeerCertificate != nil ||
		source.KeyLogWriter != nil || (source.ServerName != "" && source.ServerName != hostname) ||
		source.MinVersion > tls.VersionTLS13 || (source.MaxVersion != 0 && source.MaxVersion < tls.VersionTLS13) {
		return nil, none, ErrHTTPReader
	}
	cert := source.Certificates[0]
	if len(cert.Certificate) == 0 || len(cert.Certificate) > 4 {
		return nil, none, ErrHTTPReader
	}
	var owned tls.Certificate
	for _, der := range cert.Certificate {
		if len(der) == 0 || len(der) > 16<<10 {
			return nil, none, ErrHTTPReader
		}
		owned.Certificate = append(owned.Certificate, bytes.Clone(der))
	}
	// Match the existing private managed settings client's standalone owned
	// trust pattern. No loaders or generated credentials are used here.
	var err error
	owned.PrivateKey, err = cloneHTTPReaderKey(cert.PrivateKey)
	if err != nil {
		return nil, none, ErrHTTPReader
	}
	leaf, err := x509.ParseCertificate(owned.Certificate[0])
	if err != nil || leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(leaf.ExtKeyUsage) != 1 ||
		leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(leaf.UnknownExtKeyUsage) != 0 ||
		!httpRoleTime(leaf, now) || sha256.Sum256(leaf.RawSubjectPublicKeyInfo) != input.ClientKeySHA256 {
		return nil, none, ErrHTTPReader
	}
	signer, ok := owned.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, none, ErrHTTPReader
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(public, leaf.RawSubjectPublicKeyInfo) {
		return nil, none, ErrHTTPReader
	}
	owned.Leaf = leaf
	t, err := securefiles.WithServerKeyPin(&tls.Config{
		ServerName: hostname, RootCAs: source.RootCAs,
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
	}, hex.EncodeToString(peer[:]))
	if err != nil {
		return nil, none, ErrHTTPReader
	}
	return t, owned, nil
}

func httpRoleTime(leaf *x509.Certificate, now time.Time) bool {
	return leaf != nil && !now.Before(leaf.NotBefore) && now.Before(leaf.NotAfter)
}

// NewHTTPReader freezes existing reviewed trust but performs no network I/O.
// Never derive origin, review or TLS material from an observation/request.
func NewHTTPReader(origin string, review Review, trust HTTPClientTrust) (*HTTPReader, error) {
	u, ok := reviewedHTTPOrigin(origin)
	review.ExpiresAt = review.ExpiresAt.UTC()
	if !ok || !review.Scope.Valid() || review.DirectoryInode == 0 || review.PeerKeySHA256 == ([32]byte{}) ||
		!wireTime(review.ExpiresAt) || !time.Now().UTC().Before(review.ExpiresAt) {
		return nil, ErrHTTPReader
	}
	t, client, err := ownHTTPReaderTrust(trust, u.Hostname(), review.PeerKeySHA256, time.Now().UTC())
	if err != nil {
		return nil, ErrHTTPReader
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	u.Path = HTTPPath
	return &HTTPReader{
		endpoint: u.String(), address: net.JoinHostPort(u.Hostname(), port),
		review: review, trust: t, client: client, inFlight: &atomic.Bool{},
	}, nil
}

func (r *HTTPReader) singleTransport(readCtx context.Context, absolute time.Time) *http.Transport {
	var dialed atomic.Bool
	return &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10,
		DialTLSContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			current := func() bool {
				return dialCtx.Err() == nil && readCtx.Err() == nil && time.Now().UTC().Before(absolute)
			}
			if network != "tcp" || address != r.address || !current() || !dialed.CompareAndSwap(false, true) {
				return nil, ErrHTTPReader
			}
			// net/http may detach its internal dial context from a caller to
			// support pooling. This single-use dial must retain caller cutoff.
			ctx, cancel := context.WithTimeout(readCtx, 5*time.Second)
			defer cancel()
			stopDial := context.AfterFunc(dialCtx, cancel)
			defer stopDial()
			var selected atomic.Bool
			trust := r.trust.Clone()
			trust.GetClientCertificate = func(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
				if req == nil || !current() || ctx.Err() != nil || !httpRoleTime(r.client.Leaf, time.Now().UTC()) ||
					req.SupportsCertificate(&r.client) != nil || !selected.CompareAndSwap(false, true) {
					return nil, ErrHTTPReader
				}
				return &r.client, nil
			}
			dialer := tls.Dialer{Config: trust, NetDialer: &net.Dialer{Timeout: 5 * time.Second}}
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, ErrHTTPReader
			}
			// No HTTP bytes go to a server-auth-only peer. Capture selection
			// on this completed handshake, not a previous or pooled dial.
			if !selected.Load() || !current() || ctx.Err() != nil {
				_ = conn.Close()
				return nil, ErrHTTPReader
			}
			return conn, nil
		},
	}
}

// MatchesReview checks the startup-owned review without I/O. Matching does
// not authenticate a peer, observe a disk, or prove the reviewed route.
func (r *HTTPReader) MatchesReview(review Review) bool {
	review.ExpiresAt = review.ExpiresAt.UTC()
	return r != nil && r.inFlight != nil && r.trust != nil && r.review == review
}

// Read makes at most one request for this exact current reviewed route. It
// returns the bounded display record only, never an authenticated caller
// capability, a local descriptor sample or a scaler input.
func (r *HTTPReader) Read(ctx context.Context, current Review) (DisplayReading, error) {
	var none DisplayReading
	current.ExpiresAt = current.ExpiresAt.UTC()
	if ctx == nil || ctx.Err() != nil || !r.MatchesReview(current) ||
		!r.inFlight.CompareAndSwap(false, true) {
		return none, ErrHTTPReader
	}
	defer r.inFlight.Store(false)
	ctx, cancel := context.WithTimeout(ctx, maxAttempt)
	defer cancel()
	started := time.Now()
	deadline, _ := ctx.Deadline()
	absolute := earlier(deadline.UTC(), current.ExpiresAt)
	if !started.UTC().Before(absolute) || !httpRoleTime(r.client.Leaf, started.UTC()) {
		return none, ErrHTTPReader
	}
	ctx, stop := context.WithDeadline(ctx, absolute)
	defer stop()
	var challenge [32]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return none, ErrHTTPReader
	}
	attempt, ok := Begin(current, challenge, started, absolute)
	body, encoded := encodeHTTPChallenge(challenge)
	if !ok || !encoded || ctx.Err() != nil || !time.Now().UTC().Before(absolute) {
		return none, ErrHTTPReader
	}
	// A single fresh RoundTrip: no redirect handling, cookies, idle reuse,
	// replayable GetBody, proxy, compression or alternate request path.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return none, ErrHTTPReader
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	transport := r.singleTransport(ctx, absolute)
	defer transport.CloseIdleConnections()
	res, err := transport.RoundTrip(req)
	if err != nil {
		return none, ErrHTTPReader
	}
	defer res.Body.Close()
	ct := res.Header.Values("Content-Type")
	if res.StatusCode != http.StatusOK || len(ct) != 1 || ct[0] != "application/json" ||
		len(res.Header.Values("Content-Encoding")) != 0 || res.ContentLength > maxPayload ||
		res.TLS == nil || len(res.TLS.VerifiedChains) == 0 || len(res.TLS.PeerCertificates) == 0 {
		return none, ErrHTTPReader
	}
	peer := sha256.Sum256(res.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo)
	if peer != r.review.PeerKeySHA256 {
		return none, ErrHTTPReader
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxPayload+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPayload || ctx.Err() != nil {
		return none, ErrHTTPReader
	}
	out, ok := attempt.Accept(current, AuthenticatedPayload{PeerKeySHA256: peer, BodySHA256: sha256.Sum256(raw)}, raw, time.Now())
	finished := time.Now().UTC()
	if !ok || ctx.Err() != nil || !finished.Before(absolute) || !finished.Before(out.ExpiresAt) ||
		finished.Before(out.ObservedAt) || !httpRoleTime(r.client.Leaf, finished) {
		return none, ErrHTTPReader
	}
	return out, nil
}
