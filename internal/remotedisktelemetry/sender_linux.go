package remotedisktelemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

const (
	senderBudget       = 2 * time.Second
	challengeFrameSize = len(`{"kind":"`+httpChallengeKind+`","challenge":""}`) + 64
)

var ErrHTTPSender = errors.New("reviewed runner disk sender unavailable")

// HTTPSenderInput is a startup-only handoff. The collector must own one
// separately reviewed descriptor and must never obtain a path, FD, route or
// policy from a request. Counters describe that exact descriptor, not whole-
// mount headroom, future placement or the GARM route. The client public key is
// reviewed independently of the listener's verified mTLS trust chain.
type HTTPSenderInput struct {
	ClientKeySHA256 [32]byte
	Scope           hostinventory.DirFilesystemScope
	DirectoryInode  uint64
	ReviewExpiresAt time.Time
	Collect         func(context.Context) (hostinventory.BoundDirFilesystemSample, bool)
}

// HTTPSender can be mounted only on a dedicated, independently reviewed mTLS
// endpoint. It neither creates a listener nor sets system trust, credentials,
// permissions, or runner configuration. It needs standard net/http connection
// deadlines and sets them to this fixed request's absolute cutoff; do not
// mount it under middleware that hides the real TLS state or requires a
// shorter unreflected listener-only deadline.
type HTTPSender struct{ cell *httpSenderCell }

type httpSenderCell struct {
	startup context.Context
	input   HTTPSenderInput
	inUse   atomic.Bool
	observe func(context.Context, [32]byte, time.Time) ([]byte, bool)
}

func NewHTTPSender(startup context.Context, input HTTPSenderInput) (*HTTPSender, error) {
	input.ReviewExpiresAt = input.ReviewExpiresAt.UTC()
	if startup == nil || startup.Err() != nil || input.ClientKeySHA256 == ([32]byte{}) || !input.Scope.Valid() ||
		input.DirectoryInode == 0 || !wireTime(input.ReviewExpiresAt) || !time.Now().UTC().Before(input.ReviewExpiresAt) ||
		input.Collect == nil {
		return nil, ErrHTTPSender
	}
	cell := &httpSenderCell{startup: startup, input: input}
	cell.observe = cell.observeDescriptor
	return &HTTPSender{cell: cell}, nil
}

func (c *httpSenderCell) observeDescriptor(ctx context.Context, challenge [32]byte, started time.Time) ([]byte, bool) {
	observation, ok := c.input.Collect(ctx)
	if !ok || ctx.Err() != nil {
		return nil, false
	}
	sample, inode, expires, ok := observation.Evidence()
	if !ok || !c.matchObservation(sample, inode, expires, started, time.Now().UTC()) {
		return nil, false
	}
	return Encode(challenge, observation)
}

func (c *httpSenderCell) matchObservation(sample hostinventory.DirFilesystemSample, inode uint64, expiry, started, now time.Time) bool {
	at := sample.ObservedAt.UTC()
	return sample.Scope == c.input.Scope && inode == c.input.DirectoryInode &&
		expiry.UTC().Equal(c.input.ReviewExpiresAt) && now.Before(c.input.ReviewExpiresAt) &&
		at.After(started) && !at.After(now)
}

func verifiedSenderPeer(state *tls.ConnectionState, expected [32]byte, now time.Time) (*x509.Certificate, bool) {
	if state == nil || !state.HandshakeComplete || state.DidResume || state.Version != tls.VersionTLS13 ||
		len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return nil, false
	}
	leaf := state.PeerCertificates[0]
	if !httpRoleTime(leaf, now) || leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		len(leaf.UnknownExtKeyUsage) != 0 || sha256.Sum256(leaf.RawSubjectPublicKeyInfo) != expected {
		return nil, false
	}
	for _, chain := range state.VerifiedChains {
		if len(chain) != 0 && chain[0] != nil && bytes.Equal(chain[0].Raw, leaf.Raw) {
			return leaf, true
		}
	}
	return nil, false
}

func exactSenderRequest(req *http.Request) bool {
	if req.URL == nil || req.Method != http.MethodPost || req.ProtoMajor != 1 || req.ProtoMinor != 1 ||
		req.RequestURI != HTTPPath || req.URL.Path != HTTPPath || req.URL.RawPath != "" ||
		req.URL.ForceQuery || req.URL.RawQuery != "" || req.URL.Scheme != "" || req.URL.Host != "" ||
		req.URL.Opaque != "" || req.URL.User != nil || req.URL.Fragment != "" || req.Body == nil ||
		req.ContentLength != int64(challengeFrameSize) || len(req.TransferEncoding) != 0 || len(req.Trailer) != 0 {
		return false
	}
	ct := req.Header.Values("Content-Type")
	return len(ct) == 1 && ct[0] == "application/json" && len(req.Header.Values("Content-Encoding")) == 0
}

func (s *HTTPSender) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// This endpoint never reuses an authenticated connection or emits
	// diagnostic bodies. Failed requests don't ask the collector anything.
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-store")
	if s == nil || s.cell == nil || r == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	c := s.cell
	now := time.Now().UTC()
	peer, ok := verifiedSenderPeer(r.TLS, c.input.ClientKeySHA256, now)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !exactSenderRequest(r) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	deadline := earlier(earlier(now.Add(senderBudget), c.input.ReviewExpiresAt), peer.NotAfter.UTC())
	for _, parent := range []context.Context{r.Context(), c.startup} {
		if parent.Err() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if end, ok := parent.Deadline(); ok {
			deadline = earlier(deadline, end.UTC())
		}
	}
	if !now.Before(deadline) || !c.inUse.CompareAndSwap(false, true) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	delegated := false
	defer func() {
		if !delegated {
			c.inUse.Store(false)
		}
	}()
	control := http.NewResponseController(w)
	if control.SetReadDeadline(deadline) != nil || control.SetWriteDeadline(deadline) != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	stop := context.AfterFunc(c.startup, cancel)
	defer stop()
	current := func() bool {
		return ctx.Err() == nil && c.startup.Err() == nil && time.Now().UTC().Before(deadline)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(challengeFrameSize)+1))
	challenge, ok := DecodeHTTPChallenge(raw)
	if err != nil || len(raw) != challengeFrameSize || !ok || !current() {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	started := time.Now().UTC()
	if !started.Before(deadline) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// A hung collector cannot keep the HTTP handler or start unlimited work.
	// The one slot stays occupied until the existing operation actually exits;
	// timeout alone never claims that a kernel syscall has stopped.
	type reply struct {
		raw []byte
		ok  bool
	}
	done := make(chan reply, 1)
	delegated = true
	go func() {
		defer c.inUse.Store(false)
		var result reply
		completed := false
		defer func() {
			// The detached worker is not covered by net/http's handler
			// recovery. Report only unknown, never the panic or its payload.
			_ = recover()
			if !completed {
				result = reply{}
			}
			done <- result
		}()
		if !current() {
			return
		}
		result.raw, result.ok = c.observe(ctx, challenge, started)
		completed = true
	}()
	select {
	case <-ctx.Done():
		w.WriteHeader(http.StatusServiceUnavailable)
	case result := <-done:
		if !result.ok || len(result.raw) == 0 || len(result.raw) > maxPayload || !current() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(result.raw)))
		_, _ = w.Write(result.raw)
	}
}
