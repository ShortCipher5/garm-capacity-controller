package adminview

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// profileUpsertRequest is the operator runner-size profile write. Names and
// resource rules are admission's: whole vCPUs as millis, positive MiB RAM and
// non-negative MiB disk (zero ignores the disk dimension).
type profileUpsertRequest struct {
	Name      string `json:"name"`
	CPUMillis int64  `json:"cpu_millis"`
	RAMMiB    int64  `json:"ram_mib"`
	DiskMiB   int64  `json:"disk_mib"`
}

type profileHostEstimate struct {
	Host     string `json:"host"`
	Estimate int64  `json:"estimate"`
}

// listedProfile embeds the admission profile verbatim so the wire names stay
// the store's stable tags; estimates cover enabled hosts only.
type listedProfile struct {
	admission.Profile
	Estimates []profileHostEstimate `json:"estimates"`
}

// serveProfiles exposes runner-size profiles and their per-host fit
// estimates. GET routes accept the read credential (or the stronger
// management credential); writes require the management credential and a
// same-origin request, matching the other ledger-policy surfaces. This
// surface only names ledger profiles; PoolPolicy.Profiles remains the
// separate verbatim Incus profile list.
func (h *Handler) serveProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	const prefix = "/admin/api/v1/profiles"
	if r.URL.Path == prefix {
		switch r.Method {
		case http.MethodGet:
			h.listProfiles(w, r)
		case http.MethodPost:
			h.upsertProfile(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			fail(http.StatusMethodNotAllowed, "GET or POST required")
		}
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		fail(http.StatusNotFound, "profiles unavailable")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", "DELETE")
			fail(http.StatusMethodNotAllowed, "DELETE required")
			return
		}
		h.deleteProfile(w, r, parts[0])
	case len(parts) == 2 && parts[0] != "" && parts[1] == "estimate":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed, "GET required")
			return
		}
		h.estimateProfile(w, r, parts[0], "")
	case len(parts) == 3 && parts[0] != "" && parts[1] == "estimate" && parts[2] != "":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(http.StatusMethodNotAllowed, "GET required")
			return
		}
		h.estimateProfile(w, r, parts[0], parts[2])
	default:
		fail(http.StatusNotFound, "profiles unavailable")
	}
}

// profileReadAuthenticated accepts the read credential or, when management is
// enabled, the strictly stronger write credential.
func (h *Handler) profileReadAuthenticated(r *http.Request) bool {
	return adminAuthenticated(r, h.tokenHash) || (h.management && adminAuthenticated(r, h.writeHash))
}

func (h *Handler) profileWriteAuthenticated(w http.ResponseWriter, r *http.Request) bool {
	if !h.management {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "profile management unavailable"})
		return false
	}
	if !adminAuthenticated(r, h.writeHash) {
		h.audit("profile write authentication rejected")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "management token rejected"})
		return false
	}
	if !sameOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "same origin required"})
		return false
	}
	return true
}

// enabledProfileHosts lists config hosts eligible for profile estimates.
// Estimate absence for a host means the host is not enabled, never a
// fabricated zero.
func (h *Handler) enabledProfileHosts(r *http.Request) ([]string, error) {
	snapshot, err := h.store.ReadSnapshot(r.Context())
	if err != nil {
		return nil, err
	}
	hosts := []string{}
	for id, host := range snapshot.Config.Hosts {
		if host.Mode == "enabled" {
			hosts = append(hosts, id)
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.profileReadAuthenticated(r) {
		h.audit("profile read authentication rejected")
		fail(http.StatusUnauthorized, "token rejected")
		return
	}
	hosts, err := h.enabledProfileHosts(r)
	if err != nil {
		h.audit("profile host view unavailable")
		fail(http.StatusServiceUnavailable, "profile view unavailable; refresh before retrying")
		return
	}
	profiles, err := h.store.Profiles(r.Context())
	if err != nil {
		h.audit("profile view unavailable")
		fail(http.StatusServiceUnavailable, "profile view unavailable; refresh before retrying")
		return
	}
	result := []listedProfile{}
	for _, profile := range profiles {
		entry := listedProfile{Profile: profile, Estimates: []profileHostEstimate{}}
		for _, host := range hosts {
			estimate, err := h.store.ProfileEstimate(r.Context(), host, profile.Name)
			if err != nil {
				h.audit("profile estimate unavailable")
				fail(http.StatusServiceUnavailable, "profile estimate unavailable; refresh before retrying")
				return
			}
			entry.Estimates = append(entry.Estimates, profileHostEstimate{Host: host, Estimate: estimate})
		}
		result = append(result, entry)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"profiles": result})
}

func (h *Handler) upsertProfile(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.profileWriteAuthenticated(w, r) {
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
		fail(http.StatusBadRequest, "invalid runner-size profile")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request profileUpsertRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "invalid runner-size profile")
		return
	}
	if err := h.store.ProfileUpsert(r.Context(), request.Name, request.CPUMillis, request.RAMMiB, request.DiskMiB); err != nil {
		if errors.Is(err, admission.ErrDenied) {
			fail(http.StatusBadRequest, "invalid runner-size profile")
			return
		}
		h.audit("profile upsert outcome unavailable")
		fail(http.StatusServiceUnavailable, "upsert outcome unavailable; refresh before retrying")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"applied": true, "name": request.Name})
}

