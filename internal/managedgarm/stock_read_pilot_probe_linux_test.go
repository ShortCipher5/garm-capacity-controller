//go:build stock_read_pilot_harness

package managedgarm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedread"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

type stockPilotEmptySource struct{ calls atomic.Int32 }

func (s *stockPilotEmptySource) Identity() string     { return strings.Repeat("a", 64) }
func (s *stockPilotEmptySource) ControllerID() string { return "11111111-1111-4111-8111-111111111111" }
func (s *stockPilotEmptySource) Instances(context.Context) ([]byte, error) {
	s.calls.Add(1)
	return []byte(`[]`), nil
}
func (s *stockPilotEmptySource) Pools(context.Context) ([]byte, error) {
	s.calls.Add(1)
	return []byte(`[]`), nil
}
func (s *stockPilotEmptySource) Jobs(context.Context) ([]byte, error) {
	s.calls.Add(1)
	return []byte(`[]`), nil
}
func (s *stockPilotEmptySource) Pool(context.Context, string) ([]byte, error) {
	return nil, ErrBoundary
}

// Test-only closed probe. No URL/path/namespace selector or evidence report.
// Caller must already own a fixed local TLS transport. Future namespace use
// requires separately reviewed same-listener custody, not a caller-supplied FD.
func stockPilotProbeReads(ctx context.Context, c *http.Client, source, controller string) error {
	if ctx == nil || ctx.Err() != nil || c == nil || !digest.MatchString(source) || !stockUUID(controller) {
		return ErrBoundary
	}
	for _, path := range []string{"instances", "pools", "jobs"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://127.0.0.1:9445/api/v1/"+path, nil)
		res, e := c.Do(req)
		if e != nil {
			return ErrBoundary
		}
		raw, re := io.ReadAll(io.LimitReader(res.Body, 4097))
		res.Body.Close()
		var rows []json.RawMessage
		if re != nil || len(raw) > 4096 || res.StatusCode != 200 || res.Header.Get(managedsource.SourceHeader) != source || res.Header.Get(managedsource.ControllerHeader) != controller || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) != 0 {
			return ErrBoundary
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://127.0.0.1:9445/api/v1/instances", nil)
	res, e := c.Do(req)
	if e != nil {
		return ErrBoundary
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		return ErrBoundary
	}
	return nil
}
func stockPilotProbeClient(t *testing.T, r stockPilotTLSRoles, cert, key []byte, pin string) *http.Client {
	t.Helper()
	ca, e := x509.ParseCertificate(r.caDER)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || stockBindingDigest(cs.PeerCertificates[0].Raw) != pin {
			return ErrBoundary
		}
		return nil
	}}
	if cert != nil {
		pair, e := tls.X509KeyPair(cert, key)
		if e != nil {
			t.Fatal(e)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	tr := &http.Transport{TLSClientConfig: config, Proxy: nil, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrBoundary }}
}
func TestStockPilotDedicatedRolesAndRealLoopbackReadProbe(t *testing.T) {
	_, auth, stock := serveMaterialFixture(t)
	_, _, gateway := serveMaterialFixture(t)
	_, _, ingress := serveMaterialFixture(t)
	roles, e := prepareStockPilotTLSRoles(time.Now(), stock, gateway, ingress)
	if e != nil {
		t.Fatal(e)
	}
	defer roles.clear()
	if _, e := prepareStockPilotTLSRoles(time.Now(), stock, stock, ingress); e == nil {
		t.Fatal("shared source role accepted")
	}
	material, e := roles.serveMaterial(auth)
	if e != nil {
		t.Fatal(e)
	}
	source := &stockPilotEmptySource{}
	handler, e := managedread.New(source, material.review.ClientSHA256, []string{"capacity"}, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:9445")
	if e != nil {
		t.Fatal("fixed disposable loopback unavailable", e)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, material.tls)) }()
	t.Cleanup(func() { server.Close(); <-done })
	good := stockPilotProbeClient(t, roles, roles.clientPEM, roles.clientKey, material.review.ServerSHA256)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stockPilotProbeReads(ctx, good, source.Identity(), source.ControllerID()) != nil || source.calls.Load() != 3 {
		t.Fatal("actual fixed reads/denied write")
	}
	for _, which := range []string{"wrong-leaf", "no-client", "wrong-server-pin"} {
		t.Run(which, func(t *testing.T) {
			cert, key, pin := roles.wrongPEM, roles.wrongKey, material.review.ServerSHA256
			if which == "no-client" {
				cert, key = nil, nil
			}
			if which == "wrong-server-pin" {
				cert, key = roles.clientPEM, roles.clientKey
				pin = strings.Repeat("b", 64)
			}
			c := stockPilotProbeClient(t, roles, cert, key, pin)
			res, e := c.Get("https://127.0.0.1:9445/api/v1/instances")
			if res != nil {
				res.Body.Close()
			}
			if e == nil || res != nil {
				t.Fatal("TLS denial replaced by HTTP response")
			}
			if source.calls.Load() != 3 {
				t.Fatal("denied peer reached source")
			}
		})
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if stockPilotProbeReads(canceled, good, source.Identity(), source.ControllerID()) == nil || source.calls.Load() != 3 {
		t.Fatal("canceled probe read")
	}
	if stockPilotProbeReads(ctx, good, strings.Repeat("f", 64), source.ControllerID()) == nil {
		t.Fatal("wrong provenance accepted")
	}
}
