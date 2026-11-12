package garmprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const PreparationVersion = "garm-capacity-preparation/v1"

var ErrPreparationProtocol = errors.New("invalid preparation protocol")

// PreparationRequest contains NONSECRET original source metadata only. The
// controller, source scope and authenticated issuer are derived at the server.
// No JIT configuration, JWT, bootstrap token or credentials belong here.
type PreparationRequest struct {
	Protocol          string `json:"protocol"`
	Name              string `json:"name"`
	PoolID            string `json:"pool_id"`
	InstanceID        string `json:"instance_id"`
	AgentID           int64  `json:"agent_id"`
	Generation        uint64 `json:"generation"`
	ProviderName      string `json:"provider_name"`
	EntityType        string `json:"entity_type"`
	EntityID          string `json:"entity_id"`
	ForgeEndpointName string `json:"forge_endpoint_name"`
	JITEnabled        bool   `json:"jit_enabled"`
}

// PreparationResponse is a durable metadata acknowledgement or lookup, NEVER a
// physical dispatch/absence/release permit. Replay cannot change its identity.
type PreparationResponse struct {
	PreparationRequest
	Outcome        string `json:"outcome"`
	ReservationKey string `json:"reservation_key"`
	RecordSequence int64  `json:"record_sequence"`
	Replay         *bool  `json:"replay"`
	Dispatched     *bool  `json:"dispatched"`
}

var preparationRequestKeys = []string{"protocol", "name", "pool_id", "instance_id", "agent_id", "generation", "provider_name", "entity_type", "entity_id", "forge_endpoint_name", "jit_enabled"}

func canonicalPreparationUUID(value string, random bool) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return !random || (value[14] == '4' && strings.ContainsRune("89ab", rune(value[19])))
}
func preparationIdentity(value string) bool {
	return validIdentity(value) && strings.TrimSpace(value) == value
}
func (r PreparationRequest) Validate() error {
	if r.Protocol != PreparationVersion || !preparationIdentity(r.Name) || !preparationIdentity(r.PoolID) || !preparationIdentity(r.ProviderName) || len(r.ProviderName) > 128 || !canonicalPreparationUUID(r.InstanceID, true) || !canonicalPreparationUUID(r.EntityID, false) || (!preparationIdentity(r.ForgeEndpointName) || len(r.ForgeEndpointName) > 128) || r.Generation > math.MaxInt64 {
		return ErrPreparationProtocol
	}
	if r.EntityType != "repository" && r.EntityType != "organization" && r.EntityType != "enterprise" {
		return ErrPreparationProtocol
	}
	if (r.JITEnabled && r.AgentID <= 0) || (!r.JITEnabled && r.AgentID != 0) {
		return ErrPreparationProtocol
	}
	return nil
}
func exactPreparationObject(data []byte, keys []string) bool {
	if len(data) == 0 || len(data) > MaximumResponseBytes || !utf8.Valid(data) {
		return false
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(seen) != len(allowed) {
		return false
	}
	return decoder.Decode(new(any)) == io.EOF
}
func DecodePreparationRequest(data []byte) (PreparationRequest, error) {
	if !exactPreparationObject(data, preparationRequestKeys) {
		return PreparationRequest{}, ErrPreparationProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var r PreparationRequest
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || r.Validate() != nil {
		return PreparationRequest{}, ErrPreparationProtocol
	}
	return r, nil
}
func EncodePreparationRequest(r PreparationRequest) ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrPreparationProtocol
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, ErrPreparationProtocol
	}
	if _, err := DecodePreparationRequest(data); err != nil {
		return nil, err
	}
	return data, nil
}
func ParsePreparationResponse(exitCode int, data []byte, request PreparationRequest, outcome string) (PreparationResponse, error) {
	keys := append(append([]string(nil), preparationRequestKeys...), "outcome", "reservation_key", "record_sequence", "replay", "dispatched")
	if exitCode != 0 || (outcome != "recorded" && outcome != "validated") || request.Validate() != nil || !exactPreparationObject(data, keys) {
		return PreparationResponse{}, ErrPreparationProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var r PreparationResponse
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || r.PreparationRequest != request || r.Outcome != outcome || r.RecordSequence <= 0 || r.Replay == nil || r.Dispatched == nil || *r.Dispatched {
		return PreparationResponse{}, ErrPreparationProtocol
	}
	key, err := hex.DecodeString(r.ReservationKey)
	if err != nil || len(key) != 32 || hex.EncodeToString(key) != r.ReservationKey {
		return PreparationResponse{}, ErrPreparationProtocol
	}
	if outcome == "validated" && !*r.Replay {
		return PreparationResponse{}, ErrPreparationProtocol
	}
	return r, nil
}
func EncodePreparationResponse(r PreparationResponse, request PreparationRequest) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, ErrPreparationProtocol
	}
	if _, err := ParsePreparationResponse(0, data, request, r.Outcome); err != nil {
		return nil, err
	}
	return data, nil
}
