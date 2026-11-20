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
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func testHTTPRoute(t *testing.T, handler http.Handler) (*httptest.Server, HTTPClientTrust, Review, hostinventory.DirFilesystemSample) {
	t.Helper()
	_, review, sample, _, _ := fixture(t)
	server, trust, _ := testsupport.NativeTLS(t, handler)
	review.PeerKeySHA256 = sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	review.ExpiresAt = time.Now().UTC().Add(time.Minute)
	client, err := x509.ParseCertificate(trust.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return server, HTTPClientTrust{TLS: trust, ClientKeySHA256: sha256.Sum256(client.RawSubjectPublicKeyInfo)}, review, sample
}

func testedChallenge(t *testing.T, r *http.Request) ([32]byte, bool) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != HTTPPath || r.URL.RawQuery != "" || r.TLS == nil ||
		len(r.TLS.VerifiedChains) == 0 || r.Header.Get("Authorization") != "" || r.Header.Get("Accept-Encoding") != "" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Cache-Control") != "no-store" {
		t.Error("unexpected or unauthenticated challenge request")
		return [32]byte{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxChallengeBody+1))
	challenge, ok := DecodeHTTPChallenge(raw)
	if err != nil || !ok {
		t.Error("unexpected challenge frame", err)
		return [32]byte{}, false
	}
	return challenge, true
}

func TestHTTPReaderFixedScopedAuthenticatedRead(t *testing.T) {
	var count atomic.Int32
	var previous atomic.Pointer[[32]byte]
	var review Review
	var trust HTTPClientTrust
	var sample hostinventory.DirFilesystemSample
	server, trust, review, sample := testHTTPRoute(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		challenge, ok := testedChallenge(t, req)
		if !ok {
			return
		}
		if len(req.TLS.PeerCertificates) != 1 && len(req.TLS.PeerCertificates) != 2 {
			t.Error("missing exact client-role certificate")
			return
		}
		if sha256.Sum256(req.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo) != trust.ClientKeySHA256 {
			t.Error("client did not present expected role key")
			return
		}
		if old := previous.Swap(&challenge); old != nil && challenge == *old {
			t.Error("attempt challenge reused")
			return
		}
		count.Add(1)
		cur := sample
		cur.ObservedAt = time.Now().UTC()
		raw, ok := encode(challenge, cur, review.DirectoryInode, review.ExpiresAt)
		if !ok {
			t.Error("fixture could not encode response")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	reader, err := NewHTTPReader(server.URL, review, trust)
	if err != nil {
		t.Fatal(err)
	}
	// Ownership initialization and route refusal must not read the endpoint.
	foreign := review
	foreign.Scope.StoragePool = "other"
	if out, err := reader.Read(t.Context(), foreign); !errors.Is(err, ErrHTTPReader) || out != (DisplayReading{}) || count.Load() != 0 {
		t.Fatal("route mismatch made a request", err)
	}
	for range 2 {
		out, err := reader.Read(t.Context(), review)
		if err != nil || out.TotalBytes != sample.TotalBytes || out.AvailableBytes != 0 ||
			out.TotalInodes != sample.TotalInodes || out.AvailableInodes != sample.AvailableInodes || out.ExpiresAt.After(review.ExpiresAt) {
			t.Fatalf("expected exact bounded display projection: %+v %v", out, err)
		}
	}
	if count.Load() != 2 {
		t.Fatal("unexpected backend requests", count.Load())
	}
}

func TestHTTPReaderRejectsRedirectWrongPeerAndUnboundedResponses(t *testing.T) {
	var otherRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { otherRequests.Add(1) }))
	defer destination.Close()
	for _, tc := range []struct {
		name     string
		handler  func(http.ResponseWriter, *http.Request)
		wrongPin bool
		hits     int32
	}{
		{"wrong pinned peer", func(http.ResponseWriter, *http.Request) {}, true, 0},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL+"/different", http.StatusTemporaryRedirect)
		}, false, 1},
		{"unexpected URI fallback refused", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, false, 1},
		{"wrong media type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "test-private-raw-body")
		}, false, 1},
		{"compressed", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(w, "not-a-sample")
		}, false, 1},
		{"oversized body", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("a", maxPayload+1))
		}, false, 1},
		{"oversized header", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Fixture-Large", strings.Repeat("a", 12<<10))
			_, _ = io.WriteString(w, "bad")
		}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server, trust, review, _ := testHTTPRoute(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				hits.Add(1)
				if _, ok := testedChallenge(t, req); ok {
					tc.handler(w, req)
				}
			}))
			if tc.wrongPin {
				review.PeerKeySHA256[0] ^= 1
			}
			reader, err := NewHTTPReader(server.URL, review, trust)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := reader.Read(t.Context(), review); got != (DisplayReading{}) || err != ErrHTTPReader ||
				hits.Load() != tc.hits || otherRequests.Load() != 0 {
				t.Fatalf("unexpected response or redirect/fallback: %+v %v, hits=%d redirect=%d", got, err, hits.Load(), otherRequests.Load())
			}
		})
	}
}

