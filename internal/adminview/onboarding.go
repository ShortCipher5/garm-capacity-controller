package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

// SetOnboarding is startup-only. Profiles are trusted server configuration, not
// arbitrary browser URLs/paths/credentials. This capability never enrolls hosts.
func (h *Handler) SetOnboarding(manager *onboarding.Manager, token string) error {
	allowProbe := token != ""
	if manager == nil || (allowProbe && !manager.ProbeEnabled()) {
		return ErrConfiguration
	}
	if allowProbe {
		decoded, err := hex.DecodeString(token)
		hash := sha256.Sum256([]byte("Bearer " + token))
		if err != nil || len(decoded) != 32 || token != strings.ToLower(token) || subtle.ConstantTimeCompare(h.tokenHash[:], hash[:]) == 1 {
			return ErrConfiguration
		}
		h.onboardingHash = hash
	}
	h.onboarding = manager
	h.onboardingProbe = allowProbe
	return nil
}
func (h *Handler) serveOnboarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if h.onboarding == nil {
		fail(http.StatusNotFound, "onboarding preflight unavailable")
		return
	}
	if r.URL.Path == "/admin/api/v1/onboarding" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !adminAuthenticated(r, h.tokenHash) {
			h.audit("onboarding discovery authentication rejected")
			fail(http.StatusUnauthorized, "read token rejected")
			return
		}
		view := h.onboarding.Discovery()
		view.ProbeEnabled = h.onboardingProbe && h.onboarding.ProbeEnabled()
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	if r.URL.Path != "/admin/api/v1/onboarding/probe" {
		fail(http.StatusNotFound, "onboarding preflight unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !h.onboardingProbe {
		fail(http.StatusNotFound, "onboarding probes disabled")
		return
	}
	if !adminAuthenticated(r, h.onboardingHash) {
		h.audit("onboarding probe authentication rejected")
		fail(http.StatusUnauthorized, "management token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(http.StatusForbidden, "same origin required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(http.StatusUnsupportedMediaType, "JSON required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	if err != nil || len(body) > 2048 {
		fail(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	if !uniqueJSON(body) {
		fail(http.StatusBadRequest, "invalid onboarding probe")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request onboarding.Request
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "invalid onboarding probe")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.onboarding.Probe(ctx, request)
	switch {
	case errors.Is(err, onboarding.ErrDisabled):
		fail(http.StatusNotFound, "onboarding probes disabled")
	case errors.Is(err, onboarding.ErrDenied):
		fail(http.StatusBadRequest, "invalid or unconfigured onboarding profile")
	case errors.Is(err, onboarding.ErrBusy):
		fail(http.StatusConflict, "another onboarding probe is in progress")
	case err != nil:
		h.audit("onboarding probe or audit unavailable")
		fail(http.StatusServiceUnavailable, "preflight or its audit unavailable; no enrollment or activation is implemented")
	default:
		_ = json.NewEncoder(w).Encode(result)
	}
}
