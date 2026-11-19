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
	NativeDomains map[int64]admission.NativeDomainBinding `json:"native_domains"`
	Profiles      map[string]admission.BrokerPoolProfile  `json:"profiles"`
	PoolHosts     map[string][]string                     `json:"pool_hosts"`
	HostBindings  map[string]int64                        `json:"host_bindings"`
}
type Snapshot struct {
	Accounting       Accounting `json:"accounting"`
	AccountingDigest string     `json:"accounting_digest"`
	Lineage          string     `json:"lineage"`
	Version          string     `json:"version"`
	Controller       string     `json:"controller"`
	Nonce            string     `json:"nonce"`
	IssuedAt         time.Time  `json:"issued_at"`
	Sequence         int64      `json:"sequence"`
	ProfileDigest    string     `json:"profile_digest"`
	Payload          Payload    `json:"payload"`
}

func Hex(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func Digest(p Payload) string {
	b, _ := json.Marshal(p)
	return digestBytes(b)
}
func digestBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func New(controller, nonce, lineage string, state admission.BrokerGrantState, now time.Time) (Snapshot, error) {
	s := Snapshot{Lineage: lineage, Version: "provider-path-profiles/v4", Controller: controller, Nonce: nonce, IssuedAt: now.UTC(), Sequence: state.Sequence, Payload: Payload{NativeDomains: state.NativeDomains, Profiles: state.PoolProfiles, PoolHosts: map[string][]string{}, HostBindings: map[string]int64{}}}
	for name, p := range state.Config.Pools {
		s.Payload.PoolHosts[name] = append([]string(nil), p.Hosts...)
	}
	for name, b := range state.HostBindings {
		s.Payload.HostBindings[name] = b.Sequence
	}
	accounting, err := projectAccounting(state)
	if err != nil {
		return Snapshot{}, err
	}
	s.Accounting = accounting
	s.AccountingDigest = AccountingDigest(accounting)
	s.ProfileDigest = Digest(s.Payload)
	if validate(s, controller, nonce, now) != nil {
		return Snapshot{}, ErrSnapshot
	}
	return s, nil
}
func validate(s Snapshot, controller, nonce string, now time.Time) error {
	if validateAccounting(s.Accounting, s.Sequence) != nil || s.AccountingDigest != AccountingDigest(s.Accounting) || !Hex(s.Lineage) || s.Version != "provider-path-profiles/v4" || s.Controller != controller || controller == "" || !Hex(nonce) || s.Nonce != nonce || s.Sequence < 0 || s.IssuedAt.IsZero() || now.Sub(s.IssuedAt) > 5*time.Second || s.IssuedAt.After(now.Add(time.Second)) || s.ProfileDigest != Digest(s.Payload) || len(s.Payload.NativeDomains) > 1000 || len(s.Payload.Profiles) > 256 || len(s.Payload.PoolHosts) > 256 || len(s.Payload.HostBindings) > 1000 {
		return ErrSnapshot
	}
	referenced := map[int64]bool{}
	for id, p := range s.Payload.Profiles {
		if p.Pool != id || p.Authority.PoolID != id || p.Authority.Controller != controller || p.Sequence <= 0 || p.Sequence > s.Sequence || len(p.Domains) > 1000 {
			return ErrSnapshot
		}
		for _, ref := range p.Domains {
			d, ok := s.Payload.NativeDomains[ref.Sequence]
			if !ok || d.Sequence != ref.Sequence || d.Sequence <= 0 || d.Sequence > s.Sequence || d.EnrolledAt.IsZero() ||
				d.Spec.Controller != controller || d.Spec.Host != ref.Host || d.Spec.DomainID != ref.DomainID ||
				d.Spec.PolicyDigest != ref.PolicyDigest || d.Spec.HostBindingSequence != ref.HostBindingSequence ||
				!Hex(d.Spec.OwnerClientLeaf) || !Hex(d.Spec.OwnerServerLeaf) || d.Spec.OwnerClientLeaf == d.Spec.OwnerServerLeaf {
				return ErrSnapshot
			}
			referenced[ref.Sequence] = true
		}
	}
	if len(referenced) != len(s.Payload.NativeDomains) {
		return ErrSnapshot
	}
	if len(s.Payload.PoolHosts) != len(s.Accounting.Budgets.Pools) {
		return ErrSnapshot
	}
	for id, hosts := range s.Payload.PoolHosts {
		if _, ok := s.Accounting.Budgets.Pools[id]; !ok {
			return ErrSnapshot
		}
		for _, host := range hosts {
			if _, ok := s.Accounting.Budgets.Hosts[host]; !ok {
				return ErrSnapshot
			}
		}

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
