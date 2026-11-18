package adminview

import (
	"bytes"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/overheadrefresh"
	"io"
	"mime"
	"net/http"
)

func (h *Handler) SetOverheadRefresh(s *overheadrefresh.Service) error {
	if !h.management || s == nil || h.overheadRefresh != nil {
		return ErrConfiguration
	}
	if _, err := s.Snapshot(); err != nil {
		return ErrConfiguration
	}
	h.overheadRefresh = s
	return nil
}
func (h *Handler) serveOverheadRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.overheadRefresh == nil {
		fail(404, "owner collection is not configured")
		return
	}
	if r.Method == http.MethodGet {
		if !h.profileReadAuthenticated(r) {
			fail(401, "token rejected")
			return
		}
		v, e := h.overheadRefresh.Snapshot()
		if e != nil {
			fail(503, "durable collection status unavailable; do not assume no effect")
			return
		}
		json.NewEncoder(w).Encode(v)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	if !h.profileWriteAuthenticated(w, r) {
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 {
		fail(413, "request too large")
		return
	}
	var input struct {
		Recipe   string `json:"recipe"`
		Revision string `json:"recipe_revision"`
		Request  string `json:"request_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "only a configured recipe, revision and request ID are accepted")
		return
	}
	op, e := h.overheadRefresh.Start(input.Recipe, input.Revision, input.Request)
	if e != nil {
		fail(409, "collection not started; reload durable status before another explicit request")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(op)
}
