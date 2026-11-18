package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"net/http"
)

func (h *Handler) SetNativeRetirement(s *nativecontroller.RetirementService) { h.nativeRetirement = s }
func (h *Handler) serveNativeRetirementStatus(w http.ResponseWriter, r *http.Request) {
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
	state := "unconfigured"
	status := h.nativeRetirement.Latest()
	if h.nativeRetirement != nil {
		state = "awaiting_first_cycle"
		if !status.At.IsZero() {
			state = "processing"
			if status.Incomplete {
				state = "unresolved"
			}
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		State             string                            `json:"state"`
		Scope             string                            `json:"scope"`
		Status            nativecontroller.RetirementStatus `json:"status"`
		ReleaseAuthorized bool                              `json:"release_authorized"`
	}{state, "canonical_retirement_operations_only", status, false})
}
