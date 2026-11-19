package managedwriter

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedprojection"
	"net/http"
)

func (h *Handler) serveInventory(w http.ResponseWriter, r *http.Request) {
	raw, err := h.session.ReadInstances(r.Context())
	if err == nil {
		raw, err = managedprojection.Instances(raw, h.providers)
	}
	if err != nil {
		http.Error(w, "managed inventory unavailable", http.StatusServiceUnavailable)
		return
	}
	h.sourceHeaders(w)
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}
