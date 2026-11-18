package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassembly"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ConfigureNativeAssembly is startup-only. Existing durable registered selections
// are re-opened as historical configuration; no discovery freshness is implied.
func (h *Handler) ConfigureNativeAssembly(recipes map[string]nativeassembly.Recipe) error {
	if len(h.nativePreparation) == 0 || h.nativePlanning == nil {
		return ErrConfiguration
	}
	assembly, e := nativeassembly.NewService(h.nativePlanning, recipes)
	if e != nil {
		return ErrConfiguration
	}
	for id := range recipes {
		if t, ok := h.nativePreparation[id]; !ok || t.Service.PreparationID() != recipes[id].Binding.PreparationID || !t.Service.MatchesTarget(recipes[id].Binding.NativeTarget) {
			return ErrConfiguration
		}
	}
	candidates := map[string]*nativeprovision.Service{}
	installed := false
	defer func() {
		if !installed {
			for _, candidate := range candidates {
				candidate.Close()
			}
		}
	}()
	for id, t := range h.nativePreparation {
		registered, e := t.Service.RegisteredConfiguration()
		if e != nil {
			return ErrConfiguration
		}
		if registered == nil {
			continue
		}
		completed, e := assembly.InspectCompleted(context.Background(), id, registered.SelectionDigest)
		if e != nil || completed.Report().PlanDigest != registered.ReviewDigest {
			return ErrConfiguration
		}
		candidate, e := h.assemblyCandidate(t, completed)
		if e != nil {
			return ErrConfiguration
		}
		if _, e = candidate.View(id); e != nil {
			candidate.Close()
			return ErrConfiguration
		}
		candidates[id] = candidate
	}
	for id, candidate := range candidates {
		t := h.nativePreparation[id]
		t.Service.Close()
		t.Service = candidate
		h.nativePreparation[id] = t
	}
	installed = true
	h.nativeAssemblyTargets = map[string]bool{}
	for id := range recipes {
		h.nativeAssemblyTargets[id] = true
	}
	h.nativeAssembly = assembly
	return nil
}
func (h *Handler) assemblyCandidate(old NativePreparationTarget, c nativeassembly.Completed) (*nativeprovision.Service, error) {
	bundle, e := nativeprovision.LoadBundle(c.BundlePath())
	if e != nil {
		return nil, e
	}
	candidate, e := old.Service.Successor(bundle)
	if e != nil {
		bundle.Close()
		return nil, e
	}
	if candidate.ReviewDigest() != c.Report().PlanDigest {
		candidate.Close()
		return nil, ErrConfiguration
	}
	return candidate, nil
}
func (h *Handler) CloseNativePreparation() {
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	for _, t := range h.nativePreparation {
		t.Service.Close()
	}
}
func (h *Handler) serveNativeAssembly(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.nativeAssembly == nil {
		fail(404, "native bundle assembly disabled")
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
		h.nativePreparationMu.Lock()
		defer h.nativePreparationMu.Unlock()
		ids := []string{}
		for id := range h.nativeAssemblyTargets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		views := []nativeprovision.View{}
		for _, id := range ids {
			v, e := h.nativePreparation[id].Service.View(id)
			if e != nil {
				fail(503, "current registered plan unavailable")
				return
			}
			views = append(views, v)
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": views})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "POST required")
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
		Action          string `json:"action"`
		TargetID        string `json:"target_id"`
		SelectionDigest string `json:"selection_digest"`
		AssemblyDigest  string `json:"assembly_digest"`
		CurrentReview   string `json:"current_review_digest"`
		ConfirmBuild    bool   `json:"confirm_build"`
		ConfirmReload   bool   `json:"confirm_reload"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "exact reviewed action required")
		return
	}
	if (req.Action == "build" && (!req.ConfirmBuild || req.ConfirmReload || req.AssemblyDigest != "" || req.CurrentReview != "")) || ((req.Action == "inspect" || req.Action == "status") && (req.ConfirmBuild || req.ConfirmReload || req.AssemblyDigest != "" || req.CurrentReview != "")) || (req.Action == "reload" && (!req.ConfirmReload || req.ConfirmBuild || req.AssemblyDigest == "" || req.CurrentReview == "")) || (req.Action != "build" && req.Action != "inspect" && req.Action != "status" && req.Action != "reload") {
		fail(400, "explicit assembly or exact registry reload approval required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if req.Action == "status" {
		status, e := h.nativeAssembly.Status(ctx, req.TargetID, req.SelectionDigest)
		if e != nil {
			fail(503, "local assembly status unavailable or inconsistent; do not infer that a build can be retried")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.TargetID, "recovery": status})
		return
	}
	var completed nativeassembly.Completed
	if req.Action == "build" {
		completed, e = h.nativeAssembly.Build(ctx, req.TargetID, req.SelectionDigest)
	} else {
		completed, e = h.nativeAssembly.InspectCompleted(ctx, req.TargetID, req.SelectionDigest)
	}
	if e != nil {
		fail(409, "assembly unavailable or outcome uncertain; inspect exact selection, never automatically rebuild partial output")
		return
	}
	if req.Action != "reload" {
		json.NewEncoder(w).Encode(map[string]any{"target_id": req.TargetID, "assembly": completed.Report()})
		return
	}
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	old, ok := h.nativePreparation[req.TargetID]
	if !ok || old.Service.ReviewDigest() != req.CurrentReview || completed.Report().AssemblyDigest != req.AssemblyDigest {
		fail(409, "registry or reviewed assembly changed; inspect and review again")
		return
	}
	candidate, e := h.assemblyCandidate(old, completed)
	if e != nil {
		fail(409, "actual bundle no longer matches review")
		return
	}
	if _, e = candidate.RegisterReviewedReplacement(ctx, old.Service, req.SelectionDigest); e != nil {
		candidate.Close()
		fail(409, "registry reload rejected or uncertain; consumed preparations cannot be replaced; reload current registry before another action")
		return
	}
	old.Service.Close()
	old.Service = candidate
	h.nativePreparation[req.TargetID] = old
	view, e := candidate.View(req.TargetID)
	if e != nil {
		fail(503, "registered configuration persisted but current view unavailable; do not upload")
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"target_id": req.TargetID, "assembly": completed.Report(), "preparation": view, "state": "registry_reloaded_not_prepared"})
}
