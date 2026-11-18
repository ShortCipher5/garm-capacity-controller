// Package managedwriter exposes only journaled stock pool pauses through a
// pinned mTLS role. It never treats topology as native capacity authority.
package managedwriter

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
)

type Handler struct {
	providers    map[string]bool
	repositories map[string]bool
	pools        map[string]bool
	session      *managedgarm.Session
	manager      *garmmanagement.Manager
	leaf         [32]byte
}

func New(session *managedgarm.Session, journal *garmmanagement.Journal, clientLeaf string, providers, pools, repositories []string) (*Handler, error) {
	raw, e := hex.DecodeString(clientLeaf)
	if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != clientLeaf {
		return nil, managedgarm.ErrBoundary
	}
	remote, e := garmapi.NewManagedLocal(session, providers, pools, repositories)
	if e != nil {
		return nil, e
	}
	manager, e := garmmanagement.New(remote, journal)
	if e != nil {
		remote.Close()
		return nil, e
	}
	h := &Handler{session: session, manager: manager, providers: map[string]bool{}, repositories: map[string]bool{}, pools: map[string]bool{}}
	for _, p := range providers {
		h.providers[p] = true
	}
	for _, id := range repositories {
		h.repositories[id] = true
	}
	for _, id := range pools {
		h.pools[id] = true
	}
	copy(h.leaf[:], raw)
	return h, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(code int) {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":"managed operation unavailable; retain unresolved intent and reload status"}`)
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.URL.User != nil || r.URL.IsAbs() || r.Header.Get("Origin") != "" {
		fail(403)
		return
	}
	leaf := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	if subtle.ConstantTimeCompare(leaf[:], h.leaf[:]) != 1 {
		fail(403)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/managed/metadata/") {
		h.serveMetadata(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances" {
		h.serveInventory(w, r)
		return
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/api/v1/pools" || r.URL.Path == "/api/v1/jobs" || strings.HasPrefix(r.URL.Path, "/api/v1/pools/")) {
		h.serveDemand(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/managed/status" {
		v, e := h.manager.Status(r.Context())
		if e != nil {
			fail(503)
			return
		}
		json.NewEncoder(w).Encode(v)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/managed/settings" {
		v, e := h.manager.Snapshot(r.Context())
		if e != nil {
			fail(503)
			return
		}
		json.NewEncoder(w).Encode(v)
		return
	}
	const prefix = "/v1/managed/pools/"
	const suffix = "/pause"
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
		fail(404)
		return
	}
	pool := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 || !singleObject(raw) {
		fail(400)
		return
	}
	var input struct {
		RequestID   string `json:"request_id"`
		Expected    string `json:"expected_fingerprint"`
		Disable     bool   `json:"disable"`
		ZeroMaximum bool   `json:"zero_maximum"`
		Reason      string `json:"reason"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || (!input.Disable && !input.ZeroMaximum) {
		fail(400)
		return
	}
	if h.session.Verify(r.Context()) != nil {
		fail(503)
		return
	}
	var update garmapi.PoolUpdate
	if input.Disable {
		value := false
		update.Enabled = &value
	}
	if input.ZeroMaximum {
		value := uint64(0)
		update.MaxRunners = &value
	}
	v, e := h.manager.UpdatePool(r.Context(), input.RequestID, pool, input.Expected, update, input.Reason)
	if e != nil {
		fail(409)
		return
	}
	json.NewEncoder(w).Encode(v)
}
func singleObject(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	v, e := d.Token()
	if e != nil || v != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok || seen[key] || key != strings.ToLower(key) {
			return false
		}
		seen[key] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return false
		}
	}
	v, e = d.Token()
	return e == nil && v == json.Delim('}') && d.Decode(new(any)) == io.EOF
}
