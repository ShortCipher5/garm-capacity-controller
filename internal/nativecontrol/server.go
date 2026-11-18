// Package nativecontrol implements purpose-separated authenticated owner control.
// Discovery and canonical consumption do not enable native mutation or release.
package nativecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

const ClaimPath = "/v1/native/claims/consume"

var ErrControl = errors.New("authenticated native owner control unavailable; retain charge")

type claimRequest struct {
	PermitID       string `json:"permit_id"`
	EnvelopeDigest string `json:"envelope_digest"`
}

func serverTLS(c *tls.Config) (*tls.Config, error) {
	if c == nil || c.InsecureSkipVerify || c.ClientCAs == nil || len(c.Certificates) != 1 || len(c.Certificates[0].Certificate) == 0 || c.Certificates[0].PrivateKey == nil || c.GetCertificate != nil || c.GetConfigForClient != nil || c.GetClientCertificate != nil || c.VerifyConnection != nil || c.VerifyPeerCertificate != nil {
		return nil, ErrControl
	}
	copy := c.Clone()
	copy.ClientCAs = c.ClientCAs.Clone()
	copy.MinVersion = tls.VersionTLS13
	copy.ClientAuth = tls.RequireAndVerifyClientCert
	copy.KeyLogWriter = nil
	copy.NextProtos = []string{"http/1.1"}
	return copy, nil
}
func newServer(c *tls.Config, h http.Handler) (*http.Server, error) {
	frozen, err := serverTLS(c)
	if err != nil {
		return nil, err
	}
	return &http.Server{Handler: h, TLSConfig: frozen, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}
func fingerprint(c *tls.Config) string {
	if c == nil || len(c.Certificates) != 1 || len(c.Certificates[0].Certificate) == 0 {
		return ""
	}
	return nativeidentity.Fingerprint(c.Certificates[0].Certificate[0])
}
func hash(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func peer(state *tls.ConnectionState, expected string) bool {
	return state != nil && state.HandshakeComplete && verifiedLeaf(state, expected)
}
func verifiedLeaf(state *tls.ConnectionState, expected string) bool {
	if state == nil || state.Version != tls.VersionTLS13 || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 || nativeidentity.Fingerprint(state.PeerCertificates[0].Raw) != expected {
		return false
	}
	for _, chain := range state.VerifiedChains {
		if len(chain) > 0 && chain[0].Equal(state.PeerCertificates[0]) {
			return true
		}
	}
	return false
}

// NewOwnerServer binds discovery to a real private native client. Root-owned
// deployment supplies a separately pinned controller client leaf and an owner
// claim-client certificate; no request supplies certificates, paths or policy.
func NewOwnerServer(api *nativeapi.Client, serverCredentials, claimCredentials *tls.Config, controllerClientLeaf string) (*http.Server, error) {
	serverLeaf, ownerLeaf := fingerprint(serverCredentials), fingerprint(claimCredentials)
	if api == nil || !hash(api.PolicyDigest()) || !hash(controllerClientLeaf) || !hash(serverLeaf) || !hash(ownerLeaf) || serverLeaf == ownerLeaf || serverLeaf == controllerClientLeaf || ownerLeaf == controllerClientLeaf || claimCredentials.InsecureSkipVerify || claimCredentials.RootCAs == nil || claimCredentials.Certificates[0].PrivateKey == nil {
		return nil, ErrControl
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !peer(r.TLS, controllerClientLeaf) {
			http.Error(w, "owner control authentication required", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != nativeidentity.Path || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		identity, err := api.Identity(r.Context())
		if err != nil {
			http.Error(w, "native identity unavailable", http.StatusServiceUnavailable)
			return
		}
		descriptor := nativeidentity.Descriptor{DomainID: identity.DomainID(), PolicyDigest: api.PolicyDigest(), OwnerClientLeaf: ownerLeaf, OwnerServerLeaf: serverLeaf, NativeServerKey: identity.ServerKey(), NativeOrigin: identity.Origin(), NativeScope: identity.Scope(), NativeServerName: identity.ServerName(), NativeServerVersion: identity.ServerVersion()}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(descriptor)
	})
	return newServer(serverCredentials, h)
}

// NewClaimServer exposes only same-ledger first consumption. It has no enrollment,
// cancellation, retry, native proxy, deletion or release endpoint. Authenticated
// owner scope is checked against the immutable enrolled leaf in the transaction.
func NewClaimServer(store *admission.SQLiteStore, credentials *tls.Config) (*http.Server, error) {
	if store == nil {
		return nil, ErrControl
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		auth, err := admission.AuthenticateNativeOwner(r.TLS)
		if err != nil {
			http.Error(w, "native owner authentication required", http.StatusUnauthorized)
			return
		}
		if serveBootstrap(w, r, store, auth) {
			return
		}
		if serveStep(w, r, store, auth) {
			return
		}
		if r.URL.Path != ClaimPath || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		var request claimRequest
		if err != nil || len(raw) > 4096 || decodeCanonical(raw, &request) != nil || !permitID(request.PermitID) || !hash(request.EnvelopeDigest) {
			http.Error(w, "exact canonical claim required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		claim, err := store.ConsumeNativeExecution(ctx, auth, request.PermitID, request.EnvelopeDigest)
		if err != nil {
			http.Error(w, "claim unavailable or already consumed; no native execution authorized", http.StatusConflict)
			return
		}
		// Only the newly committed result ever gets 201. A write/disconnect error
		// deliberately leaves consumption durable; no compensating refund or retry.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(claim)
	})
	return newServer(credentials, h)
}
func decodeCanonical(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return ErrControl
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return ErrControl
	}
	return nil
}

func permitID(v string) bool {
	if len(v) != 32 {
		return false
	}
	return hash(v + v)
}