func TestHTTPReaderRejectsUnreviewedInputsAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	leave := make(chan struct{})
	defer close(leave)
	server, trust, review, _ := testHTTPRoute(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, ok := testedChallenge(t, req); ok {
			close(entered)
			// Force an actual body read to wait for cancellation. Do not hold the
			// loopback server indefinitely even if the implementation regresses.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
			case <-leave:
			}
		}
	}))
	for _, origin := range []string{
		"http://example.test", "https://user@example.test", "https://example.test/foo", "https://example.test/?",
		"https://example.test/#", "https://example.test/%2f", "https://example.test:", "https://example.test:01",
		"https://EXAMPLE.test", "https://example.test.", "https://[example.test]", "https://[fe80::1%25zone]",
	} {
		if r, err := NewHTTPReader(origin, review, trust); r != nil || err != ErrHTTPReader {
			t.Fatal("ambiguous origin accepted", origin, err)
		}
	}
	for _, dirty := range []func(*tls.Config){
		func(c *tls.Config) { c.InsecureSkipVerify = true },
		func(c *tls.Config) { c.ServerName = "other" },
		func(c *tls.Config) { c.Certificates = nil },
		func(c *tls.Config) { c.KeyLogWriter = io.Discard },
		func(c *tls.Config) {
			c.VerifyConnection = func(tls.ConnectionState) error { t.Error("used unreviewed verifier"); return nil }
		},
	} {
		copy := trust
		copy.TLS = trust.TLS.Clone()
		dirty(copy.TLS)
		if r, err := NewHTTPReader(server.URL, review, copy); r != nil || err != ErrHTTPReader {
			t.Fatal("unexpected trust options accepted", err)
		}
	}
	reader, err := NewHTTPReader(server.URL, review, trust)
	if err != nil {
		t.Fatal(err)
	}
	transport := reader.singleTransport(t.Context(), time.Now().Add(time.Second))
	if transport.Proxy != nil || !transport.DisableKeepAlives || !transport.DisableCompression || transport.DialTLSContext == nil {
		t.Fatal("unsafe alternate transport policy")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reader.Read(ctx, review)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("did not start fixed loopback request")
	}
	alias := *reader
	if got, err := alias.Read(t.Context(), review); got != (DisplayReading{}) || err != ErrHTTPReader {
		t.Fatal("same reader alias started concurrent read", err)
	}
	cancel()
	select {
	case err = <-done:
		if err != ErrHTTPReader {
			t.Fatal("canceled read returned successful or raw error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop body read")
	}
}

func TestHTTPChallengeStrictCanonicalAndBounded(t *testing.T) {
	a := [32]byte{0xaa}
	body, ok := encodeHTTPChallenge(a)
	if !ok {
		t.Fatal("encode refused challenge")
	}
	if got, ok := DecodeHTTPChallenge(body); !ok || got != a {
		t.Fatal("decode did not round-trip")
	}
	for _, raw := range [][]byte{
		nil, bytes.Repeat([]byte{'x'}, maxChallengeBody+1), append(bytes.Clone(body), ' '),
		[]byte(strings.Replace(string(body), "aa", "AA", 1)),
		[]byte(strings.Replace(string(body), "aa", "00", 1)),
		[]byte(strings.Replace(string(body), `"challenge"`, `"Challenge"`, 1)),
		[]byte(strings.Replace(string(body), `"challenge"`, `"other":0,"challenge"`, 1)),
	} {
		if got, ok := DecodeHTTPChallenge(raw); ok || got != ([32]byte{}) {
			t.Fatal("unsafe challenge accepted")
		}
	}
}

func TestHTTPReaderRequiresExactClientRoleBeforeSendingChallenge(t *testing.T) {
	server, trust, review, _ := testHTTPRoute(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request on fixture that only supplies reviewed certificates")
	}))
	for _, name := range []string{"missing checked client pin", "mismatched client pin", "same pins", "server-only EKU", "wrong private key"} {
		t.Run(name, func(t *testing.T) {
			candidate := trust
			candidate.TLS = trust.TLS.Clone()
			candidate.TLS.Certificates = append([]tls.Certificate(nil), trust.TLS.Certificates...)
			switch name {
			case "missing checked client pin":
				candidate.ClientKeySHA256 = [32]byte{}
			case "mismatched client pin":
				candidate.ClientKeySHA256[0] ^= 1
			case "same pins":
				candidate.ClientKeySHA256 = review.PeerKeySHA256
			case "server-only EKU":
				// Use a separate server role so its SPKI is independently
				// different from the expected observation server.
				other, _, _, _ := testHTTPRoute(t, http.NotFoundHandler())
				candidate.TLS.Certificates[0] = other.TLS.Certificates[0]
				candidate.ClientKeySHA256 = sha256.Sum256(other.Certificate().RawSubjectPublicKeyInfo)
			case "wrong private key":
				candidate.TLS.Certificates[0].PrivateKey = server.TLS.Certificates[0].PrivateKey
			}
			if got, err := NewHTTPReader(server.URL, review, candidate); err != ErrHTTPReader || got != nil {
				t.Fatal("non-reviewed client role accepted", err)
			}
		})
	}

	var hits atomic.Int32
	noClientAuth := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	noClientAuth.TLS = &tls.Config{
		Certificates: append([]tls.Certificate(nil), server.TLS.Certificates...),
		ClientAuth:   tls.NoClientCert, MinVersion: tls.VersionTLS13,
	}
	noClientAuth.StartTLS()
	defer noClientAuth.Close()
	reader, err := NewHTTPReader(noClientAuth.URL, review, trust)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Read(t.Context(), review); err != ErrHTTPReader || got != (DisplayReading{}) || hits.Load() != 0 {
		t.Fatalf("pinned server-auth-only peer received HTTP request or issued reading: %+v %v hits=%d", got, err, hits.Load())
	}
}
