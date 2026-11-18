package managedsource

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceDescriptorActualTLSNonceAndNamespaceDial(t *testing.T) {
	client, root := clientIdentity(t)
	controller := "11111111-1111-1111-1111-111111111111"
	source := strings.Repeat("a", 64)
	mode := "valid"
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != SourcePath || r.Method != "GET" || r.Header.Get("Authorization") != "" || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unexpected descriptor request")
		}
		v := struct {
			Version    string    `json:"version"`
			Nonce      string    `json:"nonce"`
			Controller string    `json:"controller"`
			Source     string    `json:"source"`
			ObservedAt time.Time `json:"observed_at"`
		}{"managed-source/v1", r.Header.Get(NonceHeader), controller, source, time.Now().UTC()}
		switch mode {
		case "nonce":
			v.Nonce = strings.Repeat("b", 64)
		case "stale":
			v.ObservedAt = time.Now().Add(-time.Minute)
		case "body-source":
			v.Source = strings.Repeat("b", 64)
		}
		w.Header().Set(SourceHeader, source)
		w.Header().Set(ControllerHeader, controller)
		raw, _ := json.Marshal(v)
		w.Write(raw)
	}))
	clients := x509.NewCertPool()
	clients.AddCert(root)
	s.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clients}
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	pin := sha256.Sum256(s.Certificate().RawSubjectPublicKeyInfo)
	trust := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{client}}
	calls := 0
	c, e := NewWithDialer(s.URL, hex.EncodeToString(pin[:]), controller, trust, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseIdleConnections()
	if c.Identity() != "" {
		t.Fatal("config minted identity")
	}
	o, e := c.ObserveSource(context.Background())
	if e != nil || !o.Valid() || o.SourceIdentity() != c.Identity() || o.ControllerID() != controller || o.StockSourceIdentity() != source || calls == 0 {
		t.Fatal("missing real source", e)
	}
	for _, m := range []string{"nonce", "stale", "body-source"} {
		mode = m
		o, e = c.ObserveSource(context.Background())
		if e == nil || o.Valid() {
			t.Fatal("invalid descriptor accepted", m)
		}
	}
	bad, e := NewWithDialer(s.URL, strings.Repeat("0", 64), controller, trust, func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	if e != nil {
		t.Fatal(e)
	}
	defer bad.CloseIdleConnections()
	if o, e = bad.ObserveSource(context.Background()); e == nil || o.Valid() {
		t.Fatal("dial hook bypassed TLS pin")
	}
}

func TestNamespaceDialCannotReplaceOwnedTLSValidation(t *testing.T) {
	client, root := clientIdentity(t)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	for _, kind := range []string{"connection", "peer", "dynamic-client", "old-tls"} {
		trust := &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{client}}
		switch kind {
		case "connection":
			trust.VerifyConnection = func(tls.ConnectionState) error { return nil }
		case "peer":
			trust.VerifyPeerCertificate = func([][]byte, [][]*x509.Certificate) error { return nil }
		case "dynamic-client":
			trust.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &client, nil }
		case "old-tls":
			trust.MaxVersion = tls.VersionTLS12
		}
		if _, e := NewWithDialer("https://127.0.0.1:9445", strings.Repeat("a", 64), "11111111-1111-1111-1111-111111111111", trust, (&net.Dialer{}).DialContext); e == nil {
			t.Fatal("mutable/unsupported TLS authority accepted", kind)
		}
	}
}
