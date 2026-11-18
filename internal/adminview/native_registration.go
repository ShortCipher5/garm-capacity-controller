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
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassembly"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeregistration"
	"golang.org/x/crypto/ssh"
)

// ConfigureNativeRegistration is an explicit dynamic-only startup path. No
// fabricated baseline plan exists: unassembled targets remain pending config.
func (h *Handler) ConfigureNativeRegistration(catalog nativebundle.RegistrationCatalog, directory, selectionDirectory, planningToken, preparationToken string, resolvers map[string]func() (ssh.Signer, error)) error {
	if h.sshInventory == nil || h.nativePlanning != nil || h.nativeAssembly != nil || len(h.nativePreparation) != 0 || h.nativeRegistration != nil {
		return ErrConfiguration
	}
	hashes := [][32]byte{}
	for _, token := range []string{planningToken, preparationToken} {
		raw, e := hex.DecodeString(token)
		if e != nil || len(raw) != 32 || strings.ToLower(token) != token {
			return ErrConfiguration
		}
		hash := sha256.Sum256([]byte("Bearer " + token))
		for _, other := range append([][32]byte{h.nativeAssetsHash, h.tokenHash, h.writeHash, h.onboardingHash, h.sshHash, h.sshTargetsHash, h.bootstrapHash, h.firstbootHash, h.rotationHash}, hashes...) {
			if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
				return ErrConfiguration
			}
		}
		hashes = append(hashes, hash)
	}
	compiler, e := nativebundle.NewRegistrationCompiler(catalog)
	if e != nil {
		return ErrConfiguration
	}
	owned := map[string]func() (ssh.Signer, error){}
	for name := range catalog.Administrators {
		if resolvers[name] == nil {
			return ErrConfiguration
		}
		owned[name] = resolvers[name]
	}
	registry, e := nativeregistration.NewService(compiler, h.sshInventory, directory)
	if e != nil {
		return ErrConfiguration
	}
	planning, e := nativeplanning.NewDynamicService(h.sshInventory, selectionDirectory)
	if e != nil {
		return ErrConfiguration
	}
	assembly, e := nativeassembly.NewDynamicService(planning)
	if e != nil {
		return ErrConfiguration
	}
	configured, _, e := registry.Restore(context.Background())
	if e != nil {
		return ErrConfiguration
	}
	h.nativeRegistration = registry
	h.nativeRegistrationResolvers = owned
	h.nativeConfigured = map[string]nativebundle.RegistrationMetadata{}
	h.nativePlanning = planning
	h.nativePlanningHash = hashes[0]
	h.nativeAssembly = assembly
	h.nativeAssemblyTargets = map[string]bool{}
	h.nativePreparation = map[string]NativePreparationTarget{}
	h.nativePreparationHash = hashes[1]
	success := false
	defer func() {
		if !success {
			h.CloseNativePreparation()
			h.nativeRegistration = nil
			h.nativePlanning = nil
			h.nativeAssembly = nil
			h.nativeConfigured = nil
			h.nativePreparation = nil
			h.nativePlanningHash = [32]byte{}
			h.nativePreparationHash = [32]byte{}
		}
	}()
	for _, c := range configured {
		if h.installNativeConfiguration(context.Background(), c) != nil {
			return ErrConfiguration
		}
	}
	success = true
	return nil
}

