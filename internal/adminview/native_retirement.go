package adminview

import (
	"encoding/json"
	"net/http"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
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
	status, freshness := h.nativeRetirement.LatestWithFreshness()
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
		Freshness         string                            `json:"freshness"`
		Scope             string                            `json:"scope"`
		Status            nativecontroller.RetirementStatus `json:"status"`
		CapacityReuse     *retirementReuseExplanation       `json:"capacity_reuse,omitempty"`
		ReleaseAuthorized bool                              `json:"release_authorized"`
	}{state, freshness, "canonical_retirement_operations_only", status, explainRetirementReuse(status, freshness), false})
}
