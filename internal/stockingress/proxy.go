// Package stockingress exposes only stock runner/webhook endpoints. It never
// forwards administration or supplies a management credential.
package stockingress

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrConfiguration = errors.New("invalid private stock ingress configuration")
var segment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Allowed deliberately does not normalize paths. Encoded separators, dots,
// double slashes, absolute URLs and queries must not become management routes
// after a downstream router or proxy normalization step.
func Allowed(r *http.Request, controller string) bool {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.Opaque != "" || r.URL.IsAbs() || strings.ContainsAny(r.URL.Path, "%\\\x00") || strings.Contains(r.URL.Path, "//") {
		return false
	}
	p := r.URL.Path
	if strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if r.Method == http.MethodPost {
		if p == "/webhooks" || (uuid.MatchString(controller) && p == "/webhooks/"+controller) {
			return true
		}
		return p == "/api/v1/callbacks/status" || p == "/api/v1/callbacks/system-info"
	}
	if r.Method != http.MethodGet {
		return false
	}
	switch p {
	case "/api/v1/metadata/runner-metadata", "/api/v1/metadata/runner-registration-token", "/api/v1/metadata/system/service-name", "/api/v1/metadata/systemd/unit-file", "/api/v1/metadata/system/cert-bundle", "/api/v1/metadata/tools/garm-agent", "/api/v1/metadata/install-script":
		return true
	}
	if suffix, ok := strings.CutPrefix(p, "/api/v1/metadata/credentials/"); ok {
		return segment.MatchString(suffix)
	}
	if suffix, ok := strings.CutPrefix(p, "/api/v1/metadata/tools/garm-agent/"); ok {
		suffix = strings.TrimSuffix(suffix, "/download")
		n, e := strconv.ParseUint(suffix, 10, 63)
		return e == nil && n > 0 && strconv.FormatUint(n, 10) == suffix
	}
	return false
}

// New targets a literal loopback HTTP socket inside the managed namespace only.
// This handler alone is NOT exclusivity: private binding/topology must be enforced
// by the managed deployment, and arbitrary remote GARM remains read-only.
func New(upstream, controller string) (http.Handler, error) {
	return newProxy(upstream, controller, nil)
}

// NewTLS requires normal HTTPS verification to the private stock API; its
// trust is independently pinned by the caller from prepared deployment material.
func NewTLS(upstream, controller string, trust *tls.Config) (http.Handler, error) {
	if trust == nil || trust.InsecureSkipVerify || trust.RootCAs == nil {
		return nil, ErrConfiguration
	}
	return newProxy(upstream, controller, trust.Clone())
}
func newProxy(upstream, controller string, trust *tls.Config) (http.Handler, error) {
	u, e := url.Parse(upstream)
	if e != nil || (trust == nil && u.Scheme != "http" || trust != nil && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Port() == "" || (controller != "" && !uuid.MatchString(controller)) {
		return nil, ErrConfiguration
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, ErrConfiguration
	}
	transport := &http.Transport{TLSClientConfig: trust, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ForceAttemptHTTP2: false, MaxIdleConns: 32, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableCompression: true}
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(u)
		p.Out.Host = u.Host
		p.Out.Header.Del("Forwarded")
		p.Out.Header.Del("X-Forwarded-For")
		p.Out.Header.Del("X-Forwarded-Host")
		p.Out.Header.Del("X-Forwarded-Proto")
		p.Out.Header.Del("X-HTTP-Method-Override")
		p.Out.Header.Del("X-Method-Override")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}, ModifyResponse: func(r *http.Response) error {
		if r.StatusCode >= 300 && r.StatusCode < 400 {
			return ErrConfiguration
		}
		return nil
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Allowed(r, controller) {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Upgrade") != "" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
		proxy.ServeHTTP(w, r)
	}), nil
}
