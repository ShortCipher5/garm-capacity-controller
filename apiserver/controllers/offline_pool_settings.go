// Copyright 2025 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	gErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm/apiserver/params"
	"github.com/cloudbase/garm/auth"
	"github.com/cloudbase/garm/database/common"
	runnerParams "github.com/cloudbase/garm/params"
)

// This file implements the experimental, opt-in offline pool settings API.
// The routes are only registered when the
// experimental.enable_offline_pool_settings config flag is enabled; the
// wire interface is provisional (v0) and documented in
// doc/experimental-offline-pool-settings.md.

// offlinePoolSettingsMaxBodyBytes bounds the size of a settings document.
const offlinePoolSettingsMaxBodyBytes = 4096

var (
	errOfflineSettingsMalformed = errors.New("malformed settings document")
	errOfflineIfMatchMissing    = errors.New("missing If-Match header")
	errOfflineIfMatchMalformed  = errors.New("malformed If-Match header")
)

// offlineSettingsKeys is the exact set of fields a settings document may
// contain.
var offlineSettingsKeys = map[string]bool{
	"priority":         true,
	"max_runners":      true,
	"min_idle_runners": true,
}

func offlineEntityType(pathValue string) (runnerParams.ForgeEntityType, bool) {
	switch pathValue {
	case "repository":
		return runnerParams.ForgeEntityTypeRepository, true
	case "organization":
		return runnerParams.ForgeEntityTypeOrganization, true
	case "enterprise":
		return runnerParams.ForgeEntityTypeEnterprise, true
	}
	return "", false
}

func offlinePoolPathVars(r *http.Request) (runnerParams.ForgeEntity, string, error) {
	vars := mux.Vars(r)
	entityType, ok := offlineEntityType(vars["entityType"])
	if !ok {
		return runnerParams.ForgeEntity{}, "", fmt.Errorf("invalid entity type %q: %w", vars["entityType"], errOfflineSettingsMalformed)
	}
	entityID := vars["entityID"]
	poolID := vars["poolID"]
	if entityID == "" || poolID == "" {
		return runnerParams.ForgeEntity{}, "", errOfflineSettingsMalformed
	}
	return runnerParams.ForgeEntity{ID: entityID, EntityType: entityType}, poolID, nil
}

// isCanonicalUintLiteral reports whether lit is a canonical JSON unsigned
// integer literal: digits only, no leading zeros, no sign, no decimal
// point and no exponent.
func isCanonicalUintLiteral(lit string) bool {
	if lit == "" {
		return false
	}
	if lit == "0" {
		return true
	}
	if lit[0] == '0' {
		return false
	}
	for _, c := range lit {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseOfflineSettingsValue validates one settings field value: it must be
// a canonical unsigned integer literal within the allowed range of the
// field.
func parseOfflineSettingsValue(key, lit string) (uint, error) {
	if !isCanonicalUintLiteral(lit) {
		return 0, fmt.Errorf("field %q: noncanonical integer literal %q: %w", key, lit, errOfflineSettingsMalformed)
	}
	val, err := strconv.ParseUint(lit, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q: invalid integer literal %q: %w", key, lit, errOfflineSettingsMalformed)
	}

	max := uint64(runnerParams.OfflinePoolSettingsMaxValue)
	min := uint64(0)
	if key == "max_runners" {
		min = runnerParams.OfflinePoolSettingsMinMaxRunners
	}
	if val < min || val > max {
		return 0, fmt.Errorf("field %q: value %d out of range [%d, %d]: %w", key, val, min, max, errOfflineSettingsMalformed)
	}
	return uint(val), nil
}

// decodeOfflinePoolSettingsPatch strictly decodes the request body as a
// JSON object containing exactly the known settings fields, with at least
// one field present, no duplicates, no unknown keys and canonical integer
// values.
func decodeOfflinePoolSettingsPatch(w http.ResponseWriter, r *http.Request) (runnerParams.OfflinePoolSettingsPatch, error) {
	var patch runnerParams.OfflinePoolSettingsPatch
	defer r.Body.Close()

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, offlinePoolSettingsMaxBodyBytes))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return patch, fmt.Errorf("invalid body: %w", errOfflineSettingsMalformed)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return patch, fmt.Errorf("body must be a JSON object: %w", errOfflineSettingsMalformed)
	}

	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return patch, fmt.Errorf("invalid body: %w", errOfflineSettingsMalformed)
		}
		key, ok := keyTok.(string)
		if !ok {
			return patch, fmt.Errorf("field names must be strings: %w", errOfflineSettingsMalformed)
		}
		if !offlineSettingsKeys[key] {
			return patch, fmt.Errorf("unknown field %q: %w", key, errOfflineSettingsMalformed)
		}
		if seen[key] {
			return patch, fmt.Errorf("duplicate field %q: %w", key, errOfflineSettingsMalformed)
		}
		seen[key] = true

		valTok, err := dec.Token()
		if err != nil {
			return patch, fmt.Errorf("field %q: value must be an integer: %w", key, errOfflineSettingsMalformed)
		}
		num, ok := valTok.(json.Number)
		if !ok {
			return patch, fmt.Errorf("field %q: value must be an integer: %w", key, errOfflineSettingsMalformed)
		}
		val, err := parseOfflineSettingsValue(key, num.String())
		if err != nil {
			return patch, err
		}

		switch key {
		case "priority":
			patch.Priority = &val
		case "max_runners":
			patch.MaxRunners = &val
		case "min_idle_runners":
			patch.MinIdleRunners = &val
		}
	}

	tok, err = dec.Token()
	if err != nil {
		return patch, fmt.Errorf("invalid body: %w", errOfflineSettingsMalformed)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return patch, fmt.Errorf("body must be a JSON object: %w", errOfflineSettingsMalformed)
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return patch, fmt.Errorf("unexpected trailing data: %w", errOfflineSettingsMalformed)
	}

	if !patch.HasChanges() {
		return patch, fmt.Errorf("at least one field is required: %w", errOfflineSettingsMalformed)
	}
	return patch, nil
}

