// Package garmprotocol defines the opt-in, pre-JIT admission wire contract.
// It is not a provider implementation or permission to dispatch an Incus create.
package garmprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const Version = "garm-capacity/v1"
const DeferredExitCode = 75
const MaximumResponseBytes = 4096

var ErrProtocol = errors.New("invalid admission protocol response")

// Request contains no bootstrap, GitHub, controller or Incus credentials.
// Controller identity and host placement come from trusted provider configuration.
type Request struct {
	Protocol string `json:"protocol"`
	Name string `json:"name"`
	PoolID string `json:"pool_id"`
}

// Response is a preflight receipt only. A later Create must atomically claim
// the same durable reservation at the gateway; replay is not a dispatch permit.
type Response struct {
	Protocol string `json:"protocol"`
	Outcome string `json:"outcome"`
	Name string `json:"name"`
	PoolID string `json:"pool_id"`
	Dispatched *bool `json:"dispatched"`
	ReservationKey string `json:"reservation_key,omitempty"`
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (r Request) Validate() error {
	if r.Protocol != Version || !validIdentity(r.Name) || !validIdentity(r.PoolID) {
		return ErrProtocol
	}
	return nil
}

// Parse validates exact identity, explicit no-dispatch assertion and exit-code
// pairing. Raw child output must not be included in errors or logs.
func Parse(exitCode int, output []byte, request Request) (Response, error) {
	if request.Validate() != nil || len(output) > MaximumResponseBytes || !uniqueResponseKeys(output) {
		return Response{}, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var response Response
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF {
		return Response{}, ErrProtocol
	}
	if response.Protocol != Version || response.Name != request.Name || response.PoolID != request.PoolID || response.Dispatched == nil || *response.Dispatched {
		return Response{}, ErrProtocol
	}
	switch response.Outcome {
	case "reserved":
		if exitCode != 0 || response.RetryAfterSeconds != 0 || !validIdentity(response.ReservationKey) {
			return Response{}, ErrProtocol
		}
	case "deferred":
		if exitCode != DeferredExitCode || response.ReservationKey != "" || response.RetryAfterSeconds < 5 || response.RetryAfterSeconds > 60 {
			return Response{}, ErrProtocol
		}
	default:
		return Response{}, ErrProtocol
	}
	return response, nil
}

// Encode produces the bounded response accepted by Parse, preventing a gateway
// from accidentally describing a post-dispatch failure as a benign deferral.
func Encode(exitCode int, response Response, request Request) ([]byte, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, ErrProtocol
	}
	encoded = append(encoded, '\n')
	if _, err := Parse(exitCode, encoded, request); err != nil {
		return nil, err
	}
	return encoded, nil
}

// encoding/json otherwise accepts duplicate and case-folded keys. Those are
// ambiguous on a safety boundary, so the flat response schema is exact.
func uniqueResponseKeys(data []byte) bool {
	allowed := map[string]bool{"protocol": true, "outcome": true, "name": true, "pool_id": true, "dispatched": true, "reservation_key": true, "retry_after_seconds": true}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !allowed[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil && decoder.Decode(new(any)) == io.EOF
}
