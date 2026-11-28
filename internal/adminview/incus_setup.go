package adminview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incussetup"
	"io"
	"mime"
	"net/http"
	"sort"
	"sync"
	"time"
)

type IncusSetupTarget struct {
	Profile     string
	VerifyRoute func(context.Context, string, string) bool
	Target      incussetup.Target
	Execute     func(context.Context, *incussetup.Report) (incussetup.Report, error)
}
type setupReview struct {
	r      incussetup.Report
	digest string
	until  time.Time
}
type setupHandler struct {
	hash    [32]byte
	targets map[string]IncusSetupTarget
	mu      sync.Mutex
	running map[string]bool
	reviews map[string]setupReview
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
}

func (h *Handler) SetIncusSetup(targets map[string]IncusSetupTarget, token string) (func(), error) {
	raw, e := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != token || len(targets) < 1 || len(targets) > 8 {
		return nil, ErrConfiguration
	}
	for _, other := range [][32]byte{h.tokenHash, h.writeHash, h.sshHash, h.sshTargetsHash, h.firstbootHash, h.bootstrapHash, h.rotationHash, h.onboardingHash, h.nativeAssetsHash, h.nativePlanningHash, h.nativePreparationHash} {
		if subtle.ConstantTimeCompare(hash[:], other[:]) == 1 {
			return nil, ErrConfiguration
		}
	}
	owned := map[string]IncusSetupTarget{}
	for id, t := range targets {
		if !admission.ValidOnboardingProfileID(id) || !t.Target.Valid() || t.Execute == nil {
			return nil, ErrConfiguration
		}
		owned[id] = t
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &setupHandler{hash: hash, targets: owned, running: map[string]bool{}, reviews: map[string]setupReview{}, ctx: ctx, cancel: cancel}
	h.incusSetup = s
	return func() { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock(); s.wg.Wait() }, nil
}
func (h *Handler) serveIncusSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(n int, msg string) {
		w.WriteHeader(n)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	s := h.incusSetup
	if s == nil {
		fail(404, "Incus onboarding disabled")
		return
	}
	if !adminAuthenticated(r, s.hash) {
		fail(401, "Incus setup token required")
		return
	}
	if !sameOrigin(r) {
		fail(403, "same origin required")
		return
	}
	if r.Method == "GET" {
		ids := []string{}
		for id := range s.targets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		targets := []map[string]any{}
		for _, id := range ids {
			t := s.targets[id].Target
			a, e := h.store.LatestIncusSetup(r.Context(), id)
			if e != nil {
				fail(503, "installation journal unavailable")
				return
			}
			s.mu.Lock()
			busy := s.running[id]
			s.mu.Unlock()
			targets = append(targets, map[string]any{"target_id": id, "address": t.Address, "port": t.Port, "connector_profile": s.targets[id].Profile, "admin_user": t.User, "host_key": t.HostKey, "admin_key_fingerprint": t.AdminFingerprint, "recipe_sha256": incussetup.RecipeHash(), "running": busy, "last_attempt": a})
		}
		json.NewEncoder(w).Encode(map[string]any{"targets": targets})
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		fail(415, "JSON required")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 2049))
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var q struct {
		ID      string `json:"target_id"`
		Action  string `json:"action"`
		Digest  string `json:"review_digest"`
		Confirm bool   `json:"confirm_install"`
	}
	if e != nil || len(raw) > 2048 || !uniqueJSON(raw) || d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, "invalid setup request")
		return
	}
	t, ok := s.targets[q.ID]
	if !ok {
		fail(400, "unknown setup target")
		return
	}
	s.mu.Lock()
	if s.closed || s.running[q.ID] {
		s.mu.Unlock()
		fail(409, "target busy or unavailable")
		return
	}
	if q.Action == "inspect" && !q.Confirm && q.Digest == "" {
		s.running[q.ID] = true
		delete(s.reviews, q.ID)
		s.wg.Add(1)
		s.mu.Unlock()
		defer func() { s.mu.Lock(); delete(s.running, q.ID); s.mu.Unlock(); s.wg.Done() }()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		result, e := t.Execute(ctx, nil)
		if e != nil {
			fail(503, "Inspection unavailable. Check the host pin, approved admin key, sudo, Python and local Incus socket.")
			return
		}
		digest := ""
		if result.Mutable() {
			digest = t.Target.Review(result)
			s.mu.Lock()
			s.reviews[q.ID] = setupReview{result, digest, time.Now().Add(5 * time.Minute)}
			s.mu.Unlock()
		}
		route := "not_checked"
		if result.State == "configured" && t.VerifyRoute != nil {
			route = "unavailable_or_mismatch"
			if t.VerifyRoute(ctx, result.Certificate, result.Version) {
				route = "authenticated_same_daemon"
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"target_id": q.ID, "review_digest": digest, "report": result, "connector_route": route, "connector_profile": t.Profile})
		return
	}
	review, ok := s.reviews[q.ID]
	if q.Action != "apply" || !q.Confirm || !ok || q.Digest != review.digest || !time.Now().Before(review.until) {
		s.mu.Unlock()
		fail(409, "inspect and confirm this exact fresh host plan")
		return
	}
	delete(s.reviews, q.ID)
	if h.store.BeginIncusSetup(r.Context(), q.ID, q.Digest) != nil {
		s.mu.Unlock()
		fail(409, "could not durably journal this request; no action started")
		return
	}
	s.running[q.ID] = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.running, q.ID); s.mu.Unlock(); s.wg.Done() }()
		ctx, cancel := context.WithTimeout(s.ctx, 6*time.Minute)
		defer cancel()
		res, e := t.Execute(ctx, &review.r)
		if e == nil {
			_ = h.store.FinishIncusSetup(ctx, q.ID, q.Digest, res.Outcome)
		}
	}()
	w.WriteHeader(202)
	json.NewEncoder(w).Encode(map[string]string{"target_id": q.ID, "review_digest": q.Digest, "status": "unknown"})
}
