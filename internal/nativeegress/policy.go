// Package nativeegress defines explicit network scope for fresh isolated capsules.
// A compiled policy is not installation, reachability, or allocation authority.
package nativeegress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
)

var ErrRefused = errors.New("native egress refused; no implicit network fallback")
var ErrUnknown = errors.New("native egress installation uncertain; retain birth and inspect, do not retry")

const PublicInternet = "public-internet-v4"
const Restricted = "allowlist-v4"

type Rule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     uint16 `json:"port"`
}
type Specification struct {
	Mode        string   `json:"mode"`
	TransitCIDR string   `json:"transit_cidr"`
	RunnerCIDR  string   `json:"runner_cidr"`
	DNS         []string `json:"dns"`
	// Include every public control-plane/admin endpoint. Private destinations are
	// denied independently. Configuration is deployment-owned, never discovered
	// from untrusted job input; moving endpoints require a new reviewed birth.
	ControlPlaneCIDRs []string `json:"control_plane_cidrs"`
	Rules             []Rule   `json:"rules"`
}
type Policy struct {
	spec            Specification
	digest          string
	transit, runner netip.Prefix
}

var forbidden = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"}

func public(a netip.Addr) bool {
	if !a.Is4() || !a.IsGlobalUnicast() {
		return false
	}
	for _, s := range forbidden {
		if netip.MustParsePrefix(s).Contains(a) {
			return false
		}
	}
	return true
}
func prefix(s string) (netip.Prefix, error) {
	p, e := netip.ParsePrefix(s)
	if e != nil || !p.Addr().Is4() || p.Masked() != p || p.String() != s {
		return netip.Prefix{}, ErrRefused
	}
	return p, nil
}
func overlaps(a, b netip.Prefix) bool { return a.Contains(b.Addr()) || b.Contains(a.Addr()) }
func Compile(s Specification) (Policy, error) {
	if s.Mode != PublicInternet && s.Mode != Restricted {
		return Policy{}, ErrRefused
	}
	tr, e := prefix(s.TransitCIDR)
	if e != nil || tr.Bits() != 30 || !tr.Addr().IsPrivate() {
		return Policy{}, ErrRefused
	}
	rr, e := prefix(s.RunnerCIDR)
	if e != nil || rr.Bits() != 24 || !rr.Addr().IsPrivate() || overlaps(tr, rr) {
		return Policy{}, ErrRefused
	}
	if len(s.DNS) < 1 || len(s.DNS) > 3 || len(s.ControlPlaneCIDRs) < 1 || len(s.ControlPlaneCIDRs) > 128 || len(s.Rules) > 256 || s.Mode == PublicInternet && len(s.Rules) != 0 || s.Mode == Restricted && len(s.Rules) == 0 {
		return Policy{}, ErrRefused
	}
	seen := map[string]bool{}
	for _, v := range s.DNS {
		a, e := netip.ParseAddr(v)
		if e != nil || a.String() != v || !public(a) || seen[v] {
			return Policy{}, ErrRefused
		}
		seen[v] = true
	}
	denied := []netip.Prefix{}
	for _, v := range s.ControlPlaneCIDRs {
		p, e := prefix(v)
		if e != nil {
			return Policy{}, ErrRefused
		}
		denied = append(denied, p)
	}
	for _, dns := range s.DNS {
		for _, p := range denied {
			if p.Contains(netip.MustParseAddr(dns)) {
				return Policy{}, ErrRefused
			}
		}
	}
	for _, r := range s.Rules {
		p, e := prefix(r.CIDR)
		if e != nil || r.Port == 0 || (r.Protocol != "tcp" && r.Protocol != "udp") || !public(p.Addr()) {
			return Policy{}, ErrRefused
		}
		for _, f := range forbidden {
			if overlaps(p, netip.MustParsePrefix(f)) {
				return Policy{}, ErrRefused
			}
		}
		for _, d := range denied {
			if overlaps(p, d) {
				return Policy{}, ErrRefused
			}
		}
	}
	raw, _ := json.Marshal(s)
	var copy Specification
	json.Unmarshal(raw, &copy)
	h := sha256.Sum256(raw)
	return Policy{copy, hex.EncodeToString(h[:]), tr, rr}, nil
}
func (p Policy) Digest() string { return p.digest }
func (p Policy) DNS() []string  { return append([]string(nil), p.spec.DNS...) }
func (p Policy) RunnerGateway() string {
	if p.digest == "" {
		return ""
	}
	return p.runner.Addr().Next().String() + "/24"
}
func (p Policy) HostAddress() string  { return p.transit.Addr().Next().String() }
func (p Policy) GuestAddress() string { return p.transit.Addr().Next().Next().String() }
func birth(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && hex.EncodeToString(b) == s
}
func (p Policy) names(id string) (string, string, error) {
	if !birth(id) || p.digest == "" {
		return "", "", ErrRefused
	}
	return "gc" + id[:12], "garm_" + id[:24], nil
}
