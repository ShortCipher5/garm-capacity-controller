package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// SetSSHTargetManagement is a separate startup-only authority, never implied by
// the read-only inventory role. Requests cannot upload or replace credentials.
func (h *Handler) SetSSHTargetManagement(registry *sshinventory.Registry, token string) error {
	raw, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if registry == nil || e != nil || len(raw) != 32 || token != strings.ToLower(token) {
		return ErrConfiguration
	}
	for _, other := range [][32]byte{h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return ErrConfiguration
		}
	}
	h.sshTargets = registry
	h.sshTargetsHash = hash
	return nil
}
func (h *Handler) serveSSHTargets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if h.sshTargets == nil {
		fail(404, "SSH target management disabled")
		return
	}
	if !adminAuthenticated(r, h.sshTargetsHash) {
		fail(401, "SSH target management token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]any{"scopes": h.sshTargets.Scopes()})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 20001))
	if e != nil || len(body) > 20000 {
		fail(413, "request too large")
		return
	}
	var request struct {
		Action   string              `json:"action"`
		ID       string              `json:"target_id"`
		Config   sshinventory.Config `json:"config"`
		Digest   string              `json:"review_digest"`
		Request  string              `json:"request_id"`
		Verified bool                `json:"verified_host_key"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if !uniqueJSON(body) || d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "invalid target request")
		return
	}
	var target sshinventory.Target
	switch request.Action {
	case "review":
		target, e = h.sshTargets.Review(request.ID, request.Config)
	case "approve":
		if !request.Verified {
			fail(400, "independent host-key verification required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		target, e = h.sshTargets.Save(ctx, request.Request, request.ID, request.Config, request.Digest)
	default:
		fail(400, "review or approve required")
		return
	}
	if e != nil {
		fail(409, "target is outside approved scope, changed, already used, or its durable audit is unavailable")
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"target": target, "saved": request.Action == "approve"})
}
