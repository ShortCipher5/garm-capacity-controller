package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// serveUsage returns recorded host utilization monitoring history for one
// host: GET /admin/api/v1/usage/{host}?since=<RFC3339>&limit=<n>&order=recent.
// Without order, the oldest matching rows are selected; order=recent selects
// the newest instead. Both return rows in chronological order. This is a
// read surface guarded by the read token. A host without samples yields an
// empty JSON array with 200: monitoring absence means unavailable, never a
// fabricated zero-filled series. Samples are monitoring data only and never
// influence admission or fits.
func (h *Handler) serveUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(http.StatusMethodNotAllowed, "GET required")
		return
	}
	host := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/usage/")
	if strings.Contains(host, "/") || !managementText(host, 256) {
		fail(http.StatusBadRequest, "invalid usage host")
		return
	}
	if !adminAuthenticated(r, h.tokenHash) {
		h.audit("usage history authentication rejected")
		fail(http.StatusUnauthorized, "read token rejected")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(http.StatusBadRequest, "invalid usage query")
		return
	}
	for key, entries := range values {
		if (key != "since" && key != "limit" && key != "order") || len(entries) != 1 {
			fail(http.StatusBadRequest, "invalid usage query")
			return
		}
	}
	read := h.store.UsageHistory
	if order, present := values["order"]; present {
		if order[0] != "recent" {
			fail(http.StatusBadRequest, "invalid usage order")
			return
		}
		read = h.store.RecentUsageHistory
	}
	var since time.Time
	if raw := values.Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fail(http.StatusBadRequest, "invalid since")
			return
		}
		since = parsed
	}
	limit := 0
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			fail(http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	history, err := read(ctx, host, since, limit)
	switch {
	case errors.Is(err, admission.ErrDenied):
		fail(http.StatusBadRequest, "invalid usage request")
	case err != nil:
		h.audit("usage history unavailable")
		fail(http.StatusServiceUnavailable, "usage history unavailable")
	default:
		data, err := json.Marshal(history)
		if err != nil || len(data) > 2<<20 {
			fail(http.StatusServiceUnavailable, "usage history unavailable")
			return
		}
		_, _ = w.Write(append(data, '\n'))
	}
}
