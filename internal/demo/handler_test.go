package demo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadOnlyAuthenticatedFixture(t *testing.T) {
	token := strings.Repeat("x", 32)
	h, err := NewHandler(token, fstest.MapFS{"index.html": {Data: []byte("demo")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		method string
		path   string
		auth   string
		host   string
		want   int
	}{
		{"no token", "GET", "/api/v1/overview", "", "127.0.0.1:8080", 401},
		{"wrong token", "GET", "/api/v1/overview", "Bearer wrong", "127.0.0.1:8080", 401},
		{"query token rejected", "GET", "/api/v1/overview?token=" + token, "", "127.0.0.1:8080", 401},
		{"authorized", "GET", "/api/v1/overview", "Bearer " + token, "127.0.0.1:8080", 200},
		{"ipv6 loopback", "GET", "/api/v1/overview", "Bearer " + token, "[::1]:8080", 200},
		{"no mutations", "POST", "/api/v1/overview", "Bearer " + token, "127.0.0.1:8080", 405},
		{"no preflight", "OPTIONS", "/api/v1/overview", "Bearer " + token, "127.0.0.1:8080", 405},
		{"unknown api", "GET", "/api/v1/pools", "Bearer " + token, "127.0.0.1:8080", 404},
		{"host rejected", "GET", "/api/v1/overview", "Bearer " + token, "attacker.example:8080", 403},
		{"private address rejected", "GET", "/api/v1/overview", "Bearer " + token, "10.0.0.1:8080", 403},
		{"static page", "GET", "/", "", "127.0.0.1:8080", 200},
		{"no directories", "GET", "/assets/", "", "127.0.0.1:8080", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, nil)
			r.Host = test.host
			r.Header.Set("Authorization", test.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status %d, want %d", w.Code, test.want)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("missing safety headers or unexpected CORS")
			}
			if strings.Contains(w.Body.String(), token) {
				t.Fatal("token leaked in response")
			}
			if test.path == "/api/v1/overview" && w.Code == http.StatusOK {
				var body struct {
					Mode        string `json:"mode"`
					Enforcement bool   `json:"enforcement"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Mode != "simulation" || body.Enforcement {
					t.Fatalf("unexpected live-mode response: %+v %v", body, err)
				}
			}
		})
	}
}

func TestTokenRequiredAndMissingAssets(t *testing.T) {
	if _, err := NewHandler("", fstest.MapFS{}); err == nil {
		t.Fatal("empty token accepted")
	}
	h, err := NewHandler(strings.Repeat("x", 32), fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("missing assets: %d", w.Code)
	}
}