// Called under nativePreparationMu during requests, or before serving at startup.
func (h *Handler) installNativeConfiguration(ctx context.Context, c nativeregistration.Configured) error {
	m := c.Metadata()
	if m.RegistrationDigest == "" || h.nativeRegistrationResolvers[m.Administrator] == nil {
		return ErrConfiguration
	}
	if old, ok := h.nativeConfigured[m.TargetID]; ok && old.RegistrationDigest != m.RegistrationDigest {
		return ErrConfiguration
	}
	if h.nativePlanning.AddBinding(ctx, m.TargetID, m.Binding) != nil || h.nativeAssembly.AddRecipe(ctx, m.TargetID, nativeassembly.Recipe{Binding: m.Binding, Build: m.Build, OutputParent: m.OutputParent}) != nil {
		return ErrConfiguration
	}
	h.nativeConfigured[m.TargetID] = m
	h.nativeAssemblyTargets[m.TargetID] = true
	// Never replace/close a live service during a read-only history refresh.
	if _, exists := h.nativePreparation[m.TargetID]; exists {
		return nil
	}
	initial, e := nativeprovision.ReadInitialPlan(m.JournalDirectory, m.Binding.PreparationID, m.Binding.NativeTarget)
	if e != nil {
		return ErrConfiguration
	}
	if initial == nil {
		return nil
	}
	if initial.RegistrationDigest != m.RegistrationDigest {
		return ErrConfiguration
	}
	completed, e := h.nativeAssembly.InspectCompleted(ctx, m.TargetID, initial.SelectionDigest)
	if e != nil || completed.Report().PlanDigest != initial.ReviewDigest {
		return ErrConfiguration
	}
	candidate, e := newInitialService(m, completed)
	if e != nil {
		return e
	}
	registered, e := candidate.RegisteredConfiguration()
	if e != nil {
		candidate.Close()
		return ErrConfiguration
	}
	if registered != nil {
		next, e := h.nativeAssembly.InspectCompleted(ctx, m.TargetID, registered.SelectionDigest)
		if e != nil || next.Report().PlanDigest != registered.ReviewDigest {
			candidate.Close()
			return ErrConfiguration
		}
		candidate.Close()
		candidate, e = newInitialService(m, next)
		if e != nil {
			return e
		}
	}
	if _, e = candidate.View(m.TargetID); e != nil {
		candidate.Close()
		return ErrConfiguration
	}
	if old, ok := h.nativePreparation[m.TargetID]; ok {
		old.Service.Close()
	}
	h.nativePreparation[m.TargetID] = NativePreparationTarget{candidate, h.nativeRegistrationResolvers[m.Administrator]}
	return nil
}
func newInitialService(m nativebundle.RegistrationMetadata, c nativeassembly.Completed) (*nativeprovision.Service, error) {
	if c.Target() != m.Binding.NativeTarget || c.Report().PreparationID != m.Binding.PreparationID {
		return nil, ErrConfiguration
	}
	b, e := nativeprovision.LoadBundle(c.BundlePath())
	if e != nil {
		return nil, e
	}
	s, e := nativeprovision.NewService(b, m.Binding.NativeTarget, m.JournalDirectory)
	if e != nil {
		b.Close()
		return nil, e
	}
	if s.ReviewDigest() != c.Report().PlanDigest {
		s.Close()
		return nil, ErrConfiguration
	}
	return s, nil
}
func (h *Handler) serveNativeRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	if h.nativeRegistration == nil {
		fail(404, "dynamic native registration disabled")
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	h.nativePreparationMu.Lock()
	defer h.nativePreparationMu.Unlock()
	if r.Method == http.MethodGet {
		configured, history, e := h.nativeRegistration.Restore(ctx)
		if e != nil {
			fail(503, "registration history unavailable; preserve identity reservations")
			return
		}
		for _, c := range configured {
			if h.installNativeConfiguration(ctx, c) != nil {
				fail(503, "registered configuration could not be restored; no upload permitted")
				return
			}
		}
		inventory, e := h.sshInventory.ListTargets(ctx)
		if e != nil {
			fail(503, "saved inventory targets unavailable")
			return
		}
		rows := []any{}
		for _, t := range inventory {
			rows = append(rows, map[string]any{"target": t, "target_digest": nativeplanning.Digest(t)})
		}
		json.NewEncoder(w).Encode(map[string]any{"catalog": h.nativeRegistration.CatalogView(), "registrations": history, "inventory_targets": rows})
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
	raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(raw) > 8192 {
		fail(413, "request too large")
		return
	}
	var req struct {
		Action             string                      `json:"action"`
		Configuration      *nativeregistration.Request `json:"configuration"`
		ReviewDigest       string                      `json:"review_digest"`
		ConfirmRegister    bool                        `json:"confirm_register"`
		TargetID           string                      `json:"target_id"`
		SelectionDigest    string                      `json:"selection_digest"`
		AssemblyDigest     string                      `json:"assembly_digest"`
		RegistrationDigest string                      `json:"registration_digest"`
		ConfirmInitialPlan bool                        `json:"confirm_initial_plan"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if !uniqueJSON(raw) || d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "exact registration action required")
		return
	}
	if req.Action == "review" || req.Action == "register" {
		if req.Configuration == nil || req.TargetID != "" || req.SelectionDigest != "" || req.AssemblyDigest != "" || req.RegistrationDigest != "" || req.ConfirmInitialPlan || (req.Action == "review" && (req.ConfirmRegister || req.ReviewDigest != "")) || (req.Action == "register" && (!req.ConfirmRegister || req.ReviewDigest == "")) {
			fail(400, "explicit exact configuration approval required")
			return
		}
		if req.Action == "review" {
			v, e := h.nativeRegistration.Review(ctx, *req.Configuration)
			if e != nil {
				fail(409, "configuration or approved catalog scope changed")
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"review": v})
			return
		}
		c, e := h.nativeRegistration.Register(ctx, *req.Configuration, req.ReviewDigest)
		if e != nil {
			fail(409, "registration rejected or outcome unknown; inspect history, do not retry or reuse identity")
			return
		}
		if h.installNativeConfiguration(ctx, c) != nil {
			fail(503, "configuration persisted but runtime registry unavailable; inspect history, no upload permitted")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": c.Metadata().TargetID, "registration_digest": c.Metadata().RegistrationDigest, "state": "registered_configuration_without_preparation_plan"})
		return
	}
	if req.Action != "initial_plan" || req.Configuration != nil || req.ReviewDigest != "" || req.ConfirmRegister || !req.ConfirmInitialPlan || req.TargetID == "" || req.SelectionDigest == "" || req.AssemblyDigest == "" || req.RegistrationDigest == "" {
		fail(400, "explicit exact initial bundle approval required")
		return
	}
	m, ok := h.nativeConfigured[req.TargetID]
	if !ok || m.RegistrationDigest != req.RegistrationDigest {
		fail(409, "registered target changed")
		return
	}
	c, e := h.nativeAssembly.InspectCompleted(ctx, req.TargetID, req.SelectionDigest)
	if e != nil || c.Report().AssemblyDigest != req.AssemblyDigest {
		fail(409, "actual completed bundle differs from approval")
		return
	}
	candidate, e := newInitialService(m, c)
	if e != nil {
		fail(409, "actual bundle unavailable")
		return
	}
	if _, e = candidate.RegisterInitialPlan(ctx, m.RegistrationDigest, req.SelectionDigest); e != nil {
		candidate.Close()
		fail(409, "initial plan rejected or uncertain; inspect history, consumed IDs cannot be replaced")
		return
	}
	if old, ok := h.nativePreparation[req.TargetID]; ok {
		old.Service.Close()
	}
	h.nativePreparation[req.TargetID] = NativePreparationTarget{candidate, h.nativeRegistrationResolvers[m.Administrator]}
	v, e := candidate.View(req.TargetID)
	if e != nil {
		fail(503, "initial plan persisted but current view unavailable")
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"target_id": req.TargetID, "assembly": c.Report(), "preparation": v, "state": "registry_reloaded_not_prepared"})
}
