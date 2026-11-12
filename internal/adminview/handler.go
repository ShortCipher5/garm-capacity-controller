// Package adminview provides separately authorized read and ledger-policy surfaces.
// It cannot provision, retire resources or access provider secrets.
package adminview

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/retirement"
)

var ErrConfiguration = errors.New("invalid admin configuration")

type Allocation struct {
	Budget    admission.Resources `json:"budget"`
	Allocated admission.Resources `json:"allocated"`
}
type Host struct {
	BackendBindingStatus string                `json:"backend_binding_status"`
	Drain                retirement.Assessment `json:"drain_assessment"`
	ID                   string                `json:"id"`
	Mode                 string                `json:"mode"`
	Healthy              bool                  `json:"last_observed_healthy"`
	Fresh                bool                  `json:"observation_fresh"`
	ObservedAt           time.Time             `json:"observed_at"`
	AdmissionPaused      bool                  `json:"new_claims_paused"`
	Allocation
}
type Pool struct {
	ID    string   `json:"id"`
	Hosts []string `json:"hosts"`
	Allocation
}
type Event struct {
	Sequence   int64     `json:"sequence"`
	Kind       string    `json:"kind"`
	Host       string    `json:"host,omitempty"`
	RequestKey string    `json:"request_key,omitempty"`
	At         time.Time `json:"at"`
}
type Overview struct {
	GarmManagementAvailable bool           `json:"garm_management_available"`
	PolicyRevision          string         `json:"policy_revision"`
	ManagementEnabled       bool           `json:"management_enabled"`
	Mode                    string         `json:"mode"`
	Provisioning            bool           `json:"experimental_provisioning_enabled"`
	Ready                   bool           `json:"admission_ready"`
	Epoch                   int64          `json:"epoch"`
	At                      time.Time      `json:"observed_at"`
	Global                  Allocation     `json:"global"`
	Hosts                   []Host         `json:"hosts"`
	Pools                   []Pool         `json:"pools"`
	States                  map[string]int `json:"states"`
	Events                  []Event        `json:"events"`
}
type Handler struct {
	store        *admission.SQLiteStore
	tokenHash    [32]byte
	writeHash    [32]byte
	management   bool
	garm         GarmManager
	garmWrites   bool
	remoteFlight chan struct{}
	assets       *os.Root
	provision    bool
	inFlight     chan struct{}
	Audit        func(string)
}

