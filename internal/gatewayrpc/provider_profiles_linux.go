package gatewayrpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"net/http"
	"time"
)

type profileSnapshotHandler struct {
	next       http.Handler
	store      *admission.SQLiteStore
	controller string
	leaf       [32]byte
	inflight   chan struct{}
}

func NewProfileSnapshotHandler(next http.Handler, store *admission.SQLiteStore, controller, fingerprint string) (http.Handler, error) {
	raw, e := hex.DecodeString(fingerprint)
	if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != fingerprint || next == nil || store == nil || !store.RuntimeLineageBound() || controller == "" {
		return nil, ErrConfiguration
	}
	h := &profileSnapshotHandler{next: next, store: store, controller: controller, inflight: make(chan struct{}, 4)}
	copy(h.leaf[:], raw)
	return h, nil
}
func (h *profileSnapshotHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != profilesnapshot.Path {
		h.next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(code int) { w.WriteHeader(code); w.Write([]byte("{\"error\":\"profile snapshot unavailable\"}")) }
	if r.Method != "GET" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.IsAbs() || r.URL.User != nil || r.Header.Get("Origin") != "" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		fail(403)
		return
	}
	actual := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	if subtle.ConstantTimeCompare(actual[:], h.leaf[:]) != 1 {
		fail(403)
		return
	}
	nonce := r.Header.Get(profilesnapshot.NonceHeader)
	if len(r.Header.Values(profilesnapshot.NonceHeader)) != 1 || !profilesnapshot.Hex(nonce) {
		fail(400)
		return
	}
	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		fail(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	state, e := h.store.ReadBrokerGrantState(ctx)
	if e != nil {
		fail(503)
		return
	}
	s, e := profilesnapshot.New(h.controller, nonce, h.store.RuntimeLineageFingerprint(), state, time.Now())
	if e != nil {
		fail(503)
		return
	}
	raw, e := json.Marshal(s)
	if e != nil || len(raw) > 4<<20 {
		fail(503)
		return
	}
	w.WriteHeader(200)
	w.Write(raw)
}
