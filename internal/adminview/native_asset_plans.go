package adminview

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassembly"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassettransfer"
)

func (h *Handler) assetAssembly(id string) (nativeassembly.Completed, error) {
	p, ok := h.nativePreparation[id]
	if !ok || p.Service == nil || p.Resolve == nil || h.nativeAssembly == nil {
		return nativeassembly.Completed{}, ErrConfiguration
	}
	reg, e := p.Service.RegisteredConfiguration()
	if e != nil || reg == nil || p.Service.ReviewDigest() != reg.ReviewDigest {
		return nativeassembly.Completed{}, ErrConfiguration
	}
	c, e := h.nativeAssembly.InspectCompleted(context.Background(), id, reg.SelectionDigest)
	if e != nil || c.Report().PlanDigest != reg.ReviewDigest || c.Report().PreparationID != p.Service.PreparationID() || !p.Service.MatchesTarget(c.Target()) {
		return nativeassembly.Completed{}, ErrConfiguration
	}
	return c, nil
}
func (h *Handler) SetNativeAssetRegistry(reg *nativeassettransfer.Registry) error {
	if reg == nil || h.nativeAssetRegistry != nil || h.nativeAssets == nil || len(reg.Slots())+len(h.nativeAssets) > 8 {
		return ErrConfiguration
	}
	ids := map[string]bool{}
	for id, t := range h.nativeAssets {
		v, e := t.Service.View(id)
		if e != nil {
			return ErrConfiguration
		}
		ids[v.AssetID] = true
	}
	for _, id := range reg.AssetIDs() {
		if ids[id] {
			return ErrConfiguration
		}
	}
	if reg.AnchorCatalog() != nil {
		return ErrConfiguration
	}
	h.nativeAssetSlots = map[string]string{}
	for id := range h.nativePreparation {
		c, e := h.assetAssembly(id)
		if e != nil {
			continue
		}
		for _, slot := range reg.Slots() {
			s, e := reg.Restore(slot, c)
			if e != nil {
				continue
			}
			if _, exists := h.nativeAssets[id]; exists {
				return ErrConfiguration
			}
			h.nativeAssets[id] = NativeAssetsTarget{Service: s, PreparationID: c.Report().PreparationID}
			h.nativeAssetSlots[id] = slot
		}
	}
	h.nativeAssetRegistry = reg
	return nil
}
func (h *Handler) serveNativeAssetPlans(w http.ResponseWriter, r *http.Request) {
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.nativeAssetRegistry == nil {
		fail(404, "asset plan registration disabled")
		return
	}
	if !adminAuthenticated(r, h.nativeAssetsHash) {
		fail(401, "separate asset staging token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == http.MethodGet {
		rows := []nativeassettransfer.RegistrationReview{}
		slots := []map[string]string{}
		completed := map[string]nativeassembly.Completed{}
		ids := []string{}
		for id := range h.nativePreparation {
			if _, exists := h.nativeAssets[id]; exists {
				continue
			}
			c, e := h.assetAssembly(id)
			if e == nil {
				completed[id] = c
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, slot := range h.nativeAssetRegistry.Slots() {
			state := h.nativeAssetRegistry.State(slot)
			slots = append(slots, map[string]string{"slot": slot, "state": state})
			if state != "available" {
				continue
			}
			for _, id := range ids {
				v, e := h.nativeAssetRegistry.Review(slot, completed[id])
				if e == nil {
					rows = append(rows, v)
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"plans": rows, "slots": slots, "activation_authorized": false})
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
		Slot          string `json:"slot"`
		TargetID      string `json:"target_id"`
		PreparationID string `json:"preparation_id"`
		ReviewDigest  string `json:"review_digest"`
		Confirm       bool   `json:"confirm_asset_plan_registration"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "explicit local asset plan registration approval required")
		return
	}
	if _, exists := h.nativeAssets[req.TargetID]; exists {
		fail(409, "asset target already configured")
		return
	}
	c, e := h.assetAssembly(req.TargetID)
	if e != nil || c.Report().PreparationID != req.PreparationID {
		fail(409, "completed current assembly required")
		return
	}
	s, e := h.nativeAssetRegistry.Register(req.Slot, c, req.ReviewDigest)
	if e != nil {
		fail(409, "review changed or slot consumed; reload without retrying unknown registration")
		return
	}
	h.nativeAssets[req.TargetID] = NativeAssetsTarget{Service: s, PreparationID: req.PreparationID}
	h.nativeAssetSlots[req.TargetID] = req.Slot
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"slot": req.Slot, "review_digest": req.ReviewDigest, "target_id": req.TargetID, "preparation_id": req.PreparationID, "state": "registered_not_uploaded", "activation_authorized": false})
}
