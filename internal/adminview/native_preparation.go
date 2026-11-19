package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

type NativePreparationTarget struct {
	Service *nativeprovision.Service
	Resolve func() (ssh.Signer, error)
}

func (h *Handler) SetNativePreparation(targets map[string]NativePreparationTarget, token string) error {
	b, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if e != nil || len(b) != 32 || token != strings.ToLower(token) || len(targets) < 1 || len(targets) > 8 {
		return ErrConfiguration
	}
	for _, other := range [][32]byte{h.nativeAssetsHash, h.nativePlanningHash, h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash, h.sshTargetsHash, h.bootstrapHash, h.firstbootHash, h.rotationHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return ErrConfiguration
		}
	}
	owned := map[string]NativePreparationTarget{}
	for id, t := range targets {
		if len(id) < 1 || len(id) > 64 || t.Service == nil || t.Resolve == nil {
			return ErrConfiguration
		}
		owned[id] = t
	}
	h.nativePreparation = owned
	h.nativePreparationHash = hash
	return nil
}
func (h *Handler) serveNativePreparation(w http.ResponseWriter, r *http.Request) {
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if len(h.nativePreparation) == 0 && h.nativeRegistration == nil {
		fail(404, "native preparation disabled")
		return
	}
	if !adminAuthenticated(r, h.nativePreparationHash) {
		fail(401, "separate native preparation token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		ids := []string{}
		for id := range h.nativePreparation {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		views := []nativeprovision.View{}
		for _, id := range ids {
			view, e := h.nativePreparation[id].Service.View(id)
			if e != nil {
				fail(503, "durable preparation state unavailable")
				return
			}
			views = append(views, view)
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": views})
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
	raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(raw) > 4096 {
		fail(413, "request too large")
		return
	}
	var req struct {
		ID      string `json:"target_id"`
		Digest  string `json:"review_digest"`
		Confirm bool   `json:"confirm_prepare"`
		Inspect bool   `json:"inspect_only"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "explicit reviewed target and private configuration transfer approval required")
		return
	}
	target, ok := h.nativePreparation[req.ID]
	if !ok {
		fail(400, "unknown deployment target")
		return
	}
	// A dismissed browser does not replay or cancel an already authorized upload.
	// Work is independently bounded; its durable unknown precedes this response.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	state, e := target.Service.Start(ctx, req.Digest, req.Inspect, target.Resolve)
	if e != nil {
		cancel()
		fail(409, "review changed, busy, or prior attempt consumed; reload and inspect only")
		return
	}
	go func() { <-ctx.Done(); cancel() }()
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "state": state})
}
