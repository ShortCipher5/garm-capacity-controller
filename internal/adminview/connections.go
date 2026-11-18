package adminview

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

func (h *Handler) serveConnections(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	service := h.onboarding.Connections()
	if !h.management || !h.onboardingProbe || service == nil {
		fail(404, "connection management unavailable")
		return
	}
	if !adminAuthenticated(r, h.writeHash) {
		fail(401, "management token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		records, err := service.List(r.Context())
		if err != nil {
			fail(503, "connection state unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": records, "targets": service.Targets()})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16385))
	if err != nil || len(body) > 16384 {
		fail(413, "request too large")
		return
	}
	var request struct {
		Action           string                `json:"action"`
		ExpectedRevision int64                 `json:"expected_revision"`
		Connection       *admission.Connection `json:"connection,omitempty"`
		ID               string                `json:"id,omitempty"`
		ApprovedKey      string                `json:"approved_key,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if !uniqueJSON(body) || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(400, "invalid connection request")
		return
	}
	var result admission.Connection
	switch request.Action {
	case "save-draft":
		if request.Connection == nil || request.ID != "" || request.ApprovedKey != "" || request.Connection.State != "" || request.Connection.Revision != 0 {
			fail(400, "invalid draft request")
			return
		}
		result, err = service.Put(r.Context(), *request.Connection, request.ExpectedRevision)
	case "approve", "revoke":
		if request.Connection != nil {
			fail(400, "invalid transition request")
			return
		}
		state := "approved"
		if request.Action == "revoke" {
			state = "revoked"
		}
		result, err = service.Transition(r.Context(), request.ID, request.ExpectedRevision, state, request.ApprovedKey)
	default:
		fail(400, "unknown connection action")
		return
	}
	switch {
	case errors.Is(err, admission.ErrConflict):
		fail(409, "connection changed; reload and review before retrying")
	case errors.Is(err, admission.ErrDenied) || errors.Is(err, onboarding.ErrDenied):
		fail(400, "connection not permitted by deployment policy or trust review")
	case err != nil:
		fail(503, "connection outcome unknown; reload before retrying")
	default:
		_ = json.NewEncoder(w).Encode(result)
	}
}
