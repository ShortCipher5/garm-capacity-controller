package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"net/http"
)

type AllocatorStatusSource interface{ Snapshot() brokerinput.LoopStatus }

// SetAllocatorStatusSource connects actual process activity; never an activation
// switch. Configure once before serving requests. A missing source is unconfigured.
func (h *Handler) SetAllocatorStatusSource(source AllocatorStatusSource) { h.allocatorStatus = source }
func (h *Handler) serveAllocatorStatus(w http.ResponseWriter, r *http.Request) {
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
	var empty *brokerinput.Service
	status := empty.Snapshot()
	if h.allocatorStatus != nil {
		status = h.allocatorStatus.Snapshot()
	}
	_ = json.NewEncoder(w).Encode(struct {
		Scope  string                 `json:"scope"`
		Status brokerinput.LoopStatus `json:"status"`
	}{"process-grant-loop", status})
}
