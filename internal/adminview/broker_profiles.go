package adminview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

// BrokerProfileRecipe is deployment-owned configuration, never browser input.
// It specifies a permitted registration shape, not allocation/native authority.
type BrokerProfileRecipe struct {
	ID            string
	TrustIdentity string // SHA256 of deployment-owned CA file, configuration CAS only.
	Transport     stockidentity.Configuration
	Request       admission.BrokerPoolProfileRequest
	Policies      map[int64]nativeapi.Policy
}

var recipeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// SetBrokerProfileRecipes must be called before serving requests. Policies are
// already immutable compiler values; all caller-owned collections are copied.
func (h *Handler) SetBrokerProfileRecipes(recipes []BrokerProfileRecipe) error {
	if !h.management || len(recipes) == 0 || len(recipes) > 1000 || h.brokerProfiles != nil {
		return ErrConfiguration
	}
	next := map[string]BrokerProfileRecipe{}
	pools := map[string]bool{}
	for _, r := range recipes {
		if !recipeID.MatchString(r.ID) || len(r.TrustIdentity) != 64 || next[r.ID].ID != "" || pools[r.Request.Authority.PoolID] || r.Transport.Pool != r.Request.Authority.PoolID || r.Transport.Provider != r.Request.Authority.ProviderName || r.Request.Execution.Name != "" || len(r.Policies) == 0 || len(r.Policies) > 1000 || stockidentity.ValidateConfiguration(r.Transport) != nil {
			return ErrConfiguration
		}
		r.Transport.TLS = r.Transport.TLS.Clone()
		if r.Transport.TLS.RootCAs != nil {
			r.Transport.TLS.RootCAs = r.Transport.TLS.RootCAs.Clone()
		}
		r.Request.Execution.Profiles = append([]string(nil), r.Request.Execution.Profiles...)
		policies := map[int64]nativeapi.Policy{}
		for seq, p := range r.Policies {
			if seq <= 0 || p.Digest() == "" {
				return ErrConfiguration
			}
			policies[seq] = p
		}
		r.Policies = policies
		next[r.ID] = r
		pools[r.Request.Authority.PoolID] = true
	}
	h.brokerProfiles = next
	return nil
}

// A reviewed recipe cannot silently change across an admin restart. Credentials
// are excluded; this digest is configuration CAS, never authenticated authority.
func brokerRecipeRevision(r BrokerProfileRecipe) string {
	digests := map[int64]string{}
	for sequence, p := range r.Policies {
		digests[sequence] = p.Digest()
	}
	raw, _ := json.Marshal(struct {
		ID, Endpoint, TrustIdentity              string
		ManagedServerKeyPin, ManagedControllerID string
		Request                                  admission.BrokerPoolProfileRequest
		Policies                                 map[int64]string
	}{r.ID, r.Transport.Endpoint, r.TrustIdentity, r.Transport.ManagedServerKeyPin, r.Transport.ManagedControllerID, r.Request, digests})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

type brokerProfileSummary struct {
	Pool       string              `json:"pool"`
	Repository string              `json:"repository"`
	Size       string              `json:"size_profile"`
	Sequence   int64               `json:"sequence"`
	Resources  admission.Resources `json:"resources"`
	Hosts      []string            `json:"hosts"`
}

func summarizeBrokerProfile(p admission.BrokerPoolProfile) brokerProfileSummary {
	hosts := []string{}
	for _, d := range p.Domains {
		hosts = append(hosts, d.Host)
	}
	return brokerProfileSummary{p.Pool, p.Repository, p.SizeProfile, p.Sequence, p.Resources, hosts}
}
func (h *Handler) serveBrokerProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	}
	switch r.Method {
	case http.MethodGet:
		if !h.profileReadAuthenticated(r) {
			fail(401, "token rejected")
			return
		}
		profiles, err := h.store.CurrentBrokerPoolProfiles(r.Context())
		if err != nil {
			fail(503, "pool mappings unavailable")
			return
		}
		current := []brokerProfileSummary{}
		for _, p := range profiles {
			current = append(current, summarizeBrokerProfile(p))
		}
		sort.Slice(current, func(i, j int) bool { return current[i].Pool < current[j].Pool })
		type choice struct {
			Revision string `json:"revision"`
			ID       string `json:"id"`
			Pool     string `json:"pool"`
		}
		choices := []choice{}
		for id, p := range h.brokerProfiles {
			choices = append(choices, choice{brokerRecipeRevision(p), id, p.Request.Authority.PoolID})
		}
		sort.Slice(choices, func(i, j int) bool { return choices[i].ID < choices[j].ID })
		_ = json.NewEncoder(w).Encode(map[string]any{"profiles": current, "recipes": choices, "writes_enabled": h.management && len(choices) > 0, "configuration_only": true})
	case http.MethodPost:
		if !h.profileWriteAuthenticated(w, r) {
			return
		}
		if len(h.brokerProfiles) == 0 {
			fail(404, "registration recipes unavailable")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			fail(415, "JSON required")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(raw) > 4096 {
			fail(413, "request too large")
			return
		}
		var input struct {
			RecipeRevision string              `json:"recipe_revision"`
			Recipe         string              `json:"recipe"`
			Size           string              `json:"size_profile"`
			Previous       int64               `json:"expected_sequence"`
			Resources      admission.Resources `json:"resources"`
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if !uniqueJSON(raw) || d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Resources.CPU <= 0 || input.Resources.CPU%1000 != 0 || input.Resources.RAM <= 0 || input.Resources.Disk <= 0 || input.Resources.Runners != 1 || input.Previous < 0 || input.Previous > 9007199254740991 || !recipeID.MatchString(input.Size) {
			fail(400, "invalid registration")
			return
		}
		recipe, ok := h.brokerProfiles[input.Recipe]
		if !ok {
			fail(400, "unknown registration recipe")
			return
		}
		if input.RecipeRevision != brokerRecipeRevision(recipe) {
			fail(409, "registration recipe changed; reload and review")
			return
		}
		// Fail stale UI revisions before upstream I/O. The store checks CAS again in
		// the authoritative transaction, including concurrent writes after this read.
		current, err := h.store.CurrentBrokerPoolProfiles(r.Context())
		if err != nil {
			fail(503, "pool mappings unavailable")
			return
		}
		if current[recipe.Request.Authority.PoolID].Sequence != input.Previous {
			fail(409, "mapping changed; reload and review")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		observation, err := stockidentity.Observe(ctx, recipe.Transport)
		if err != nil {
			fail(503, "fresh stock ownership unavailable; no registration confirmed")
			return
		}
		request := recipe.Request
		request.SizeProfile = input.Size
		request.ExpectedResources = input.Resources
		request.ExpectedProfileSequence = input.Previous
		if request.SourceIdentity == "" {
			request.SourceIdentity = observation.SourceIdentity()
		}
		profile, err := h.store.RegisterBrokerPoolProfile(ctx, request, observation, recipe.Policies)
		if err != nil {
			h.audit("broker profile registration not confirmed")
			fail(409, "registration not confirmed; reload and review size, ownership and native policy")
			return
		}
		h.audit("broker profile registration recorded")
		_ = json.NewEncoder(w).Encode(map[string]any{"profile": summarizeBrokerProfile(profile), "configuration_only": true, "saved": true})
	default:
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
	}
}

// ValidateBrokerProfileRecipes performs the same startup validation without
// opening a ledger, making upstream requests or enabling an HTTP surface.
func ValidateBrokerProfileRecipes(recipes []BrokerProfileRecipe) error {
	return (&Handler{management: true}).SetBrokerProfileRecipes(recipes)
}
