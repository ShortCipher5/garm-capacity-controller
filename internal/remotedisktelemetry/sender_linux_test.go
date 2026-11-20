package remotedisktelemetry

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func inputForSender(review Review, trust HTTPClientTrust, collect func(context.Context) (hostinventory.BoundDirFilesystemSample, bool)) HTTPSenderInput {
	return HTTPSenderInput{
		ClientKeySHA256: trust.ClientKeySHA256, Scope: review.Scope, DirectoryInode: review.DirectoryInode,
		ReviewExpiresAt: review.ExpiresAt, Collect: collect,
	}
}

func TestHTTPSenderWithExactAuthenticatedReader(t *testing.T) {
	var s *HTTPSender
	var rawCalls, realCalls atomic.Int32
	server, trust, review, sample := testHTTPRoute(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ServeHTTP(w, r)
	}))
	in := inputForSender(review, trust, func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		realCalls.Add(1)
		// Testing unknown without inventing an opaque same-FD observation.
		return hostinventory.BoundDirFilesystemSample{}, false
	})
	var err error
	s, err = NewHTTPSender(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewHTTPReader(server.URL, review, trust)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Read(t.Context(), review); err != ErrHTTPReader || got != (DisplayReading{}) || realCalls.Load() != 1 {
		t.Fatalf("missing local proof did not stay unavailable: %+v %v calls=%d", got, err, realCalls.Load())
	}

	// Test the complete TLS/HTTP/one-shot exchange with a synthetic counter
	// fixture, not a fabricated opaque descriptor result.
	waitForSenderSlot(t, s)
	s.cell.observe = func(ctx context.Context, id [32]byte, started time.Time) ([]byte, bool) {
		rawCalls.Add(1)
		f := sample
		f.ObservedAt = time.Now().UTC()
		if ctx.Err() != nil || !s.cell.matchObservation(f, in.DirectoryInode, in.ReviewExpiresAt, started, time.Now().UTC()) {
			return nil, false
		}
		return encode(id, f, review.DirectoryInode, review.ExpiresAt)
	}
	got, err := reader.Read(t.Context(), review)
	if err != nil || got.TotalBytes != sample.TotalBytes || got.AvailableBytes != 0 || got.AvailableInodes != sample.AvailableInodes || rawCalls.Load() != 1 {
		t.Fatalf("synthetic read exchange failed: %+v %v calls=%d", got, err, rawCalls.Load())
	}
}

func waitForSenderSlot(t *testing.T, s *HTTPSender) {
	t.Helper()
	for end := time.Now().Add(time.Second); s.cell.inUse.Load(); {
		if time.Now().After(end) {
			t.Fatal("sender slot was not released")
		}
		time.Sleep(time.Millisecond)
	}
}

func sendFixtureRequest(t *testing.T, trust *tls.Config, endpoint, method, ct string, body []byte) (int, []byte) {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: trust.Clone(), DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ct)
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

func TestHTTPSenderVerifiesSpecificPeerAndCanonicalRequestBeforeSampling(t *testing.T) {
	var collected atomic.Int32
	var s *HTTPSender
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeHTTP(w, r) })
	server, trust, review, _ := testHTTPRoute(t, handler)
	var err error
	s, err = NewHTTPSender(t.Context(), inputForSender(review, trust, func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		collected.Add(1)
		return hostinventory.BoundDirFilesystemSample{}, false
	}))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := encodeHTTPChallenge([32]byte{2})
	if !ok || len(p) != challengeFrameSize {
		t.Fatal("challenge size contract drifted", len(p))
	}
	for _, request := range []struct {
		method, path, media string
		data                []byte
	}{
		{http.MethodGet, HTTPPath, "application/json", p},
		{http.MethodPost, HTTPPath + "?selector=other", "application/json", p},
		{http.MethodPost, HTTPPath + "?", "application/json", p},
		{http.MethodPost, "/v1/%72unner-disk-observation", "application/json", p},
		{http.MethodPost, HTTPPath, "application/json; charset=utf-8", p},
		{http.MethodPost, HTTPPath, "application/json", append(bytes.Clone(p), ' ')},
		{http.MethodPost, HTTPPath, "application/json", []byte(strings.Replace(string(p), "0200", "0000", 1))},
	} {
		if status, raw := sendFixtureRequest(t, trust.TLS, server.URL+request.path, request.method, request.media, request.data); status != http.StatusBadRequest || len(raw) != 0 {
			t.Fatalf("unexpected malformed request response %d %q", status, raw)
		}
	}

	// Listener requests a certificate but does not verify it. The same
	// reviewed client key cannot produce a verified chain at this handler.
	unverified := httptest.NewUnstartedServer(handler)
	unverified.TLS = &tls.Config{
		Certificates: append([]tls.Certificate(nil), server.TLS.Certificates...),
		ClientAuth:   tls.RequestClientCert, MinVersion: tls.VersionTLS13,
	}
	unverified.StartTLS()
	defer unverified.Close()
	if status, raw := sendFixtureRequest(t, trust.TLS, unverified.URL+HTTPPath, http.MethodPost, "application/json", p); status != http.StatusForbidden || len(raw) != 0 {
		t.Fatalf("unverified client accepted %d %q", status, raw)
	}
	if collected.Load() != 0 {
		t.Fatal("failed request reached collector")
	}

	// A verified chain alone is not enough. Re-create the same synthetic
	// listener and authorize some other independently selected startup pin.
	foreign := trust
	foreign.ClientKeySHA256[0] ^= 1
	foreignSender, err := NewHTTPSender(t.Context(), inputForSender(review, foreign, func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		collected.Add(1)
		return hostinventory.BoundDirFilesystemSample{}, false
	}))
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(foreignSender)
	other.TLS = server.TLS.Clone()
	other.StartTLS()
	defer other.Close()
	if status, raw := sendFixtureRequest(t, trust.TLS, other.URL+HTTPPath, http.MethodPost, "application/json", p); status != http.StatusForbidden || len(raw) != 0 || collected.Load() != 0 {
		t.Fatalf("foreign verified client accepted %d %q", status, raw)
	}
}

type senderDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (senderDeadlineRecorder) SetReadDeadline(time.Time) error  { return nil }
func (senderDeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestHTTPSenderCanceledCollectorStillOwnsOnlySlot(t *testing.T) {
	_, trust, review, _ := testHTTPRoute(t, http.NotFoundHandler())
	entered, release, response := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	s, err := NewHTTPSender(t.Context(), inputForSender(review, trust, func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		close(entered)
		<-release // model a kernel-side operation that does not honor cancellation
		return hostinventory.BoundDirFilesystemSample{}, false
	}))
	if err != nil {
		t.Fatal(err)
	}
	makeRequest := func(ctx context.Context) *http.Request {
		raw, ok := encodeHTTPChallenge([32]byte{7})
		if !ok {
			t.Fatal("challenge refused")
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, HTTPPath, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		// Synthetic transport state here exercises only the resource lifetime;
		// previous loopback tests cover actual verified peer and headers.
		leaf, err := x509.ParseCertificate(trust.TLS.Certificates[0].Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		req.TLS = &tls.ConnectionState{
			Version: tls.VersionTLS13, HandshakeComplete: true,
			PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}},
		}
		return req
	}
	noDeadlines := httptest.NewRecorder()
	s.ServeHTTP(noDeadlines, makeRequest(t.Context()))
	if noDeadlines.Code != http.StatusServiceUnavailable || s.cell.inUse.Load() {
		t.Fatal("server without deadline support did not fail closed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := senderDeadlineRecorder{httptest.NewRecorder()}
	req := makeRequest(ctx)
	go func() {
		defer close(response)
		s.ServeHTTP(first, req)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("did not start the single sample")
	}
	cancel()
	select {
	case <-response:
	case <-time.After(time.Second):
		t.Fatal("canceled HTTP handler waited on the collector")
	}
	if first.Code != http.StatusServiceUnavailable || !s.cell.inUse.Load() {
		t.Fatal("timeout released the still-running collector")
	}
	second := senderDeadlineRecorder{httptest.NewRecorder()}
	s.ServeHTTP(second, makeRequest(t.Context()))
	if second.Code != http.StatusServiceUnavailable || second.Body.Len() != 0 || !s.cell.inUse.Load() {
		t.Fatal("second request reused blocked slot")
	}
}

func TestHTTPSenderCollectorPanicIsUnknownAndReleasesSlot(t *testing.T) {
	var calls atomic.Int32
	var sender *HTTPSender
	server, trust, review, _ := testHTTPRoute(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sender.ServeHTTP(w, r)
	}))
	var err error
	sender, err = NewHTTPSender(t.Context(), inputForSender(review, trust, func(context.Context) (hostinventory.BoundDirFilesystemSample, bool) {
		if calls.Add(1) == 1 {
			panic("test-private-collector-panic-data")
		}
		return hostinventory.BoundDirFilesystemSample{}, false
	}))
	if err != nil {
		t.Fatal(err)
	}
	for n := byte(1); n <= 2; n++ {
		raw, ok := encodeHTTPChallenge([32]byte{n})
		if !ok {
			t.Fatal("invalid test challenge")
		}
		status, response := sendFixtureRequest(t, trust.TLS, server.URL+HTTPPath, http.MethodPost, "application/json", raw)
		if status != http.StatusServiceUnavailable || len(response) != 0 || calls.Load() != int32(n) {
			t.Fatalf("worker %d did not report unknown or release for next request: status=%d calls=%d", n, status, calls.Load())
		}
		waitForSenderSlot(t, sender)
	}
}
