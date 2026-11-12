// Package gatewayrpc implements the provider-only credential-isolation boundary.
// It exposes no admin/configuration/retirement API.
package gatewayrpc

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	common "github.com/cloudbase/garm-provider-common/params"
)

var ErrConfiguration = errors.New("invalid authenticated gateway configuration")

const MaximumRequestBytes = 1 << 20
const MaximumResponseBytes = 1 << 20

type Handler struct {
	facade             *gateway.Facade
	fingerprints       map[string]bool
	sourceFingerprints map[string]bool
	provision          bool
	inFlight           chan struct{}
	Audit              func(string)
}

// NewHandler authenticates a verified client chain AND an explicit leaf SHA256
// allowlist. CA membership alone does not authorize another controller. One
// facade/identity scope per listener; certificate revocation requires replacement.
func NewHandler(facade *gateway.Facade, allowed []string, allowExperimentalProvisioning bool, sourceAllowed ...[]string) (*Handler, error) {
	if facade == nil || len(allowed) == 0 || (allowExperimentalProvisioning && facade.RuntimeOriginalPreparationRequired() && !facade.PreparationConfigured()) {
		return nil, ErrConfiguration
	}
	h := &Handler{facade: facade, fingerprints: map[string]bool{}, sourceFingerprints: map[string]bool{}, provision: allowExperimentalProvisioning, inFlight: make(chan struct{}, 64)}
	for _, value := range allowed {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || value != strings.ToLower(value) || h.fingerprints[value] {
			return nil, ErrConfiguration
		}
		h.fingerprints[value] = true
	}
	if len(sourceAllowed) > 1 {
		return nil, ErrConfiguration
	}
	if len(sourceAllowed) == 1 {
		for _, fp := range sourceAllowed[0] {
			raw, err := hex.DecodeString(fp)
			if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != fp || h.sourceFingerprints[fp] || h.fingerprints[fp] {
				return nil, ErrConfiguration
			}
			h.sourceFingerprints[fp] = true
		}
	}
	if facade.PreparationConfigured() {
		for _, fp := range facade.PreparationFingerprints() {
			if !h.sourceFingerprints[fp] {
				return nil, ErrConfiguration
			}
		}
	}
	return h, nil
}
func ServerTLS(certificate tls.Certificate, clients *x509.CertPool) (*tls.Config, error) {
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil || clients == nil {
		return nil, ErrConfiguration
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: clients.Clone(), ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}, nil
}