// Token is 32 random bytes encoded as 64 lowercase hexadecimal characters and
// delivered out-of-band from an owner-only file. It grants READ access only.
func New(store *admission.SQLiteStore, token string, assets *os.Root, provision bool) (*Handler, error) {
	decoded, err := hex.DecodeString(token)
	if store == nil || err != nil || len(decoded) != 32 || token != strings.ToLower(token) {
		return nil, ErrConfiguration
	}
	return &Handler{store: store, tokenHash: sha256.Sum256([]byte("Bearer " + token)), assets: assets, provision: provision, inFlight: make(chan struct{}, 16)}, nil
}
func (h *Handler) audit(event string) {
	if h.Audit != nil {
		h.Audit(event)
	}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	fail := func(status int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{\"error\":\"admin view unavailable\"}\n"))
	}
	defer func() {
		if recover() != nil {
			h.audit("admin view failed")
			fail(http.StatusServiceUnavailable)
		}
	}()
	if r.TLS == nil {
		fail(http.StatusBadRequest)
		return
	}
	if r.URL.RawQuery != "" {
		fail(http.StatusBadRequest)
		return
	} // Never accept tokens in URLs.
	select {
	case h.inFlight <- struct{}{}:
		defer func() { <-h.inFlight }()
	default:
		fail(http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/admin/api/v1/garm" || strings.HasPrefix(r.URL.Path, "/admin/api/v1/garm/") {
		h.serveGarm(w, r)
		return
	}
	if r.URL.Path == "/admin/api/v1/policy" {
		h.servePolicy(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/api/") {
		if r.URL.Path != "/admin/api/v1/overview" {
			fail(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed)
			return
		}
		values := r.Header.Values("Authorization")
		var presented [32]byte
		if len(values) == 1 {
			presented = sha256.Sum256([]byte(values[0]))
		}
		if len(values) != 1 || subtle.ConstantTimeCompare(h.tokenHash[:], presented[:]) != 1 {
			h.audit("admin authentication rejected")
			fail(http.StatusUnauthorized)
			return
		}
		snapshot, err := h.store.ReadSnapshot(r.Context())
		if err != nil {
			h.audit("admin snapshot unavailable")
			fail(http.StatusServiceUnavailable)
			return
		}
		view, err := overview(snapshot, h.provision, time.Now().UTC())
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		view.ManagementEnabled = h.management
		view.GarmManagementAvailable = h.garm != nil
		data, err := json.Marshal(view)
		if err != nil || len(data) > 2<<20 {
			fail(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(data, '\n'))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(http.StatusMethodNotAllowed)
		return
	}
	asset := "index.html"
	if r.URL.Path != "/" {
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			fail(http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if name == "" || len(name) > 128 || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
			fail(http.StatusNotFound)
			return
		}
		for _, char := range name {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char)) {
				fail(http.StatusNotFound)
				return
			}
		}
		switch path.Ext(name) {
		case ".js", ".css", ".svg", ".woff2":
		default:
			fail(http.StatusNotFound)
			return
		}
		asset = "assets/" + name
	}
	if h.assets == nil {
		fail(http.StatusNotFound)
		return
	}
	file, err := h.assets.OpenFile(asset, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		fail(http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		fail(http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		fail(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(asset)))
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
func overview(state admission.SQLiteSnapshot, provision bool, now time.Time) (Overview, error) {
	result := Overview{PolicyRevision: admission.PolicyRevision(state.Config), Mode: "experimental-ledger", Provisioning: provision, Ready: state.Ready, Epoch: state.Epoch, At: now, Global: Allocation{state.Config.Global, state.Allocated.Global}, Hosts: []Host{}, Pools: []Pool{}, States: map[string]int{}, Events: []Event{}}
	for id, host := range state.Config.Hosts {
		drain, err := retirement.Assess(state, id, now)
		if err != nil {
			return Overview{}, err
		}
		age := now.Sub(host.ObservedAt)
		fresh := age >= 0 && age <= state.Config.MaxObservationAge
		bindingStatus := backendBindingStatus(state, id)
		result.Hosts = append(result.Hosts, Host{BackendBindingStatus: bindingStatus, Drain: drain, ID: id, Mode: host.Mode, Healthy: host.Healthy, Fresh: fresh, ObservedAt: host.ObservedAt, AdmissionPaused: bindingStatus != "bound_observation" || !provision || !state.Ready || host.Mode != "enabled" || !host.Healthy || !fresh, Allocation: Allocation{host.Budget, state.Allocated.Hosts[id]}})
	}
	sort.Slice(result.Hosts, func(i, j int) bool { return result.Hosts[i].ID < result.Hosts[j].ID })
	for id, pool := range state.Config.Pools {
		result.Pools = append(result.Pools, Pool{ID: id, Hosts: append([]string(nil), pool.Hosts...), Allocation: Allocation{pool.Budget, state.Allocated.Pools[id]}})
	}
	sort.Slice(result.Pools, func(i, j int) bool { return result.Pools[i].ID < result.Pools[j].ID })
	linked := map[string]string{}
	for _, resource := range state.Inventory {
		if resource.ReservationKey != "" {
			linked[resource.ReservationKey] = resource.State
		} else {
			result.States[resource.State]++
		}
	}
	for key, reservation := range state.Reservations {
		status := reservation.State
		if physical, ok := linked[key]; ok {
			status = physical
		}
		result.States[status]++
	}
	for _, event := range state.Audit {
		result.Events = append(result.Events, Event{event.Sequence, event.Kind, event.Host, event.RequestKey, event.RecordedAt})
	}
	return result, nil
}

// backendBindingStatus exposes only the classification. An authenticated daemon
// observation is not physical-hardware, whole-host or parent-quota qualification.
// Legacy exclusion wins even if a future inconsistent snapshot also has a bind.
func backendBindingStatus(state admission.SQLiteSnapshot, host string) string {
	for _, legacy := range state.LegacyUnboundHosts {
		if legacy == host {
			return "legacy_scope_import_required"
		}
	}
	if _, exists := state.HostBindings[host]; exists {
		return "bound_observation"
	}
	return "unbound"
}
