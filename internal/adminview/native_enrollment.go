package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

// NativeEnrollmentOwner and recipes are private deployment configuration.
// Browser requests can select only an immutable recipe/revision.
type NativeEnrollmentOwner struct {
	TrustIdentity string
	Policy        nativeapi.Policy
	Transport     nativeidentity.Configuration
}
type NativeEnrollmentRecipe struct {
	ID      string
	Initial bool
	Owners  []NativeEnrollmentOwner
}
type frozenNativeRecipe struct {
	NativeEnrollmentRecipe
	revision string
}

func (h *Handler) SetNativeEnrollmentRecipes(recipes []NativeEnrollmentRecipe) error {
	if !h.management || h.nativeEnrollments != nil || len(recipes) == 0 || len(recipes) > 1000 {
		return ErrConfiguration
	}
	next := map[string]frozenNativeRecipe{}
	for _, r := range recipes {
		if !recipeID.MatchString(r.ID) || next[r.ID].ID != "" || len(r.Owners) == 0 || len(r.Owners) > 1000 {
			return ErrConfiguration
		}
		r.Owners = append([]NativeEnrollmentOwner(nil), r.Owners...)
		hosts := map[string]bool{}
		domains := map[string]bool{}
		controller := ""
		material := []any{r.ID, r.Initial}
		for i, o := range r.Owners {
			p, c := o.Policy, o.Transport
			trustBytes, trustError := hex.DecodeString(o.TrustIdentity)
			if trustError != nil || len(trustBytes) != 32 || hex.EncodeToString(trustBytes) != o.TrustIdentity {
				return ErrConfiguration
			}
			u, e := url.Parse(c.Endpoint)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !p.Protected() || hosts[p.Host()] || domains[p.DomainID()] || c.PolicyDigest != p.Digest() || c.DomainID != p.DomainID() {
				return ErrConfiguration
			}
			if controller != "" && controller != p.Controller() {
				return ErrConfiguration
			}
			controller = p.Controller()
			pin := func(s string) bool {
				b, e := hex.DecodeString(s)
				return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
			}
			if !pin(c.OwnerClientLeaf) || !pin(c.OwnerServerLeaf) || c.OwnerClientLeaf == c.OwnerServerLeaf {
				return ErrConfiguration
			}
			t := c.TLS
			if t == nil || t.InsecureSkipVerify || t.RootCAs == nil || len(t.Certificates) != 1 || len(t.Certificates[0].Certificate) == 0 || t.GetClientCertificate != nil || t.VerifyConnection != nil || t.VerifyPeerCertificate != nil {
				return ErrConfiguration
			}
			// Freeze static credentials and trust. No retained caller-owned key/cert
			// slices, callbacks, key logger or TLS session cache can alter this recipe.
			cert := t.Certificates[0]
			key, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
			if e != nil {
				return ErrConfiguration
			}
			cert.PrivateKey, e = x509.ParsePKCS8PrivateKey(key)
			if e != nil {
				return ErrConfiguration
			}
			chain := make([][]byte, len(cert.Certificate))
			for j, b := range cert.Certificate {
				chain[j] = append([]byte(nil), b...)
			}
			cert.Certificate = chain
			cert.Leaf, e = x509.ParseCertificate(chain[0])
			if e != nil {
				return ErrConfiguration
			}
			cert.OCSPStaple = nil
			cert.SignedCertificateTimestamps = nil
			cert.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), cert.SupportedSignatureAlgorithms...)
			leaf := nativeidentity.Fingerprint(chain[0])
			if leaf == c.OwnerServerLeaf || leaf == c.OwnerClientLeaf {
				return ErrConfiguration
			}
			c.TLS = &tls.Config{RootCAs: t.RootCAs.Clone(), Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, ServerName: t.ServerName}
			// Trust hash is review CAS only, not authority; actual TLS independently
			// verifies the frozen roots and exact role leaf on every observation.
			material = append(material, []any{p.Host(), p.Controller(), p.Digest(), c.Endpoint, c.DomainID, c.OwnerClientLeaf, c.OwnerServerLeaf, chain, o.TrustIdentity, c.TLS.ServerName})
			r.Owners[i] = NativeEnrollmentOwner{TrustIdentity: o.TrustIdentity, Policy: p, Transport: c}
			hosts[p.Host()] = true
			domains[p.DomainID()] = true
		}
		raw, _ := json.Marshal(material)
		digest := sha256.Sum256(raw)
		next[r.ID] = frozenNativeRecipe{r, hex.EncodeToString(digest[:])}
	}
	h.nativeEnrollments = next
	return nil
}

