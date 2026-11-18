package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) SetFirstboot(s *firstboot.Service, token string) error {
	b, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if s == nil || e != nil || len(b) != 32 || token != strings.ToLower(token) {
		return ErrConfiguration
	}
	for _, other := range [][32]byte{h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash, h.sshTargetsHash, h.bootstrapHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return ErrConfiguration
		}
	}
	h.firstboot = s
	h.firstbootHash = hash
	return nil
}
func (h *Handler) serveFirstboot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.firstboot == nil {
		fail(404, "isolated-listener first-time firstboot disabled")
		return
	}
	if !adminAuthenticated(r, h.firstbootHash) {
		fail(401, "separate firstboot token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		targets, e := h.firstboot.Targets(ctx)
		if e != nil {
			fail(503, "durable firstboot state unavailable")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": targets})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(body) > 4096 {
		fail(413, "request too large")
		return
	}
	var req struct {
		ID      string `json:"target_id"`
		Digest  string `json:"review_digest"`
		Request string `json:"request_id"`
		Confirm bool   `json:"confirm_install"`
		Retry   bool   `json:"retry_unknown"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if !uniqueJSON(body) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "exact reviewed installation confirmation required")
		return
	}
	op, e := h.firstboot.Start(ctx, req.ID, req.Digest, req.Request, req.Retry)
	if e != nil {
		fail(409, "review changed, operation busy, or durable authorization unavailable; reload status before retrying")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(op)
}
