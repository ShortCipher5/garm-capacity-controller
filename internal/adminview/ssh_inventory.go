package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// SetSSHInventory is startup-only, explicit opt-in, and independently authorized.
func (h *Handler) SetSSHInventory(service *sshinventory.Service, token string) error {
	raw, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if service == nil || e != nil || subtle.ConstantTimeCompare(hash[:], h.rotationHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.firstbootHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.bootstrapHash[:]) == 1 || len(raw) != 32 || token != strings.ToLower(token) || subtle.ConstantTimeCompare(hash[:], h.sshTargetsHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.tokenHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.writeHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.onboardingHash[:]) == 1 {
		return ErrConfiguration
	}
	h.sshInventory = service
	h.sshHash = hash
	return nil
}
func (h *Handler) serveSSHInventory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if h.sshInventory == nil {
		fail(404, "SSH inventory disabled")
		return
	}
	if !adminAuthenticated(r, h.sshHash) {
		fail(401, "SSH inventory token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		targets, e := h.sshInventory.ListTargets(ctx)
		if e != nil {
			fail(503, "SSH target registry unavailable")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": targets})
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
	body, e := io.ReadAll(io.LimitReader(r.Body, 2049))
	if e != nil || len(body) > 2048 {
		fail(413, "request too large")
		return
	}
	var request struct {
		Target   string `json:"target_id"`
		Digest   string `json:"review_digest"`
		Request  string `json:"request_id"`
		Verified bool   `json:"verified_host_key"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if !uniqueJSON(body) || d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || !request.Verified {
		fail(400, "explicit target and host-key review required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, e := h.sshInventory.Probe(ctx, request.Target, request.Digest, request.Request)
	switch {
	case errors.Is(e, sshinventory.ErrReview):
		fail(409, "SSH target changed; reload and independently review the new key")
	case errors.Is(e, sshinventory.ErrBusy):
		fail(409, "another SSH inventory probe is running")
	case e != nil:
		fail(503, "SSH inventory or its audit unavailable; no host was enrolled or activated")
	default:
		json.NewEncoder(w).Encode(result)
	}
}
