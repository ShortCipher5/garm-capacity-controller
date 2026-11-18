package adminview

import (
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"io"
	"mime"
	"net/http"
	"strings"
)

func (h *Handler) serveBrokerPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	switch r.Method {
	case http.MethodGet:
		if !h.profileReadAuthenticated(r) {
			fail(401, "token rejected")
			return
		}
		p, err := h.store.BrokerPolicy(r.Context())
		if err != nil {
			fail(503, "advisory policy unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"policy": p, "advisory_only": true, "writes_enabled": h.management})
	case http.MethodPut:
		if !h.profileWriteAuthenticated(w, r) {
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			fail(415, "JSON required")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 262145))
		if err != nil || len(body) > 262144 {
			fail(413, "request too large")
			return
		}
		if !uniqueJSON(body) {
			fail(400, "invalid advisory policy")
			return
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		var p admission.BrokerPolicy
		if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF {
			fail(400, "invalid advisory policy")
			return
		}
		err = h.store.ReplaceBrokerPolicy(r.Context(), p)
		if errors.Is(err, admission.ErrBrokerConflict) {
			fail(409, "policy changed; refresh and review")
			return
		}
		if errors.Is(err, admission.ErrDenied) {
			fail(400, "invalid advisory policy")
			return
		}
		if err != nil {
			fail(503, "save outcome unavailable; read policy before another edit")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"saved": true, "revision": p.Revision + 1, "advisory_only": true})
	default:
		w.Header().Set("Allow", "GET, PUT")
		fail(405, "GET or PUT required")
	}
}
