// Package demo serves a read-only synthetic fixture, never live infrastructure.
package demo

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

//go:embed fixture.json
var overview []byte

// NewHandler requires a disposable token. It is not production admin auth.
func NewHandler(token string, assets fs.FS) (http.Handler, error) {
	if len(token) < 32 {
		return nil, errors.New("a disposable demo token of at least 32 characters is required")
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "loopback host required", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only demo", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/v1/overview" {
			supplied := sha256.Sum256([]byte(r.Header.Get("Authorization")))
			if subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer realm=\"capacity-demo\"")
				http.Error(w, "demo token required", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				w.Write(overview)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			index, err := fs.ReadFile(assets, "index.html")
			if err != nil {
				http.Error(w, "build the demo web assets first", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method == http.MethodGet {
				w.Write(index)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") && !strings.Contains(r.URL.Path, "..") && !strings.HasSuffix(r.URL.Path, "/") {
			files.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}), nil
}
