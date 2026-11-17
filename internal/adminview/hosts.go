package adminview

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// enrollHostRequest is the explicit operator enrollment action. It is distinct
// from discovery: nothing here reads a backend, and discovery never enrolls.
// detected is optional; when present it is recorded as the host's last
// discovery totals with a fresh observation.
type enrollHostRequest struct {
	Name     string               `json:"name"`
	Budget   admission.Resources  `json:"budget"`
	Detected *admission.Resources `json:"detected,omitempty"`
	Mode     string               `json:"mode"`
}

// serveHosts enrolls a NEW ledger host. It requires the separately authorized
// management role: enrollment is a policy-shape write, not a probe. The
// admission store rejects duplicate names, enabled initial modes and invalid
// budgets; this handler only maps those durable outcomes.
func (h *Handler) serveHosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.management {
		fail(http.StatusNotFound, "host enrollment unavailable")
		return
	}
	if r.URL.Path != "/admin/api/v1/hosts" {
		fail(http.StatusNotFound, "host enrollment unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !adminAuthenticated(r, h.writeHash) {
		h.audit("host enrollment authentication rejected")
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 {
		fail(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	if !uniqueJSON(body) {
		fail(http.StatusBadRequest, "invalid host enrollment")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request enrollHostRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "invalid host enrollment")
		return
	}
	if err := h.store.EnrollHost(r.Context(), request.Name, request.Budget, request.Mode); err != nil {
		switch {
		case errors.Is(err, admission.ErrConflict):
			fail(http.StatusConflict, "host already enrolled")
		case errors.Is(err, admission.ErrDenied):
			fail(http.StatusBadRequest, "invalid host enrollment")
		default:
			h.audit("host enrollment outcome unavailable")
			fail(http.StatusServiceUnavailable, "enrollment outcome unavailable; refresh before retrying")
		}
		return
	}
	detectedRecorded := false
	if request.Detected != nil {
		if err := h.store.SetHostDetected(r.Context(), request.Name, request.Detected, time.Now().UTC()); err != nil {
			h.audit("enrolled host detected totals unavailable")
			fail(http.StatusServiceUnavailable, "host enrolled but detected totals unavailable")
			return
		}
		detectedRecorded = true
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"enrolled": true, "name": request.Name, "detected_recorded": detectedRecorded})
}
