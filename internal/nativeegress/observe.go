package nativeegress

import (
	"encoding/json"
	"net/netip"
	"strings"
)

func rulesetSafe(raw []byte, table string) bool {
	var v struct {
		Items []map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Items == nil {
		return false
	}
	for _, o := range v.Items {
		if _, ok := o["flowtable"]; ok {
			return false
		}
		if b, ok := o["table"]; ok {
			var t struct{ Name string }
			if json.Unmarshal(b, &t) != nil || t.Name == table {
				return false
			}
		}
		if b, ok := o["rule"]; ok {
			var r struct{ Expr []map[string]json.RawMessage }
			if json.Unmarshal(b, &r) != nil {
				return false
			}
			for _, x := range r.Expr {
				if _, ok := x["flow"]; ok {
					return false
				}
				if _, ok := x["fwd"]; ok {
					return false
				}
			}
		}
	}
	return true
}
func onlyLoopback(raw []byte) bool {
	var links []struct {
		IfName string `json:"ifname"`
	}
	return json.Unmarshal(raw, &links) == nil && len(links) == 1 && links[0].IfName == "lo"
}
func hostAddressesSafe(raw []byte, p Policy, iface string) bool {
	var links []struct {
		IfName string `json:"ifname"`
		Info   []struct {
			Family, Local string
			PrefixLen     int `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if json.Unmarshal(raw, &links) != nil || len(links) == 0 || len(links) > 4096 {
		return false
	}
	for _, l := range links {
		if l.IfName == iface {
			return false
		}
		for _, a := range l.Info {
			if a.Family != "inet" {
				continue
			}
			ip, e := netip.ParseAddr(a.Local)
			if e != nil || !ip.Is4() || a.PrefixLen < 0 || a.PrefixLen > 32 {
				return false
			}
			pr := netip.PrefixFrom(ip, a.PrefixLen).Masked()
			if overlaps(pr, p.transit) || overlaps(pr, p.runner) {
				return false
			}
		}
	}
	return true
}

type linkObservation struct {
	Index int `json:"index"`
	Peer  int `json:"peer"`
}

func observeLink(raw []byte, name, address string) (linkObservation, error) {
	var strict []map[string]json.RawMessage
	if json.Unmarshal(raw, &strict) != nil || len(strict) != 1 {
		return linkObservation{}, ErrUnknown
	}
	if _, ok := strict[0]["master"]; ok {
		return linkObservation{}, ErrUnknown
	}
	var links []struct {
		Index int      `json:"ifindex"`
		Name  string   `json:"ifname"`
		Peer  int      `json:"link_index"`
		Flags []string `json:"flags"`
		Info  struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
		Addresses []struct {
			Family, Local string
			Prefix        int `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if json.Unmarshal(raw, &links) != nil || len(links) != 1 {
		return linkObservation{}, ErrUnknown
	}
	l := links[0]
	up := false
	for _, f := range l.Flags {
		if f == "UP" {
			up = true
		}
	}
	if l.Index <= 0 || l.Peer <= 0 || l.Name != name || l.Info.Kind != "veth" || !up {
		return linkObservation{}, ErrUnknown
	}
	count := 0
	for _, a := range l.Addresses {
		if a.Family == "inet" {
			if a.Local != address || a.Prefix != 30 {
				return linkObservation{}, ErrUnknown
			}
			count++
		}
	}
	if count != 1 {
		return linkObservation{}, ErrUnknown
	}
	return linkObservation{l.Index, l.Peer}, nil
}

func hostRoutesSafe(raw []byte, p Policy) bool {
	var routes []struct {
		Destination string `json:"dst"`
	}
	if json.Unmarshal(raw, &routes) != nil || routes == nil || len(routes) > 16384 {
		return false
	}
	for _, r := range routes {
		if r.Destination == "default" || r.Destination == "0.0.0.0/0" {
			continue
		}
		s := r.Destination
		if !strings.Contains(s, "/") {
			s += "/32"
		}
		v, e := prefix(s)
		if e != nil || overlaps(v, p.transit) || overlaps(v, p.runner) {
			return false
		}
	}
	return true
}
