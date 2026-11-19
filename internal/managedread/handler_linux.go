// Package managedread serves a closed read-only projection over an already
// authenticated source. It does not authenticate stock, admit a runtime, own a
// listener, or provide settings/writer capabilities. A future sealed stock
// source must enforce its own freshness, lifetime and topology at every read.
package managedread

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/ShortCipher5/garm-capacity-controller/internal/managedprojection"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

var ErrConfiguration = errors.New("managed read configuration unavailable")
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var provider = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Source has no request/URL/body transport, credential or mutation methods.
// This interface is not evidence of runtime authority. Its implementation must
// enforce authenticated reads; arbitrary implementations cannot mint any of
// the existing protected managed/native runtime types.
type Source interface {
	Identity() string
	ControllerID() string
	Instances(context.Context) ([]byte, error)
	Pools(context.Context) ([]byte, error)
	Jobs(context.Context) ([]byte, error)
	Pool(context.Context, string) ([]byte, error)
}

type Handler struct {
	revoked                        atomic.Bool
	source                         Source
	identity, controller           string
	leaf                           [32]byte
	providers, pools, repositories map[string]bool
}

func New(source Source, clientLeaf string, providers, pools, repositories []string) (*Handler, error) {
	if source == nil || !digest.MatchString(clientLeaf) {
		return nil, ErrConfiguration
	}
	h := &Handler{source: source, identity: source.Identity(), controller: source.ControllerID()}
	if !digest.MatchString(h.identity) || !uuid.MatchString(h.controller) {
		return nil, ErrConfiguration
	}
	raw, _ := hex.DecodeString(clientLeaf)
	copy(h.leaf[:], raw)
	var err error
	h.providers, err = scope(providers, provider, 256, true)
	if err != nil {
		return nil, err
	}
	h.pools, err = scope(pools, uuid, 1000, false)
	if err != nil {
		return nil, err
	}
	h.repositories, err = scope(repositories, uuid, 1000, false)
	if err != nil {
		return nil, err
	}
	return h, nil
}
func scope(values []string, grammar *regexp.Regexp, bound int, required bool) (map[string]bool, error) {
	if len(values) > bound || required && len(values) == 0 {
		return nil, ErrConfiguration
	}
	result := map[string]bool{}
	for _, v := range values {
		if !grammar.MatchString(v) || result[v] {
			return nil, ErrConfiguration
		}
		result[v] = true
	}
	return result, nil
}
func (h *Handler) sameSource() bool {
	if h.revoked.Load() {
		return false
	}
	if h.source.Identity() != h.identity || h.source.ControllerID() != h.controller {
		h.revoked.Store(true)
		return false
	}
	return !h.revoked.Load()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(code int) { w.WriteHeader(code); _, _ = w.Write([]byte(`{"error":"managed read unavailable"}`)) }
	// Deny all mutations before inspecting a body or touching the source.
	if r.Method != http.MethodGet {
		fail(http.StatusForbidden)
		return
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || r.URL == nil || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.URL.Opaque != "" || r.URL.Fragment != "" || r.URL.User != nil || r.URL.IsAbs() || r.URL.Host != "" || r.Header.Get("Origin") != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Body != nil && r.Body != http.NoBody {
		fail(http.StatusForbidden)
		return
	}
	leaf := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	if subtle.ConstantTimeCompare(leaf[:], h.leaf[:]) != 1 {
		fail(http.StatusForbidden)
		return
	}
	path := r.URL.Path
	pool := ""
	switch path {
	case "/api/v1/instances", "/api/v1/pools", "/api/v1/jobs":
	default:
		const prefix = "/api/v1/pools/"
		if !strings.HasPrefix(path, prefix) {
			fail(http.StatusNotFound)
			return
		}
		pool = strings.TrimPrefix(path, prefix)
		if !uuid.MatchString(pool) || !h.pools[pool] {
			fail(http.StatusNotFound)
			return
		}
	}
	if r.Context().Err() != nil || !h.sameSource() {
		fail(http.StatusServiceUnavailable)
		return
	}
	var raw []byte
	var err error
	switch path {
	case "/api/v1/instances":
		raw, err = h.source.Instances(r.Context())
	case "/api/v1/pools":
		raw, err = h.source.Pools(r.Context())
	case "/api/v1/jobs":
		raw, err = h.source.Jobs(r.Context())
	default:
		raw, err = h.source.Pool(r.Context(), pool)
	}
	if err != nil || r.Context().Err() != nil || !h.sameSource() {
		fail(http.StatusServiceUnavailable)
		return
	}
	if path == "/api/v1/instances" {
		raw, err = managedprojection.Instances(raw, h.providers)
	} else {
		raw, err = managedprojection.Demand(raw, path, h.providers, h.repositories)
	}
	if err != nil {
		fail(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set(managedsource.SourceHeader, h.identity)
	w.Header().Set(managedsource.ControllerHeader, h.controller)
	_, _ = w.Write(raw)
}