type Identity struct {
	Name   string `json:"name"`
	PoolID string `json:"pool_id"`
}
type AdmissionResult struct {
	ExitCode int             `json:"exit_code"`
	Response json.RawMessage `json:"response"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	audit := func(event string) {
		if h.Audit != nil {
			h.Audit(event)
		}
	}
	fail := func(status int) {
		audit("gateway request rejected")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{\"error\":\"gateway request unavailable\"}\n"))
	}
	defer func() {
		if recover() != nil {
			fail(http.StatusServiceUnavailable)
		}
	}()
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		fail(http.StatusUnauthorized)
		return
	}
	hash := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	actor := hex.EncodeToString(hash[:])
	sourceCommand := r.URL.Path == "/v1/record-preparation" || r.URL.Path == "/v1/validate-preparation"
	if (sourceCommand && !h.sourceFingerprints[actor]) || (!sourceCommand && !h.fingerprints[actor]) {
		fail(http.StatusForbidden)
		return
	}
	select {
	case h.inFlight <- struct{}{}:
		defer func() { <-h.inFlight }()
	default:
		fail(http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		fail(http.StatusNotFound)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		fail(http.StatusUnsupportedMediaType)
		return
	}
	if (r.URL.Path == "/v1/admit" || r.URL.Path == "/v1/create" || r.URL.Path == "/v1/cancel-admission" || sourceCommand) && !h.provision {
		fail(http.StatusForbidden)
		return
	}
	var result any
	switch r.URL.Path {
	case "/v1/record-preparation", "/v1/validate-preparation":
		data, err := io.ReadAll(io.LimitReader(r.Body, garmprotocol.MaximumResponseBytes+1))
		if err != nil {
			fail(http.StatusBadRequest)
			return
		}
		request, err := garmprotocol.DecodePreparationRequest(data)
		if err != nil {
			fail(http.StatusBadRequest)
			return
		}
		if !h.facade.AuthorizePreparationPool(request.PoolID, actor) {
			fail(http.StatusForbidden)
			return
		}
		var response garmprotocol.PreparationResponse
		if r.URL.Path == "/v1/record-preparation" {
			response, err = h.facade.RecordPreparation(r.Context(), request, actor)
		} else {
			response, err = h.facade.ValidatePreparation(r.Context(), request, actor)
		}
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = response
	case "/v1/admit":
		var request garmprotocol.Request
		if Decode(r.Body, &request) != nil {
			fail(http.StatusBadRequest)
			return
		}
		exit, data, err := h.facade.Admit(r.Context(), request)
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = AdmissionResult{exit, json.RawMessage(data)}
	case "/v1/cancel-admission":
		var request garmprotocol.Request
		if Decode(r.Body, &request) != nil {
			fail(http.StatusBadRequest)
			return
		}
		response, err := h.facade.CancelAdmission(r.Context(), request)
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = response
	case "/v1/create":
		var request common.BootstrapInstance
		if Decode(r.Body, &request) != nil {
			fail(http.StatusBadRequest)
			return
		}
		instance, err := h.facade.Create(r.Context(), request)
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = instance
	case "/v1/get":
		var request Identity
		if Decode(r.Body, &request) != nil {
			fail(http.StatusBadRequest)
			return
		}
		instance, err := h.facade.Get(r.Context(), request.Name, request.PoolID)
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = instance
	case "/v1/list":
		var request Identity
		if Decode(r.Body, &request) != nil || request.Name != "" {
			fail(http.StatusBadRequest)
			return
		}
		instances, err := h.facade.List(r.Context(), request.PoolID)
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		result = instances
	default:
		fail(http.StatusNotFound)
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaximumResponseBytes {
		fail(http.StatusServiceUnavailable)
		return
	}
	audit("gateway request completed")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(data, '\n'))
}

// Strict decoding rejects duplicate/unknown/case-folded struct keys recursively.
// RawMessage extraspecs are still rejected by the facade's policy boundary.
func Decode(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, MaximumRequestBytes+1))
	if err != nil || len(data) > MaximumRequestBytes {
		return ErrConfiguration
	}
	if err := exactKeys(data, reflect.TypeOf(target)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrConfiguration
	}
	return nil
}
func exactKeys(data []byte, t reflect.Type) error {
	nullable := t.Kind() == reflect.Slice || t.Kind() == reflect.Map || (t.Kind() == reflect.Pointer && t.Elem().Kind() != reflect.Struct)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		if nullable {
			return nil
		}
		return ErrConfiguration
	}
	// time.Time has an explicit JSON string representation; descending into its
	// private struct fields would reject canonical persisted host observations.
	if t == reflect.TypeOf(time.Time{}) {
		var timestamp time.Time
		if json.Unmarshal(data, &timestamp) != nil {
			return ErrConfiguration
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		decoder := json.NewDecoder(bytes.NewReader(data))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return ErrConfiguration
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "" {
				tag = f.Name
			}
			if tag != "-" && f.IsExported() {
				fields[tag] = f.Type
			}
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			name, ok := token.(string)
			field, known := fields[name]
			if err != nil || !ok || !known || seen[name] {
				return ErrConfiguration
			}
			seen[name] = true
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil || exactKeys(raw, field) != nil {
				return ErrConfiguration
			}
		}
		if _, err = decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF {
			return ErrConfiguration
		}
	case reflect.Map:
		decoder := json.NewDecoder(bytes.NewReader(data))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return ErrConfiguration
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			name, ok := token.(string)
			if err != nil || !ok || seen[name] {
				return ErrConfiguration
			}
			seen[name] = true
			switch t.Key().Kind() {
			case reflect.String:
			case reflect.Uint64, reflect.Uint, reflect.Uint32:
				value, err := strconv.ParseUint(name, 10, t.Key().Bits())
				if err != nil || strconv.FormatUint(value, 10) != name {
					return ErrConfiguration
				}
			default:
				return ErrConfiguration
			}
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil || exactKeys(raw, t.Elem()) != nil {
				return ErrConfiguration
			}
		}
		if _, err = decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF {
			return ErrConfiguration
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return ErrConfiguration
		}
		for _, value := range values {
			if exactKeys(value, t.Elem()) != nil {
				return ErrConfiguration
			}
		}
	}
	return nil
}