// parseOfflineIfMatch parses the If-Match header. Exactly one strong,
// quoted "<epoch>:<seq>" tag is accepted; lists, weak tags and wildcards
// are rejected as malformed.
func parseOfflineIfMatch(r *http.Request) (runnerParams.OfflinePoolSettingsRevision, error) {
	var rev runnerParams.OfflinePoolSettingsRevision
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if value == "" {
		return rev, errOfflineIfMatchMissing
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return rev, fmt.Errorf("expected a single quoted etag: %w", errOfflineIfMatchMalformed)
	}
	inner := value[1 : len(value)-1]
	if strings.ContainsAny(inner, ",*") {
		return rev, fmt.Errorf("etag lists and wildcards are not accepted: %w", errOfflineIfMatchMalformed)
	}
	parts := strings.Split(inner, ":")
	if len(parts) != 2 {
		return rev, fmt.Errorf("expected \"<epoch>:<seq>\": %w", errOfflineIfMatchMalformed)
	}
	epoch, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || !isCanonicalUintLiteral(parts[0]) {
		return rev, fmt.Errorf("invalid epoch %q: %w", parts[0], errOfflineIfMatchMalformed)
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || !isCanonicalUintLiteral(parts[1]) {
		return rev, fmt.Errorf("invalid seq %q: %w", parts[1], errOfflineIfMatchMalformed)
	}
	return runnerParams.OfflinePoolSettingsRevision{Epoch: epoch, Seq: seq}, nil
}

func writeOfflinePoolSettings(w http.ResponseWriter, settings runnerParams.OfflinePoolSettings, rev runnerParams.OfflinePoolSettingsRevision) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", rev.ETag())
	if err := json.NewEncoder(w).Encode(settings); err != nil {
		slog.With(slog.Any("error", err)).Error("failed to encode response")
	}
}

