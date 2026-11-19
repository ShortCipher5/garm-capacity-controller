package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"net/http"
	"time"
)

type allocatorPlanSource interface {
	ReadPlan(context.Context) (brokerinput.ReadPlan, error)
}

// SetAllocatorPlanSource attaches a read-only source independently of the grant loop.
func (h *Handler) SetAllocatorPlanSource(source allocatorPlanSource) { h.allocatorPlan = source }

func (h *Handler) serveAllocatorPlan(w http.ResponseWriter, r *http.Request) {
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
	fail := func(reason string) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "unavailable", "reason": reason})
	}
	source := h.allocatorPlan
	if source == nil {
		source, _ = h.allocatorStatus.(allocatorPlanSource)
	}
	if source == nil {
		fail("source_not_configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	plan, err := source.ReadPlan(ctx)
	if err != nil {
		fail(brokerinput.PlanFailureReason(err))
		return
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 2<<20 {
		fail("preview_unavailable")
		return
	}
	_, _ = w.Write(raw)
}
