package stockingress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const controller = "11111111-1111-4111-8111-111111111111"

func TestExactPinnedPublicRoutes(t *testing.T) {
	for _, p := range []string{"/webhooks", "/webhooks/" + controller, "/api/v1/callbacks/status", "/api/v1/callbacks/system-info"} {
		if !Allowed(httptest.NewRequest("POST", p, nil), controller) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"runner-metadata", "runner-registration-token", "system/service-name", "systemd/unit-file", "system/cert-bundle", "tools/garm-agent", "tools/garm-agent/1", "tools/garm-agent/1/download", "install-script", "credentials/runner", "credentials/credentials_rsaparams"} {
		if !Allowed(httptest.NewRequest("GET", "/api/v1/metadata/"+p, nil), controller) {
			t.Fatal(p)
		}
	}
	for _, p := range []string{"/api/v1/pools", "/api/v1/auth/login", "/api/v1/first-run", "/ui/", "/webhooks/../../api/v1/pools", "//webhooks", "/webhooks%2f..%2fapi/v1/pools", "/api/v1/metadata/credentials/..", "/api/v1/metadata/tools/garm-agent/abc", "/webhooks?target=/api/v1/pools", "/api/v1/metadata/runner-metadata?", "http://other/webhooks", "/webhooks/22222222-2222-4222-8222-222222222222"} {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "CONNECT", "OPTIONS"} {
			if Allowed(httptest.NewRequest(m, p, nil), controller) {
				t.Fatalf("allowed %s %s", m, p)
			}
		}
	}
}
func TestProxyNeverForwardsManagementOrCredentialsOfItsOwn(t *testing.T) {
	var requests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/metadata/runner-metadata" || r.Header.Get("Authorization") != "Bearer runner" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-HTTP-Method-Override") != "" {
			t.Error("request changed or authority injected", r.URL.Path, r.Header)
		}
		io.WriteString(w, "runner-result")
	}))
	defer backend.Close()
	handler, e := New(backend.URL, controller)
	if e != nil {
		t.Fatal(e)
	}
	public := httptest.NewServer(handler)
	defer public.Close()
	req, _ := http.NewRequest("GET", public.URL+"/api/v1/metadata/runner-metadata", nil)
	req.Header.Set("Authorization", "Bearer runner")
	req.Header.Set("X-Forwarded-For", "spoofed")
	req.Header.Set("X-HTTP-Method-Override", "PUT")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(raw) != "runner-result" {
		t.Fatal(res.StatusCode, string(raw))
	}
	for _, p := range []string{"/api/v1/pools", "/api/v1/auth/login", "/api/v1/first-run", "/api/v1/metadata/../pools"} {
		r, _ := http.NewRequest("POST", public.URL+p, strings.NewReader("secret"))
		result, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		result.Body.Close()
		if result.StatusCode != 404 {
			t.Fatal(p, result.StatusCode)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("denied request reached stock API")
	}
}
func TestProxyRejectsRemoteUpstreamAndRedirect(t *testing.T) {
	for _, u := range []string{"https://127.0.0.1:1", "http://localhost:1", "http://example.com:1", "http://127.0.0.1:1/path", "http://user@127.0.0.1:1", "http://127.0.0.1:1?x=y"} {
		if _, e := New(u, controller); e == nil {
			t.Fatal(u)
		}
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/api/v1/pools")
		w.WriteHeader(302)
	}))
	defer backend.Close()
	h, e := New(backend.URL, controller)
	if e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/webhooks", nil))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

// A fresh installation has no stock controller UUID before first-run. Empty
// identity permits startup without guessing one, and exposes no UUID route.
func TestPreBootstrapIngressHasNoControllerSuffix(t *testing.T) {
	if _, err := New("http://127.0.0.1:9998", ""); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/webhooks/12345678-1234-1234-1234-123456789abc", nil)
	if Allowed(r, "") {
		t.Fatal("unbound controller suffix accepted")
	}
	for _, p := range []string{"/api/v1/first-run", "/api/v1/auth/login", "/api/v1/controller"} {
		r := httptest.NewRequest(http.MethodPost, p, nil)
		if Allowed(r, "") {
			t.Fatal("bootstrap admin route exposed")
		}
	}
}
