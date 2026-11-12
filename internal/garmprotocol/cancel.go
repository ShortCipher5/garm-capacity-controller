package garmprotocol

import (
	"bytes"
	"encoding/json"
	"io"
)

// CancellationResponse acknowledges only an atomic never-dispatched tombstone.
// It is never a delete/release permission for a runner or an unknown create.
type CancellationResponse struct {
	Protocol       string `json:"protocol"`
	Outcome        string `json:"outcome"`
	Name           string `json:"name"`
	PoolID         string `json:"pool_id"`
	Dispatched     *bool  `json:"dispatched"`
	ReservationKey string `json:"reservation_key"`
	Replay         *bool  `json:"replay"`
}

func ParseCancellation(exit int, data []byte, request Request) (CancellationResponse, error) {
	if exit != 0 || request.Validate() != nil || len(data) > MaximumResponseBytes {
		return CancellationResponse{}, ErrProtocol
	}
	allowed := map[string]bool{"protocol": true, "outcome": true, "name": true, "pool_id": true, "dispatched": true, "reservation_key": true, "replay": true}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return CancellationResponse{}, ErrProtocol
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return CancellationResponse{}, ErrProtocol
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return CancellationResponse{}, ErrProtocol
		}
	}
	if _, err = decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF || len(seen) != len(allowed) {
		return CancellationResponse{}, ErrProtocol
	}
	var result CancellationResponse
	if json.Unmarshal(data, &result) != nil || result.Protocol != Version || result.Outcome != "cancelled" || result.Name != request.Name || result.PoolID != request.PoolID || result.Dispatched == nil || *result.Dispatched || result.Replay == nil || !validIdentity(result.ReservationKey) {
		return CancellationResponse{}, ErrProtocol
	}
	return result, nil
}
func EncodeCancellation(response CancellationResponse, request Request) ([]byte, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return nil, ErrProtocol
	}
	data = append(data, '\n')
	if _, err := ParseCancellation(0, data, request); err != nil {
		return nil, err
	}
	return data, nil
}
