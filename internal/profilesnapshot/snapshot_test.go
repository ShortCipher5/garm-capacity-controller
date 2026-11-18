package profilesnapshot

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"strings"
	"testing"
	"time"
)

func TestSnapshotNonceHashAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	nonce := strings.Repeat("a", 64)
	lineage := strings.Repeat("b", 64)
	state := admission.BrokerGrantState{Sequence: 12, PoolProfiles: map[string]admission.BrokerPoolProfile{}, HostBindings: map[string]admission.HostBinding{}}
	s, err := New("controller", nonce, lineage, state, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if _, err = Decode(raw, "controller", nonce, now); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"nonce", "controller", "stale", "future", "hash", "lineage", "duplicate", "alias"} {
		t.Run(mode, func(t *testing.T) {
			v := s
			switch mode {
			case "nonce":
				v.Nonce = strings.Repeat("c", 64)
			case "controller":
				v.Controller = "other"
			case "stale":
				v.IssuedAt = now.Add(-6 * time.Second)
			case "future":
				v.IssuedAt = now.Add(2 * time.Second)
			case "hash":
				v.ProfileDigest = strings.Repeat("c", 64)
			case "lineage":
				v.Lineage = ""
			}
			b, _ := json.Marshal(v)
			if mode == "duplicate" {
				b = []byte(strings.Replace(string(b), `"version":`, `"version":"provider-path-profiles/v1","version":`, 1))
			}
			if mode == "alias" {
				b = []byte(strings.Replace(string(b), `"sequence":`, `"Sequence":`, 1))
			}
			if _, err := Decode(b, "controller", nonce, now); err == nil {
				t.Fatal("untrusted snapshot accepted")
			}
		})
	}
}

func TestNativeDomainProjectionRequiresExactReferencedEnrollment(t *testing.T) {
	now := time.Now().UTC()
	d := admission.NativeDomainBinding{Sequence: 2, EnrolledAt: now, Spec: admission.NativeDomainSpec{Controller: "controller", Host: "host", HostBindingSequence: 1, DomainID: "domain", PolicyDigest: strings.Repeat("a", 64), OwnerClientLeaf: strings.Repeat("b", 64), OwnerServerLeaf: strings.Repeat("c", 64)}}
	p := admission.BrokerPoolProfile{Sequence: 3, Pool: "pool", Authority: admission.StockProviderAuthority{Controller: "controller", PoolID: "pool"}, Domains: []admission.BrokerProfileDomain{{Sequence: 2, Host: "host", HostBindingSequence: 1, DomainID: "domain", PolicyDigest: d.Spec.PolicyDigest}}}
	state := admission.BrokerGrantState{Sequence: 4, PoolProfiles: map[string]admission.BrokerPoolProfile{"pool": p}, NativeDomains: map[int64]admission.NativeDomainBinding{2: d}}
	for _, mode := range []string{"valid", "missing", "extra", "role", "scope", "sequence", "digest"} {
		t.Run(mode, func(t *testing.T) {
			state.NativeDomains = map[int64]admission.NativeDomainBinding{2: d}
			v := d
			switch mode {
			case "missing":
				delete(state.NativeDomains, 2)
			case "extra":
				state.NativeDomains[7] = d
			case "role":
				v.Spec.OwnerClientLeaf = v.Spec.OwnerServerLeaf
				state.NativeDomains[2] = v
			case "scope":
				v.Spec.Controller = "foreign"
				state.NativeDomains[2] = v
			case "sequence":
				v.Sequence = 3
				state.NativeDomains[2] = v
			case "digest":
				v.Spec.PolicyDigest = strings.Repeat("f", 64)
				state.NativeDomains[2] = v
			}
			snapshot, err := New("controller", strings.Repeat("d", 64), strings.Repeat("e", 64), state, now)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(snapshot)
				if _, err = Decode(raw, "controller", snapshot.Nonce, now); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid enrollment accepted")
			}
		})
	}
}