// writeOfflinePoolSettingsHTTPError maps store errors onto the provisional
// wire v0 status codes.
func writeOfflinePoolSettingsHTTPError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")

	var staleErr *common.OfflinePoolSettingsStaleRevisionError
	apiErr := params.APIErrorResponse{}
	switch {
	case errors.As(err, &staleErr):
		w.Header().Set("ETag", staleErr.Current.ETag())
		w.WriteHeader(http.StatusPreconditionFailed)
		apiErr.Error = "Precondition Failed"
		apiErr.Details = fmt.Sprintf("stale revision; current ETag is %s", staleErr.Current.ETag())
	case errors.Is(err, gErrors.ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
		apiErr.Error = "Not Found"
		apiErr.Details = err.Error()
	case errors.Is(err, common.ErrPoolNotMemberOfEntity):
		w.WriteHeader(http.StatusForbidden)
		apiErr.Error = "Forbidden"
		apiErr.Details = err.Error()
	case errors.Is(err, gErrors.ErrUnauthorized):
		// Backstop for the runner level admin check. The handler checks
		// admin access itself so this is normally unreachable.
		w.WriteHeader(http.StatusForbidden)
		apiErr.Error = "Forbidden"
		apiErr.Details = "caller lacks access"
	case errors.Is(err, gErrors.ErrBadRequest):
		w.WriteHeader(http.StatusBadRequest)
		apiErr.Error = "Bad Request"
		apiErr.Details = err.Error()
	case errors.Is(err, gErrors.ErrUnprocessable):
		w.WriteHeader(http.StatusUnprocessableEntity)
		apiErr.Error = "Unprocessable Entity"
		apiErr.Details = err.Error()
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		apiErr.Error = "Service Unavailable"
		// Deliberately no details: generic failure.
	}
	if encodeErr := json.NewEncoder(w).Encode(apiErr); encodeErr != nil {
		slog.With(slog.Any("error", encodeErr)).Error("failed to encode response")
	}
}

// writeOfflineClientError writes a 4xx error for request problems detected
// in the handler itself (malformed body/header/path).
func writeOfflineClientError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	apiErr := params.APIErrorResponse{
		Details: err.Error(),
	}
	switch status {
	case http.StatusBadRequest:
		apiErr.Error = "Bad Request"
	case http.StatusPreconditionRequired:
		apiErr.Error = "Precondition Required"
	case http.StatusForbidden:
		apiErr.Error = "Forbidden"
	default:
		apiErr.Error = http.StatusText(status)
	}
	w.WriteHeader(status)
	if encodeErr := json.NewEncoder(w).Encode(apiErr); encodeErr != nil {
		slog.With(slog.Any("error", encodeErr)).Error("failed to encode response")
	}
}

// GetOfflinePoolSettingsHandler handles GET requests for the experimental
// offline pool settings.
func (a *APIController) GetOfflinePoolSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !auth.IsAdmin(r.Context()) {
		writeOfflineClientError(w, http.StatusForbidden, errors.New("caller lacks access"))
		return
	}

	entity, poolID, err := offlinePoolPathVars(r)
	if err != nil {
		writeOfflineClientError(w, http.StatusBadRequest, err)
		return
	}

	settings, rev, err := a.r.GetOfflinePoolSettings(r.Context(), entity, poolID)
	if err != nil {
		writeOfflinePoolSettingsHTTPError(w, err)
		return
	}
	writeOfflinePoolSettings(w, settings, rev)
}

// UpdateOfflinePoolSettingsHandler handles PATCH requests for the
// experimental offline pool settings.
func (a *APIController) UpdateOfflinePoolSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !auth.IsAdmin(r.Context()) {
		writeOfflineClientError(w, http.StatusForbidden, errors.New("caller lacks access"))
		return
	}

	entity, poolID, err := offlinePoolPathVars(r)
	if err != nil {
		writeOfflineClientError(w, http.StatusBadRequest, err)
		return
	}

	patch, err := decodeOfflinePoolSettingsPatch(w, r)
	if err != nil {
		writeOfflineClientError(w, http.StatusBadRequest, err)
		return
	}

	expected, err := parseOfflineIfMatch(r)
	if errors.Is(err, errOfflineIfMatchMissing) {
		writeOfflineClientError(w, http.StatusPreconditionRequired, err)
		return
	}
	if err != nil {
		writeOfflineClientError(w, http.StatusBadRequest, err)
		return
	}

	settings, newRev, err := a.r.UpdateOfflinePoolSettings(r.Context(), entity, poolID, expected, patch)
	if err != nil {
		writeOfflinePoolSettingsHTTPError(w, err)
		return
	}
	writeOfflinePoolSettings(w, settings, newRev)
}
