package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"net/http"
)

func (h *Handler) SetDemandObserver(s *demandobserver.Service) { h.demandObserver = s }
func (h *Handler) serveDemandObserver(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		w.WriteHeader(401)
		return
	}
	_ = json.NewEncoder(w).Encode(h.demandObserver.Snapshot())
}
