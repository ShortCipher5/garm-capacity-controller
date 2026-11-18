package adminview

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) serveFirstbootPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.firstboot == nil {
		fail(404, "firstboot diagnostics disabled")
		return
	}
	if !adminAuthenticated(r, h.firstbootHash) {
		fail(401, "separate firstboot token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(405, "POST required")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 {
		fail(413, "bounded diagnostic approval required")
		return
	}
	var req struct {
		ID      string `json:"target_id"`
		Digest  string `json:"review_digest"`
		Confirm bool   `json:"confirm_read"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if r.URL.RawQuery != "" || !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "exact reviewed read confirmation required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, e := h.firstboot.Preflight(ctx, req.ID, req.Digest)
	if e != nil {
		fail(409, "Diagnostic unavailable or review changed: reload the fixed recipe; verify the approved administrator credential, protected preinstalled diagnostic binary and its artifact pin. No installation or activation was attempted.")
		return
	}
	json.NewEncoder(w).Encode(report)
}
