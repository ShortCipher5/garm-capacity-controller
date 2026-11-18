package managedwriter

import (
	"bytes"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"io"
	"mime"
	"net/http"
	"strings"
)

// Invoked only after the Handler's exact pinned controller mTLS authorization.
func (h *Handler) serveMaximumReduction(w http.ResponseWriter, r *http.Request) {
	fail := func(code int) {
		w.WriteHeader(code)
		io.WriteString(w, `{"error":"maximum reduction unavailable; preserve unresolved intent and reload status"}`)
	}
	pool := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/managed/pools/"), "/reduce-maximum")
	if !h.pools[pool] {
		fail(403)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 || !singleObject(raw) {
		fail(400)
		return
	}
	var input struct {
		RequestID string  `json:"request_id"`
		Expected  string  `json:"expected_fingerprint"`
		Maximum   *uint64 `json:"maximum"`
		Reason    string  `json:"reason"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Maximum == nil || *input.Maximum == 0 || *input.Maximum > 1000000 {
		fail(400)
		return
	}
	if h.session == nil || h.manager == nil || h.session.Verify(r.Context()) != nil {
		fail(503)
		return
	}
	// The existing manager persists the exact request/precondition before PUT,
	// preserves possible-send failures as UNKNOWN, and never reconstructs retry.
	// Transport re-reads current maximum while holding its exclusive session lock.
	result, e := h.manager.UpdatePool(r.Context(), input.RequestID, pool, input.Expected, garmapi.PoolUpdate{MaxRunners: input.Maximum}, input.Reason)
	if e != nil {
		fail(409)
		return
	}
	h.sourceHeaders(w)
	json.NewEncoder(w).Encode(result)
}
