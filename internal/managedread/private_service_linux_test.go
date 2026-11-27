package managedread

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func TestPrivateStockServiceAcrossBothTLSBoundaries(t *testing.T) {
	var requests atomic.Int64
	fixture := &sourceFixture{}
	var unavailable atomic.Bool
	stock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-only" || r.Header.Get("X-Gateway-Only") != "" {
			t.Error("upstream credential or method boundary failed")
			w.WriteHeader(400)
			return
		}
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.URL.Path {
		case "/api/v1/controller":
			fmt.Fprintf(w, `{"controller_id":%q}`, controllerID)
		case "/api/v1/instances":
			fmt.Fprint(w, `[]`)
		case "/api/v1/pools":
			raw, _ := fixture.Pools(r.Context())
			_, _ = w.Write(raw)
		case "/api/v1/jobs":
			raw, _ := fixture.Jobs(r.Context())
			_, _ = w.Write(raw)
		case "/api/v1/pools/" + poolID:
			raw, _ := fixture.Pool(r.Context(), poolID)
			_, _ = w.Write(raw)
		default:
			t.Errorf("unexpected stock path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer stock.Close()
	stockRoots := x509.NewCertPool()
	stockRoots.AddCert(stock.Certificate())
	source, err := newIsolatedStockReads(context.Background(), stock.URL, controllerID, fingerprint(stock.Certificate().RawSubjectPublicKeyInfo), "fixture-only", &tls.Config{RootCAs: stockRoots})
	if err != nil {
		t.Fatal(err)
	}
	serverKey, clientKey, wrongKey, roots := tlsFixture(t)
	c := PrivateStockConfig{Listen: "127.0.0.1:53421", GatewayLeaf: fingerprint(clientKey.Certificate[0]), Providers: []string{"owned"}, Pools: []string{poolID}, Repositories: []string{repoID}}
	certs := &tls.Config{Certificates: []tls.Certificate{serverKey}, RootCAs: roots}
	for _, bind := range []string{"0.0.0.0:53421", "localhost:53421", "[::]:53421", "192.168.3.2:53421", "127.0.0.1:0"} {
		bad := c
		bad.Listen = bind
		if _, err := privateServer(source, bad, certs); err == nil {
			t.Fatalf("invalid bind accepted: %s", bind)
		}
	}
	srv, err := privateServer(source, c, certs)
	if err != nil {
		t.Fatal(err)
	}
	inv := httptest.NewUnstartedServer(srv.Handler)
	inv.Config = srv
	inv.TLS = srv.TLSConfig
	inv.StartTLS()
	defer inv.Close()
	request := func(cert []tls.Certificate, method, path string) (int, string) {
		t.Helper()
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: cert, MinVersion: tls.VersionTLS13}, DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr}
		req, err := http.NewRequest(method, inv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer gateway-irrelevant")
		req.Header.Set("X-Gateway-Only", "never-forward")
		res, err := client.Do(req)
		if err != nil {
			return 0, ""
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(raw)
	}
	const path = "/api/v1/instances"
	allowed := []tls.Certificate{clientKey}
	if code, _ := request(nil, "GET", path); code != 0 {
		t.Fatalf("no-certificate status %d", code)
	}
	if code, _ := request([]tls.Certificate{wrongKey}, "GET", path); code != 403 {
		t.Fatalf("wrong-leaf status %d", code)
	}
	if code, _ := request(allowed, "POST", path); code != 403 {
		t.Fatalf("write status %d", code)
	}
	if code, _ := request(allowed, "GET", "/api/v1/controller"); code != 404 {
		t.Fatalf("unlisted route status %d", code)
	}
	if requests.Load() != 1 {
		t.Fatalf("blocked calls reached stock: %d", requests.Load())
	}
	if code, body := request(allowed, "GET", path); code != 200 || body != "[]" {
		t.Fatalf("qualified read %d %s", code, body)
	}
	leaf, err := x509.ParseCertificate(serverKey.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	trust := &tls.Config{RootCAs: roots, Certificates: allowed, MinVersion: tls.VersionTLS13}
	serverPin := fingerprint(leaf.RawSubjectPublicKeyInfo)
	inventory, err := garmapi.NewManagedInventory(inv.URL, serverPin, trust, c.Providers, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer inventory.Close()
	if rows, err := inventory.Instances(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("actual gateway inventory client: %v", err)
	}
	demand, err := garmapi.NewManagedDemand(inv.URL, serverPin, controllerID, trust, c.Providers, c.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	defer demand.Close()
	observation, err := demand.Observe(context.Background())
	if err != nil || len(observation.Jobs) != 1 || observation.SourceIdentity() == "" || observation.SourceIdentity() != demand.SourceIdentity() {
		t.Fatalf("actual gateway demand client did not obtain scoped jobs and provenance: %v", err)
	}
	unavailable.Store(true)
	if code, _ := request(allowed, "GET", path); code != 503 {
		t.Fatalf("unavailable upstream status %d", code)
	}
}
