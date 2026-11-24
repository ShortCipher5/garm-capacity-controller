package adminview

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// serveReuseStatus exposes the durable per-charge reuse disposition:
// pending cleanup vs verified reusable, with the still-charged dimensions
// and hold reasons. It is read-only and offers no deletion or retry.
func (h *Handler) serveReuseStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	view, err := h.store.ReadReuseStatus(ctx)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	data, err := json.Marshal(view)
	if err != nil || len(data) > 2<<20 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write(data)
}