func (h *Handler) serveNativeEnrollment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "readiness_authorized": false})
	}
	if r.Method == http.MethodGet {
		if !h.profileReadAuthenticated(r) {
			fail(401, "token rejected")
			return
		}
		domains, sequence, e := h.store.NativeDomainEnrollmentView(r.Context())
		if e != nil {
			fail(503, "enrollment unavailable")
			return
		}
		recipes := []map[string]any{}
		for id, v := range h.nativeEnrollments {
			hosts := []string{}
			for _, o := range v.Owners {
				hosts = append(hosts, o.Policy.Host())
			}
			sort.Strings(hosts)
			mode := "existing"
			if v.Initial {
				mode = "initial"
			}
			recipes = append(recipes, map[string]any{"id": id, "revision": v.revision, "hosts": hosts, "mode": mode})
		}
		sort.Slice(recipes, func(i, j int) bool { return recipes[i]["id"].(string) < recipes[j]["id"].(string) })
		enrolled := []map[string]any{}
		for _, d := range domains {
			enrolled = append(enrolled, map[string]any{"host": d.Spec.Host, "domain_id": d.Spec.DomainID, "sequence": d.Sequence})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"recipes": recipes, "enrolled": enrolled, "ledger_sequence": sequence, "writes_enabled": h.management && len(recipes) > 0, "readiness_authorized": false})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(405, "GET or POST required")
		return
	}
	if !h.profileWriteAuthenticated(w, r) {
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
	var input struct {
		Recipe   string `json:"recipe"`
		Revision string `json:"recipe_revision"`
		Sequence int64  `json:"expected_sequence"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if !uniqueJSON(raw) || dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF || input.Sequence <= 0 || input.Sequence > 9007199254740991 {
		fail(400, "invalid enrollment request")
		return
	}
	recipe, ok := h.nativeEnrollments[input.Recipe]
	if !ok || input.Revision != recipe.revision {
		fail(409, "recipe changed; reload and review")
		return
	}
	confirmed := 0
	result := func(code int, msg string, partial bool) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"recipe": input.Recipe, "recipe_revision": input.Revision, "expected_sequence": input.Sequence, "confirmed_domains": confirmed, "error": msg, "partial": partial, "configuration_only": true, "readiness_authorized": false})
	}
	state, e := h.store.ReadBrokerGrantState(r.Context())
	if e != nil || state.Sequence != input.Sequence {
		result(409, "ledger changed; reload and review", false)
		return
	}
	hosts := []string{}
	for _, o := range recipe.Owners {
		host := o.Policy.Host()
		if _, ok := state.Config.Hosts[host]; !ok {
			result(409, "configured host unavailable", false)
			return
		}
		if !recipe.Initial {
			if _, ok := state.HostBindings[host]; !ok {
				result(409, "existing exact host binding required", false)
				return
			}
		}
		hosts = append(hosts, host)
	}
	var fence admission.Fence
	if recipe.Initial {
		fence, e = h.store.BeginInitialNativeEnrollment(r.Context(), input.Sequence, hosts)
		if e != nil {
			result(409, "initial enrollment requires a wholly fresh ledger", false)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	obs := make([]nativeidentity.Observation, len(recipe.Owners))
	bindings := make([]admission.HostBinding, len(recipe.Owners))
	for i, o := range recipe.Owners {
		obs[i], e = nativeidentity.Observe(ctx, o.Transport)
		if e != nil {
			result(503, "fresh pinned owner observation unavailable; reload before retry", recipe.Initial)
			return
		}
		bindings[i], e = admission.ObservedNativeHostBinding(o.Policy.Host(), obs[i])
		if e != nil || (!recipe.Initial && !state.HostBindings[o.Policy.Host()].Matches(bindings[i])) {
			result(409, "owner identity does not match host binding", recipe.Initial)
			return
		}
	}
	if recipe.Initial {
		if e = h.store.BindHostIdentities(ctx, fence, bindings); e != nil {
			result(409, "initial host binding not confirmed; reload before retry", true)
			return
		}
	}
	for i, o := range recipe.Owners {
		p, c := o.Policy, o.Transport
		host, e := h.store.LookupHostBinding(ctx, p.Host())
		if e != nil || !host.Matches(bindings[i]) {
			result(409, "exact host binding unavailable; reload before retry", recipe.Initial || confirmed > 0)
			return
		}
		_, e = h.store.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: p.Controller(), Host: p.Host(), HostBindingSequence: host.Sequence, DomainID: p.DomainID(), PolicyDigest: p.Digest(), OwnerClientLeaf: c.OwnerClientLeaf, OwnerServerLeaf: c.OwnerServerLeaf}, obs[i])
		if e != nil {
			result(409, "enrollment not confirmed; reload before retry", true)
			return
		}
		confirmed++
	}
	result(200, "", false)
}
