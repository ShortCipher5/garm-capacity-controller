package adminview

import (
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"io"
	"mime"
	"net/http"
	"strings"
)

func (h *Handler) serveRunnerMix(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	switch r.Method {
	case http.MethodGet:
		if !h.profileReadAuthenticated(r) {
			fail(http.StatusUnauthorized, "token rejected")
			return
		}
		host := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/runner-mix/")
		if host == r.URL.Path || host == "" || strings.Contains(host, "/") {
			fail(400, "one host required")
			return
		}
		view, err := h.store.ReadRunnerMix(r.Context(), host)
		if err != nil {
			fail(503, "mix unavailable; reload before applying")
			return
		}
		_ = json.NewEncoder(w).Encode(view)
	case http.MethodPost:
		if r.URL.Path != "/admin/api/v1/runner-mix" {
			fail(404, "unknown mix route")
			return
		}
		if !h.profileWriteAuthenticated(w, r) {
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || r.URL.RawQuery != "" {
			fail(400, "JSON body without query required")
			return
		}
		var input struct {
			ExpectedSequence int64               `json:"expected_sequence"`
			Mix              admission.RunnerMix `json:"mix"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF {
			fail(400, "invalid mix review")
			return
		}
		err = h.store.ApplyRunnerMix(r.Context(), input.ExpectedSequence, input.Mix)
		if errors.Is(err, admission.ErrRunnerMixConflict) {
			fail(409, "review changed; reload profiles and mix")
			return
		}
		if errors.Is(err, admission.ErrDenied) {
			fail(400, "invalid mix configuration")
			return
		}
		if err != nil {
			fail(503, "save outcome uncertain; read the saved mix before retrying")
			return
		}
		// Return the exact committed revision, not a later concurrent read.
		_ = json.NewEncoder(w).Encode(map[string]any{"host": input.Mix.Host, "revision": input.Mix.Revision + 1, "state": "saved_profiles_and_desired_mix_not_mapped"})
	default:
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
	}
}
