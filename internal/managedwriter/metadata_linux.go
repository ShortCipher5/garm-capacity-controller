package managedwriter

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

func (h *Handler) serveMetadata(w http.ResponseWriter, r *http.Request) {
	fail := func() { http.Error(w, "managed metadata unavailable", http.StatusServiceUnavailable) }
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/managed/metadata/"), "/")
	if len(parts) != 3 || !h.pools[parts[0]] {
		fail()
		return
	}
	content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || content != "application/json" {
		fail()
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 17409))
	if e != nil || len(raw) > 17408 || !singleObject(raw) {
		fail()
		return
	}
	var input struct {
		Token string `json:"instance_token"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		fail()
		return
	}
	value, e := h.session.ReadScopedMetadata(r.Context(), parts[0], parts[1], parts[2], input.Token, h.providers, h.repositories)
	if e != nil {
		fail()
		return
	}
	h.sourceHeaders(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(value)
}
