package adminview

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

// A separate explicitly reviewed read action. No upload, installation or
// preparation attempt is performed by this route.
func (h *Handler) serveNativePreparationDiagnostic(w http.ResponseWriter, r *http.Request) {
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if len(h.nativePreparation) == 0 {
		fail(404, "prepared diagnostics unavailable")
		return
	}
	if !adminAuthenticated(r, h.nativePreparationHash) {
		fail(401, "separate native preparation token required")
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
		fail(413, "request too large")
		return
	}
	var req struct {
		TargetID      string `json:"target_id"`
		PreparationID string `json:"preparation_id"`
		ReviewDigest  string `json:"review_digest"`
		Confirm       bool   `json:"confirm_read"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "explicit diagnostic read approval required")
		return
	}
	target, ok := h.nativePreparation[req.TargetID]
	if !ok {
		fail(400, "unknown prepared target")
		return
	}
	report, e := target.Service.Diagnostic(r.Context(), req.PreparationID, req.ReviewDigest, target.Resolve)
	if e != nil {
		fail(409, "diagnostic unavailable, busy or reviewed preparation changed; reload before another explicit read")
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"target_id": req.TargetID, "preparation_id": req.PreparationID, "review_digest": req.ReviewDigest, "report": report})
}
