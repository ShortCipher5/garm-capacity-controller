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
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// EnableManagement is startup-only. It requires an independent write credential;
// the read token can never edit policy, even when management is enabled.
func (h *Handler) EnableManagement(token string) error {
	decoded, err := hex.DecodeString(token)
	hash := sha256.Sum256([]byte("Bearer " + token))
	if err != nil || len(decoded) != 32 || token != strings.ToLower(token) || subtle.ConstantTimeCompare(h.tokenHash[:], hash[:]) == 1 {
		return ErrConfiguration
	}
	h.writeHash = hash
	h.management = true
	return nil
}

func (h *Handler) servePolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, message string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if !h.management {
		fail(http.StatusNotFound, "management disabled")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed, "POST required")
		return
	}
	values := r.Header.Values("Authorization")
	var presented [32]byte
	if len(values) == 1 {
		presented = sha256.Sum256([]byte(values[0]))
	}
	if len(values) != 1 || subtle.ConstantTimeCompare(h.writeHash[:], presented[:]) != 1 {
		h.audit("management authentication rejected")
		fail(http.StatusUnauthorized, "management token rejected")
		return
	}
	// No cookie auth, CORS or redirects. Reject browser cross-origin requests even
	// if a future frontend accidentally exposes a credential to another origin.
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host != r.Host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			fail(http.StatusForbidden, "same origin required")
			return
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
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
		fail(http.StatusBadRequest, "invalid policy change")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var change admission.PolicyChange
	if decoder.Decode(&change) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(http.StatusBadRequest, "invalid policy change")
		return
	}
	err = h.store.ApplyPolicyChange(r.Context(), change)
	switch {
	case errors.Is(err, admission.ErrPolicyConflict):
		fail(http.StatusConflict, "policy changed; refresh before retrying")
	case errors.Is(err, admission.ErrDenied):
		fail(http.StatusBadRequest, "invalid policy change")
	case err != nil:
		h.audit("management change outcome unavailable")
		fail(http.StatusServiceUnavailable, "outcome unavailable; refresh policy and audit before retrying")
	default:
		_ = json.NewEncoder(w).Encode(map[string]bool{"applied": true})
	}
}

// Reject duplicate fields rather than silently applying a last-value override.
func uniqueJSON(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	var consume func(int) bool
	consume = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		if token == nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || name != strings.ToLower(name) || seen[name] {
					return false
				}
				seen[name] = true
				if !consume(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !consume(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !consume(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
