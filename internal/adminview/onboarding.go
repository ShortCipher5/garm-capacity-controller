package adminview

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
)

// SetOnboarding is startup-only. Profiles are trusted server configuration, not
// arbitrary browser URLs/paths/credentials. This capability never enrolls hosts.
func (h *Handler) SetOnboarding(manager *onboarding.Manager, token string) error {
	allowProbe := token != ""
	if manager == nil || (allowProbe && !manager.ProbeEnabled()) {
		return ErrConfiguration
	}
	if allowProbe {
		decoded, err := hex.DecodeString(token)
		hash := sha256.Sum256([]byte("Bearer " + token))
		if err != nil || subtle.ConstantTimeCompare(hash[:], h.firstbootHash[:]) == 1 || subtle.ConstantTimeCompare(hash[:], h.bootstrapHash[:]) == 1 || len(decoded) != 32 || token != strings.ToLower(token) || subtle.ConstantTimeCompare(h.tokenHash[:], hash[:]) == 1 || subtle.ConstantTimeCompare(h.sshHash[:], hash[:]) == 1 || subtle.ConstantTimeCompare(h.sshTargetsHash[:], hash[:]) == 1 {
			return ErrConfiguration
		}
		h.onboardingHash = hash
	}
	h.onboarding = manager
	h.onboardingProbe = allowProbe
	return nil
}
func (h *Handler) serveOnboarding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if h.onboarding == nil {
		fail(http.StatusNotFound, "onboarding preflight unavailable")
		return
	}
	if r.URL.Path == "/admin/api/v1/onboarding/connections" {
		h.serveConnections(w, r)
		return
	}
	if r.URL.Path == "/admin/api/v1/onboarding" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !adminAuthenticated(r, h.tokenHash) {
			h.audit("onboarding discovery authentication rejected")
			fail(http.StatusUnauthorized, "read token rejected")
			return
		}
		view := h.onboarding.Discovery()
		if service := h.onboarding.Connections(); service != nil {
			profiles, err := service.ApprovedProfiles(r.Context())
			if err != nil {
				fail(http.StatusServiceUnavailable, "connection profiles unavailable")
				return
			}
			view.Profiles = append(view.Profiles, profiles...)
		}
		view.ProbeEnabled = h.onboardingProbe && h.onboarding.ProbeEnabled()
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/api/v1/onboarding/profiles/") {
		h.serveOnboardingProfileDiscovery(w, r)
		return
	}
	if r.URL.Path != "/admin/api/v1/onboarding/probe" {
		fail(http.StatusNotFound, "onboarding preflight unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !h.onboardingProbe {
		fail(http.StatusNotFound, "onboarding probes disabled")
		return
	}
	if !adminAuthenticated(r, h.onboardingHash) {
		h.audit("onboarding probe authentication rejected")
		fail(http.StatusUnauthorized, "management token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(http.StatusForbidden, "same origin required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(http.StatusUnsupportedMediaType, "JSON required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2049))
	if err != nil || len(body) > 2048 {
		fail(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	if !uniqueJSON(body) {
		fail(http.StatusBadRequest, "invalid onboarding probe")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request onboarding.Request
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "invalid onboarding probe")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.onboarding.Probe(ctx, request)
	switch {
	case errors.Is(err, onboarding.ErrDisabled):
		fail(http.StatusNotFound, "onboarding probes disabled")
	case errors.Is(err, onboarding.ErrDenied):
		fail(http.StatusBadRequest, "invalid or unconfigured onboarding profile")
	case errors.Is(err, onboarding.ErrBusy):
		fail(http.StatusConflict, "another onboarding probe is in progress")
	case err != nil:
		h.audit("onboarding probe or audit unavailable")
		fail(http.StatusServiceUnavailable, "preflight or its audit unavailable; no enrollment or activation is implemented")
	default:
		_ = json.NewEncoder(w).Encode(result)
	}
}

// serveOnboardingProfileDiscovery runs read-only discovery on demand for one
// allowlisted profile. Like the probe, it requires the flag-gated management
// role and same origin; it never enrolls, activates or qualifies a backend.
func (h *Handler) serveOnboardingProfileDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.onboardingProbe {
		fail(http.StatusNotFound, "onboarding probes disabled")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(http.StatusMethodNotAllowed, "GET required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/onboarding/profiles/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "discovery" || !admission.ValidOnboardingProfileID(parts[0]) {
		fail(http.StatusNotFound, "onboarding discovery unavailable")
		return
	}
	if !adminAuthenticated(r, h.onboardingHash) {
		h.audit("onboarding discovery authentication rejected")
		fail(http.StatusUnauthorized, "management token rejected")
		return
	}
	if !sameOrigin(r) {
		fail(http.StatusForbidden, "same origin required")
		return
	}
	requestID, err := discoveryRequestID()
	if err != nil {
		h.audit("onboarding discovery correlation unavailable")
		fail(http.StatusServiceUnavailable, "discovery correlation unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.onboarding.Discover(ctx, onboarding.Request{RequestID: requestID, ProfileID: parts[0], Reason: "on-demand admin discovery view"})
	switch {
	case errors.Is(err, onboarding.ErrDisabled):
		fail(http.StatusNotFound, "onboarding probes disabled")
	case errors.Is(err, onboarding.ErrDenied):
		fail(http.StatusNotFound, "unknown onboarding profile")
	case errors.Is(err, onboarding.ErrBusy):
		fail(http.StatusConflict, "another onboarding discovery is in progress")
	case err != nil:
		h.audit("onboarding discovery or audit unavailable")
		fail(http.StatusServiceUnavailable, "discovery or its audit unavailable; no enrollment or activation is implemented")
	default:
		proposal := onboarding.ProposeBudget(result)
		profiles, profilesErr := h.store.Profiles(r.Context())
		if profilesErr != nil || len(profiles) > 512 {
			proposal.SizeSource = "unavailable"
			proposal.Sizes = []onboarding.PlanningSize{}
			proposal.Warnings = append(proposal.Warnings, "Saved runner-size profiles unavailable or exceed the 512-profile planning limit; no profile fit is proposed.")
		} else if len(profiles) > 0 {
			proposal.SizeSource = "saved-profiles"
			proposal.Sizes = []onboarding.PlanningSize{}
			for _, profile := range profiles {
				proposal.Sizes = append(proposal.Sizes, onboarding.PlanningSize{Name: profile.Name, CPU: profile.CPUMillis, RAM: profile.RAMMiB, Disk: profile.DiskMiB})
			}
		}
		result.Proposal = &proposal
		// Persist exactly the sanitized view returned to the reviewer. This is
		// enrollment provenance only, never a backend binding or health proof.
		snapshot, encodeErr := json.Marshal(result)
		var detected *admission.Resources
		if t := result.Totals; t != nil && t.Complete && t.CPUMillis != nil && t.RAMMiB != nil && t.DiskMiB != nil {
			detected = &admission.Resources{CPU: *t.CPUMillis, RAM: *t.RAMMiB, Disk: *t.DiskMiB}
		}
		if encodeErr != nil {
			fail(http.StatusServiceUnavailable, "discovery review unavailable")
			return
		}
		review, recordErr := h.store.RecordConnectionDiscoveryReview(r.Context(), requestID, parts[0], result.ObservedAt, snapshot, detected, result.ConnectionRevision)
		if recordErr != nil {
			fail(http.StatusServiceUnavailable, "discovery review unavailable; no enrollment performed")
			return
		}
		result.Review = &review
		var body bytes.Buffer
		if json.NewEncoder(&body).Encode(result) != nil || body.Len() > 2<<20 {
			fail(http.StatusServiceUnavailable, "discovery view unavailable")
			return
		}
		_, _ = w.Write(body.Bytes())
	}
}

// discoveryRequestID mints a canonical lowercase UUID to correlate one
// on-demand discovery run's audit pair; the browser cannot supply or replay it.
func discoveryRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	var text [36]byte
	hex.Encode(text[0:8], id[0:4])
	text[8] = '-'
	hex.Encode(text[9:13], id[4:6])
	text[13] = '-'
	hex.Encode(text[14:18], id[6:8])
	text[18] = '-'
	hex.Encode(text[19:23], id[8:10])
	text[23] = '-'
	hex.Encode(text[24:36], id[10:16])
	return string(text[:]), nil
}
