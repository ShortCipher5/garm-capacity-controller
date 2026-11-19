package managedwriter

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedprojection"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
	"net/http"
	"strings"
)

func (h *Handler) sourceHeaders(w http.ResponseWriter) {
	w.Header().Set(managedsource.SourceHeader, h.session.Identity())
	w.Header().Set(managedsource.ControllerHeader, h.session.ControllerID())
}
func (h *Handler) serveDemand(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	single := strings.HasPrefix(path, "/api/v1/pools/")
	if single && !h.pools[strings.TrimPrefix(path, "/api/v1/pools/")] {
		http.Error(w, "pool unavailable", 404)
		return
	}
	raw, e := h.session.ReadCollection(r.Context(), path)
	if e == nil {
		raw, e = managedprojection.Demand(raw, path, h.providers, h.repositories)
	}
	if e != nil {
		http.Error(w, "managed demand unavailable", 503)
		return
	}
	h.sourceHeaders(w)
	w.Write(raw)
}
