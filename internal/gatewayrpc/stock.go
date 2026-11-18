package gatewayrpc

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

// StockScope is administrator-owned configuration, not request JSON. Stock
// leaves must be separate from ALL prepared/provider/source leaves on the service.
type StockScope struct {
	Controller        string `json:"controller"`
	PoolID            string `json:"pool_id"`
	ProviderName      string `json:"provider_name"`
	ClientFingerprint string `json:"client_fingerprint"`
}
type StockHandler struct {
	facade   *gateway.Facade
	scopes   map[string]StockScope
	inFlight chan struct{}
}

// NewStockHandler intentionally does not alter NewHandler's prepared contract.
// The existing handler supplies its actual ordinary/source leaf allowlists, so
// the caller cannot accidentally omit a conflicting identity. No listener starts.
func NewStockHandler(base *Handler, scopes []StockScope) (*StockHandler, error) {
	if base == nil || base.facade == nil || len(scopes) == 0 {
		return nil, ErrConfiguration
	}
	f := base.facade
	// A startup flag cannot prove exclusive native ownership. Refuse runtime
	// activation until an independently verified host authority is implemented.
	if f.RuntimeOriginalPreparationRequired() {
		return nil, ErrConfiguration
	}
	occupied := map[string]bool{}
	for fp := range base.fingerprints {
		occupied[fp] = true
	}
	for fp := range base.sourceFingerprints {
		occupied[fp] = true
	}
	h := &StockHandler{facade: f, scopes: map[string]StockScope{}, inFlight: make(chan struct{}, 64)}
	for _, s := range scopes {
		raw, err := hex.DecodeString(s.ClientFingerprint)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != s.ClientFingerprint || occupied[s.ClientFingerprint] || s.ProviderName == "" || !f.AuthorizesStockScope(s.Controller, s.PoolID) {
			return nil, ErrConfiguration
		}
		// One leaf has exactly one pool/provider/controller scope. No wildcard matching.
		if _, exists := h.scopes[s.ClientFingerprint]; exists {
			return nil, ErrConfiguration
		}
		h.scopes[s.ClientFingerprint] = s
	}
	return h, nil
}
func (h *StockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{\"error\":\"stock provider outcome unavailable\"}\n"))
	}
	if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
		fail(http.StatusUnauthorized)
		return
	}
	leaf := r.TLS.PeerCertificates[0]
	verified := false
	for _, chain := range r.TLS.VerifiedChains {
		if len(chain) > 0 && chain[0].Equal(leaf) {
			verified = true
		}
	}
	if !verified {
		fail(http.StatusUnauthorized)
		return
	}
	sum := sha256.Sum256(leaf.Raw)
	scope, ok := h.scopes[hex.EncodeToString(sum[:])]
	if !ok {
		fail(http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/stock/create" || r.URL.RawQuery != "" {
		fail(http.StatusNotFound)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		fail(http.StatusUnsupportedMediaType)
		return
	}
	select {
	case h.inFlight <- struct{}{}:
		defer func() { <-h.inFlight }()
	default:
		fail(http.StatusServiceUnavailable)
		return
	}
	var b common.BootstrapInstance
	if Decode(r.Body, &b) != nil {
		fail(http.StatusBadRequest)
		return
	}
	if b.PoolID != scope.PoolID {
		fail(http.StatusForbidden)
		return
	}
	result, err := h.facade.CreateStockProvider(r.Context(), b, admission.StockProviderAuthority{Controller: scope.Controller, PoolID: scope.PoolID, ProviderName: scope.ProviderName, ClientFingerprint: scope.ClientFingerprint})
	if err != nil {
		fail(http.StatusServiceUnavailable)
		return
	}
	// No request, token, bootstrap payload, or backend error is logged or reflected.
	_ = json.NewEncoder(w).Encode(result)
}

// CreateStockProvider is explicitly selected by the stock plugin. A transport
// error is unknown, never authorization to retry a physical create. The ledger
// handles repeated identical calls; the HTTP client performs no automatic retry.
func (c *Client) CreateStockProvider(ctx context.Context, b common.BootstrapInstance) (common.ProviderInstance, error) {
	var result common.ProviderInstance
	if err := c.call(ctx, "/v1/stock/create", b, &result); err != nil {
		return common.ProviderInstance{}, err
	}
	if result.Name != b.Name || result.ProviderID != b.Name || result.Status != common.InstanceRunning || result.OSType != b.OSType || result.OSArch != b.OSArch || len(result.ProviderFault) != 0 {
		return common.ProviderInstance{}, ErrUnavailable
	}
	return result, nil
}