// deleteProfile removes one runner-size profile. Deletion succeeds even while
// a gateway pool policy still references the name: pool policies are gateway
// startup configuration this surface cannot inspect, so the response comment
// records that the next dispatch against a referencing pool fails closed with
// a policy error instead of charging unbounded or invented resources.
func (h *Handler) deleteProfile(w http.ResponseWriter, r *http.Request, name string) {
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.profileWriteAuthenticated(w, r) {
		return
	}
	if err := h.store.ProfileDelete(r.Context(), name); err != nil {
		switch {
		case errors.Is(err, admission.ErrProfile):
			fail(http.StatusNotFound, "runner-size profile unknown")
		case errors.Is(err, admission.ErrDenied):
			fail(http.StatusBadRequest, "invalid runner-size profile name")
		default:
			h.audit("profile delete outcome unavailable")
			fail(http.StatusServiceUnavailable, "delete outcome unavailable; refresh before retrying")
		}
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": true, "name": name, "comment": "pool policies referencing this profile fail closed at next dispatch; recreate the profile or reconfigure the pool"})
}

// estimateProfile reports the profile fit for one host, or for every enabled
// host when the host segment is empty. Unknown profiles and unknown or
// disabled hosts fail closed as not found; a zero estimate is an honest
// "does not fit", never an error.
func (h *Handler) estimateProfile(w http.ResponseWriter, r *http.Request, name, host string) {
	fail := func(status int, message string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.profileReadAuthenticated(r) {
		h.audit("profile estimate authentication rejected")
		fail(http.StatusUnauthorized, "token rejected")
		return
	}
	if host != "" {
		estimate, err := h.store.ProfileEstimate(r.Context(), host, name)
		if err != nil {
			switch {
			case errors.Is(err, admission.ErrProfile):
				fail(http.StatusNotFound, "runner-size profile or host unknown or disabled")
			case errors.Is(err, admission.ErrDenied):
				fail(http.StatusBadRequest, "invalid runner-size profile name")
			default:
				h.audit("profile estimate outcome unavailable")
				fail(http.StatusServiceUnavailable, "estimate outcome unavailable; refresh before retrying")
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "host": host, "estimate": estimate})
		return
	}
	hosts, err := h.enabledProfileHosts(r)
	if err != nil {
		h.audit("profile host view unavailable")
		fail(http.StatusServiceUnavailable, "estimate outcome unavailable; refresh before retrying")
		return
	}
	estimates := []profileHostEstimate{}
	for _, id := range hosts {
		estimate, err := h.store.ProfileEstimate(r.Context(), id, name)
		if err != nil {
			switch {
			case errors.Is(err, admission.ErrProfile):
				fail(http.StatusNotFound, "runner-size profile unknown")
			case errors.Is(err, admission.ErrDenied):
				fail(http.StatusBadRequest, "invalid runner-size profile name")
			default:
				h.audit("profile estimate outcome unavailable")
				fail(http.StatusServiceUnavailable, "estimate outcome unavailable; refresh before retrying")
			}
			return
		}
		estimates = append(estimates, profileHostEstimate{Host: id, Estimate: estimate})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "estimates": estimates})
}
