package managedwriter

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsettings"
	"net/http"
)

func (h *Handler) serveHistory(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || h.session == nil {
		http.Error(w, "managed history unavailable", 503)
		return
	}
	pools := []string{}
	for id := range h.pools {
		pools = append(pools, id)
	}
	rows, e := h.manager.RecentScopedIntents(r.Context(), 26, pools, nil)
	if e != nil {
		http.Error(w, "managed history unavailable", 503)
		return
	}
	view, e := projectHistory(rows, h.pools, h.providers)
	if e != nil {
		http.Error(w, "managed history unavailable", 503)
		return
	}
	h.sourceHeaders(w)
	json.NewEncoder(w).Encode(view)
}
func projectHistory(rows []garmmanagement.Intent, pools, providers map[string]bool) (managedsettings.History, error) {
	out := managedsettings.History{Events: []managedsettings.HistoryEvent{}}
	total := 64
	for _, v := range rows {
		if v.Kind != "pool" || !pools[v.Target] {
			return out, managedgarm.ErrBoundary
		}
		e := managedsettings.HistoryEvent{RequestID: v.RequestID, Kind: v.Kind, Target: v.Target, State: v.State, Reason: v.Reason, CreatedAt: v.CreatedAt}
		if json.Unmarshal(v.Before, &e.Before) != nil || json.Unmarshal(v.Desired, &e.Desired) != nil {
			return out, managedgarm.ErrBoundary
		}
		if len(v.Result) > 0 {
			e.Result = &garmapi.ManagedPool{}
			if json.Unmarshal(v.Result, e.Result) != nil {
				return out, managedgarm.ErrBoundary
			}
		}
		if !managedsettings.ValidHistoryEvent(e) {
			return out, managedgarm.ErrBoundary
		}
		if !providers[e.Before.ProviderName] || !providers[e.Desired.ProviderName] || e.Result != nil && !providers[e.Result.ProviderName] {
			out.Truncated = true
			continue
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return out, managedgarm.ErrBoundary
		}
		if len(out.Events) == 25 || len(raw)+1 > (512<<10)-total {
			out.Truncated = true
			break
		}
		total += len(raw) + 1
		out.Events = append(out.Events, e)
	}
	return out, nil
}
