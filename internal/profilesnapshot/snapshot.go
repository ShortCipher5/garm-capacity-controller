// Package profilesnapshot defines a bounded, nonce-bound read-only protocol.
// A profile snapshot is not native execution or capacity-increase authority.
package profilesnapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"io"
	"time"
)

const Path = "/v1/provider-path-profiles"
const NonceHeader = "X-Capacity-Profile-Nonce"

var ErrSnapshot = errors.New("authenticated profile snapshot unavailable")

type Payload struct {
	Profiles     map[string]admission.BrokerPoolProfile `json:"profiles"`
	PoolHosts    map[string][]string                    `json:"pool_hosts"`
	HostBindings map[string]int64                       `json:"host_bindings"`
}
type Snapshot struct {
	Lineage       string    `json:"lineage"`
	Version       string    `json:"version"`
	Controller    string    `json:"controller"`
	Nonce         string    `json:"nonce"`
	IssuedAt      time.Time `json:"issued_at"`
	Sequence      int64     `json:"sequence"`
	ProfileDigest string    `json:"profile_digest"`
	Payload       Payload   `json:"payload"`
}

func Hex(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func Digest(p Payload) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func New(controller, nonce, lineage string, state admission.BrokerGrantState, now time.Time) (Snapshot, error) {
	s := Snapshot{Lineage: lineage, Version: "provider-path-profiles/v1", Controller: controller, Nonce: nonce, IssuedAt: now.UTC(), Sequence: state.Sequence, Payload: Payload{Profiles: state.PoolProfiles, PoolHosts: map[string][]string{}, HostBindings: map[string]int64{}}}
	for name, p := range state.Config.Pools {
		s.Payload.PoolHosts[name] = append([]string(nil), p.Hosts...)
	}
	for name, b := range state.HostBindings {
		s.Payload.HostBindings[name] = b.Sequence
	}
	s.ProfileDigest = Digest(s.Payload)
	if validate(s, controller, nonce, now) != nil {
		return Snapshot{}, ErrSnapshot
	}
	return s, nil
}
func validate(s Snapshot, controller, nonce string, now time.Time) error {
	if !Hex(s.Lineage) || s.Version != "provider-path-profiles/v1" || s.Controller != controller || controller == "" || !Hex(nonce) || s.Nonce != nonce || s.Sequence < 0 || s.IssuedAt.IsZero() || now.Sub(s.IssuedAt) > 5*time.Second || s.IssuedAt.After(now.Add(time.Second)) || s.ProfileDigest != Digest(s.Payload) || len(s.Payload.Profiles) > 256 || len(s.Payload.PoolHosts) > 256 || len(s.Payload.HostBindings) > 1000 {
		return ErrSnapshot
	}
	for id, p := range s.Payload.Profiles {
		if p.Pool != id || p.Authority.PoolID != id || p.Authority.Controller != controller || p.Sequence <= 0 || p.Sequence > s.Sequence || len(p.Domains) > 1000 {
			return ErrSnapshot
		}
	}
	for _, hosts := range s.Payload.PoolHosts {
		if len(hosts) > 1000 {
			return ErrSnapshot
		}
	}
	return nil
}
func Decode(raw []byte, controller, nonce string, now time.Time) (Snapshot, error) {
	var s Snapshot
	if len(raw) > 4<<20 {
		return s, ErrSnapshot
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || validate(s, controller, nonce, now) != nil {
		return Snapshot{}, ErrSnapshot
	}
	canonical, e := json.Marshal(s)
	if e != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return Snapshot{}, ErrSnapshot
	}
	return s, nil
}
