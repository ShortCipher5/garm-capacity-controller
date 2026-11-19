package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassettransfer"
)

type NativeAssetsTarget struct {
	Service       *nativeassettransfer.Service
	PreparationID string
}

func (h *Handler) SetNativeAssets(targets map[string]NativeAssetsTarget, token string) error {
	raw, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if h.nativeAssets != nil || e != nil || len(raw) != 32 || token != hex.EncodeToString(raw) || len(targets) > 8 {
		return ErrConfiguration
	}
	for _, other := range [][32]byte{h.nativePreparationHash, h.nativePlanningHash, h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash, h.sshTargetsHash, h.bootstrapHash, h.firstbootHash, h.rotationHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return ErrConfiguration
		}
	}
	owned := map[string]NativeAssetsTarget{}
	ids := map[string]bool{}
	for id, t := range targets {
		p, e := hex.DecodeString(t.PreparationID)
		if id == "" || len(id) > 64 || t.Service == nil || e != nil || len(p) != 32 || hex.EncodeToString(p) != t.PreparationID {
			return ErrConfiguration
		}
		v, e := t.Service.View(id)
		if e != nil || ids[v.AssetID] {
			return ErrConfiguration
		}
		ids[v.AssetID] = true
		owned[id] = t
	}
	h.nativeAssets = owned
	h.nativeAssetsHash = hash
	return nil
}

type nativeAssetsView struct {
	nativeassettransfer.View
	PreparationID string `json:"preparation_id"`
}

func (h *Handler) boundNativeAssets(id string) (nativeAssetsView, NativePreparationTarget, bool) {
	a, ok := h.nativeAssets[id]
	if slot := h.nativeAssetSlots[id]; slot != "" {
		c, e := h.assetAssembly(id)
		if e != nil {
			return nativeAssetsView{}, NativePreparationTarget{}, false
		}
		s, e := h.nativeAssetRegistry.Restore(slot, c)
		if e != nil || s != a.Service {
			return nativeAssetsView{}, NativePreparationTarget{}, false
		}
	}
	p, pok := h.nativePreparation[id]
	if !ok || !pok || p.Service == nil || p.Resolve == nil {
		return nativeAssetsView{}, NativePreparationTarget{}, false
	}
	pv, e := p.Service.View(id)
	if e != nil {
		return nativeAssetsView{}, NativePreparationTarget{}, false
	}
	av, e := a.Service.View(id)
	if e != nil || pv.PreparationID != a.PreparationID || pv.Address != av.Address || pv.Port != av.Port || pv.AdminUser != av.AdminUser || pv.HostKeyFingerprint != av.HostKeyFingerprint || pv.AdminKeyFingerprint != av.AdminKeyFingerprint {
		return nativeAssetsView{}, NativePreparationTarget{}, false
	}
	return nativeAssetsView{av, a.PreparationID}, p, true
}
func (h *Handler) serveNativeAssets(w http.ResponseWriter, r *http.Request) {
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if len(h.nativeAssets) == 0 && h.nativeAssetRegistry == nil {
		fail(404, "native asset staging disabled")
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
		ids := []string{}
		for id := range h.nativeAssets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := []nativeAssetsView{}
		unavailable := []map[string]string{}
		for _, id := range ids {
			v, _, ok := h.boundNativeAssets(id)
			if !ok {
				unavailable = append(unavailable, map[string]string{"target_id": id, "reason": "preparation_binding_unavailable"})
				continue
			}
			rows = append(rows, v)
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": rows, "unavailable_targets": unavailable})
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
		ID            string `json:"target_id"`
		PreparationID string `json:"preparation_id"`
		Digest        string `json:"review_digest"`
		Confirm       bool   `json:"confirm_asset_staging"`
		Inspect       bool   `json:"inspect_only"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || !req.Confirm {
		fail(400, "separate exact asset staging approval required")
		return
	}
	v, p, ok := h.boundNativeAssets(req.ID)
	if !ok || req.Digest != v.ReviewDigest || req.PreparationID != v.PreparationID {
		fail(409, "approved preparation binding or asset review unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	state, e := h.nativeAssets[req.ID].Service.Start(ctx, req.Digest, req.Inspect, p.Resolve)
	if e != nil {
		cancel()
		fail(409, "busy, changed review, or consumed transfer; reload and inspect only")
		return
	}
	go func() { <-ctx.Done(); cancel() }()
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"target_id": req.ID, "preparation_id": v.PreparationID, "state": state})
}
