package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/jobobserver"
	"net/http"
)

type JobObserverStatusSource interface{ Latest() jobobserver.Status }

func (h *Handler) SetJobObserver(source *jobobserver.Service) {
	if source == nil {
		h.jobObserver = nil
		return
	}
	h.jobObserver = source
}
func (h *Handler) serveJobObserverStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(405)
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		w.WriteHeader(401)
		return
	}
	var empty *jobobserver.Service
	status := empty.Latest()
	state := "unconfigured"
	if h.jobObserver != nil {
		status = h.jobObserver.Latest()
		state = "awaiting_first_cycle"
		if !status.At.IsZero() {
			state = "observing"
			if status.Incomplete {
				state = "unresolved"
			}
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		State             string             `json:"state"`
		Scope             string             `json:"scope"`
		Status            jobobserver.Status `json:"status"`
		ReleaseAuthorized bool               `json:"release_authorized"`
	}{state, "positive_job_and_root_journal_only", status, false})
}
