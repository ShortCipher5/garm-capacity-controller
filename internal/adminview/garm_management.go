package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// GarmOverview contains only scoped sanitized settings and journal status, never
// provider specs, credentials, runner bootstrap, raw jobs or raw API responses.
type GarmChangeEvent struct {
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"`
	Target    string          `json:"target"`
	State     string          `json:"state"`
	Reason    string          `json:"reason"`
	CreatedAt string          `json:"created_at"`
	Before    json.RawMessage `json:"before"`
	Desired   json.RawMessage `json:"desired"`
	Result    json.RawMessage `json:"result,omitempty"`
}
type GarmOverview struct {
	HistoryTruncated  bool                       `json:"history_truncated"`
	History           []GarmChangeEvent          `json:"history"`
	SettingsAvailable bool                       `json:"settings_available"`
	Settings          garmapi.ManagementSnapshot `json:"settings"`
	WritesEnabled     bool                       `json:"writes_enabled"`
	WriteBlocked      bool                       `json:"write_blocked"`
	Pending           *GarmPending               `json:"pending,omitempty"`
}
type GarmPending struct {
	RequestID string    `json:"request_id"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	StartedAt time.Time `json:"started_at"`
}

// GarmManager must durably journal intent/audit before a remote mutation and
// block ambiguous or interrupted writes across restarts. The raw API client
// deliberately does not satisfy this interface. v0.2.1 offers no external CAS.
type GarmManager interface {
	Snapshot(context.Context) (GarmOverview, error)
	UpdatePool(context.Context, string, string, string, garmapi.PoolUpdate, string) (garmapi.ManagedPool, error)
	UpdateRepositoryBalancer(context.Context, string, string, string, string, string) (garmapi.ManagedRepository, error)
}

type GarmChange struct {
	RequestID   string              `json:"request_id"`
	Target      string              `json:"target"`
	Fingerprint string              `json:"fingerprint"`
	Reason      string              `json:"reason"`
	Pool        *garmapi.PoolUpdate `json:"pool,omitempty"`
	Balancer    string              `json:"balancer,omitempty"`
}

// SetGarmManager is startup-only. A separate opt-in controls remote writes even
// when local ledger policy writes are enabled. Discovery still needs read auth.
func (h *Handler) SetGarmManager(manager GarmManager, writes bool) error {
	if manager == nil || (writes && !h.management) {
		return ErrConfiguration
	}
	h.garm = manager
	h.garmWrites = writes
	h.remoteFlight = make(chan struct{}, 1)
	return nil
}
func adminAuthenticated(r *http.Request, expected [32]byte) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	presented := sha256.Sum256([]byte(values[0]))
	return subtle.ConstantTimeCompare(expected[:], presented[:]) == 1
}
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) > 1 {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host != r.Host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return false
		}
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || site == "none"
}
func managementText(value string, max int) bool {
	if len(value) > max || strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
func (h *Handler) serveGarm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if h.garm == nil {
		fail(http.StatusNotFound, "Garm management unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.URL.Path == "/admin/api/v1/garm" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !adminAuthenticated(r, h.tokenHash) {
			h.audit("Garm discovery authentication rejected")
			fail(http.StatusUnauthorized, "read token rejected")
			return
		}
		view, err := h.garm.Snapshot(ctx)
		if err != nil {
			h.audit("Garm management snapshot unavailable")
			fail(http.StatusServiceUnavailable, "Garm settings unavailable")
			return
		}
		view.WritesEnabled = view.WritesEnabled && h.garmWrites && h.management
		data, err := json.Marshal(view)
		if err != nil || len(data) > 2<<20 {
			fail(http.StatusServiceUnavailable, "Garm settings unavailable")
			return
		}
		_, _ = w.Write(append(data, '\n'))
		return
	}
	if r.URL.Path != "/admin/api/v1/garm/pool" && r.URL.Path != "/admin/api/v1/garm/repository" {
		fail(http.StatusNotFound, "Garm management unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !h.management || !h.garmWrites {
		fail(http.StatusNotFound, "Garm writes disabled")
		return
	}
	if !adminAuthenticated(r, h.writeHash) {
		h.audit("Garm management authentication rejected")
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 {
		fail(http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	if !uniqueJSON(body) {
		fail(http.StatusBadRequest, "invalid Garm settings change")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var change GarmChange
	if decoder.Decode(&change) != nil || decoder.Decode(new(any)) != io.EOF || !managementText(change.RequestID, 64) || !managementText(change.Target, 64) || len(change.Fingerprint) != 64 || !managementText(change.Reason, 256) {
		fail(http.StatusBadRequest, "invalid Garm settings change")
		return
	}
	if (r.URL.Path == "/admin/api/v1/garm/pool" && (change.Pool == nil || change.Balancer != "")) || (r.URL.Path == "/admin/api/v1/garm/repository" && (change.Pool != nil || change.Balancer == "")) {
		fail(http.StatusBadRequest, "invalid Garm settings change")
		return
	}
	select {
	case h.remoteFlight <- struct{}{}:
		defer func() { <-h.remoteFlight }()
	default:
		fail(http.StatusConflict, "another Garm change is in progress; refresh before retrying")
		return
	}
	if change.Pool != nil {
		_, err = h.garm.UpdatePool(ctx, change.RequestID, change.Target, change.Fingerprint, *change.Pool, change.Reason)
	} else {
		_, err = h.garm.UpdateRepositoryBalancer(ctx, change.RequestID, change.Target, change.Fingerprint, change.Balancer, change.Reason)
	}
	switch {
	case errors.Is(err, garmapi.ErrManagementConflict):
		fail(http.StatusConflict, "Garm settings changed; refresh and review a new change")
	case errors.Is(err, garmapi.ErrManagementUnsafe):
		fail(http.StatusBadRequest, "unsupported or unsafe Garm settings change")
	case errors.Is(err, garmapi.ErrManagementDisabled):
		fail(http.StatusNotFound, "Garm writes disabled")
	case err != nil:
		h.audit("Garm management outcome unavailable")
		fail(http.StatusServiceUnavailable, "Garm change outcome unavailable; refresh journal status; do not retry")
	default:
		_ = json.NewEncoder(w).Encode(map[string]bool{"recorded_confirmed": true})
	}
}
