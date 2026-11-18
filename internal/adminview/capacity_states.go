package adminview

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (h *Handler) serveCapacityStates(w http.ResponseWriter, r *http.Request) {
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
	view, err := h.store.ReadCapacityStates(ctx)
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
