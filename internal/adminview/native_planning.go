package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (h *Handler) ConfigureNativePlanning(bindings map[string]nativeplanning.Binding, directory, token string) error {
	raw, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if e != nil || len(raw) != 32 || token != strings.ToLower(token) || h.sshInventory == nil {
		return ErrConfiguration
	}
	for _, other := range [][32]byte{h.nativeAssetsHash, h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash, h.sshTargetsHash, h.bootstrapHash, h.firstbootHash, h.rotationHash, h.nativePreparationHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return ErrConfiguration
		}
	}
	service, e := nativeplanning.NewService(h.sshInventory, bindings, directory)
	if e != nil {
		return ErrConfiguration
	}
	h.nativePlanning = service
	h.nativePlanningHash = hash
	return nil
}
func (h *Handler) serveNativePlanning(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.nativePlanning == nil {
		fail(404, "native discovery planning disabled")
		return
	}
	if !adminAuthenticated(r, h.nativePlanningHash) {
		fail(401, "separate native planning token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		bindings := h.nativePlanning.Bindings()
		ids := []string{}
		for id := range bindings {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := []any{}
		for _, id := range ids {
			rows = append(rows, map[string]any{"target_id": id, "binding": bindings[id], "binding_digest": nativeplanning.Digest(bindings[id])})
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": rows})
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
	raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(raw) > 8192 {
		fail(413, "request too large")
		return
	}
	var req struct {
		SelectionDigest string                 `json:"selection_digest,omitempty"`
		DiskMiB         int64                  `json:"disk_mib,omitempty"`
		Action          string                 `json:"action"`
		ID              string                 `json:"target_id"`
		BindingDigest   string                 `json:"binding_digest,omitempty"`
		RequestID       string                 `json:"request_id,omitempty"`
		ReviewDigest    string                 `json:"review_digest,omitempty"`
		Choice          *nativeplanning.Choice `json:"choice,omitempty"`
		ConfirmRead     bool                   `json:"confirm_read,omitempty"`
		ConfirmSave     bool                   `json:"confirm_save,omitempty"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "exact reviewed action required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if req.Action == "mix-proposal" && req.SelectionDigest != "" && req.DiskMiB > 0 && !req.ConfirmRead && !req.ConfirmSave && req.Choice == nil && req.ReviewDigest == "" && req.BindingDigest == "" && req.RequestID == "" {
		p, e := h.nativePlanning.ProposeMix(ctx, req.ID, req.SelectionDigest, req.DiskMiB)
		if e != nil {
			fail(409, "saved selection expired, changed or too small; no mix or profiles saved")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "proposal": p})
		return
	}
	if req.SelectionDigest != "" || req.DiskMiB != 0 {
		fail(400, "mix inputs only allowed for explicit mix proposal")
		return
	}
	if req.Action == "history" && !req.ConfirmRead && !req.ConfirmSave && req.Choice == nil && req.ReviewDigest == "" && req.BindingDigest == "" && req.RequestID == "" {
		saved, e := h.nativePlanning.SavedSelections(req.ID)
		if e != nil {
			fail(503, "saved selection history unavailable")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "saved": saved})
		return
	}
	if req.Action == "probe" && req.ConfirmRead && !req.ConfirmSave && req.Choice == nil && req.ReviewDigest == "" {
		p, e := h.nativePlanning.Probe(ctx, req.ID, req.BindingDigest, req.RequestID)
		if e != nil {
			fail(409, "discovery unavailable or binding changed; no configuration saved")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "binding_digest": req.BindingDigest, "proposal": p})
		return
	}
	if req.Action == "save" && req.ConfirmSave && !req.ConfirmRead && req.Choice != nil && req.BindingDigest == "" && req.RequestID == "" {
		saved, e := h.nativePlanning.Save(ctx, req.ID, req.ReviewDigest, *req.Choice)
		if e != nil {
			fail(409, "save outcome unavailable: review may be expired or a file may have been saved; load saved selections before repeating")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "review_digest": req.ReviewDigest, "saved": saved})
		return
	}
	fail(400, "explicit read or save confirmation required")
}
