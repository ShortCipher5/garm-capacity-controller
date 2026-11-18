package nativeegress

import (
	"encoding/json"
	"net/netip"
	"reflect"
)

type object = map[string]any

func meta(k string) object { return object{"meta": object{"key": k}} }
func payload(proto, field string) object {
	return object{"payload": object{"protocol": proto, "field": field}}
}
func match(left any, op string, right any) object {
	return object{"match": object{"op": op, "left": left, "right": right}}
}
func cidr(s string) any {
	p := netip.MustParsePrefix(s)
	if p.Bits() == 32 {
		return p.Addr().String()
	}
	return object{"prefix": object{"addr": p.Addr().String(), "len": p.Bits()}}
}
func (p Policy) firewall(id string) ([]object, error) {
	iface, table, e := p.names(id)
	if e != nil {
		return nil, e
	}
	out := []object{{"table": object{"family": "inet", "name": table, "comment": p.digest}}}
	chain := func(name, kind, hook string, prio int) {
		c := object{"family": "inet", "table": table, "name": name}
		if hook != "" {
			c["type"] = kind
			c["hook"] = hook
			c["prio"] = prio
			c["policy"] = "accept"
		}
		out = append(out, object{"chain": c})
	}
	chain("host_input", "filter", "input", -310)
	chain("host_output", "filter", "output", -150)
	chain("transit", "filter", "forward", -310)
	chain("outbound", "", "", 0)
	chain("inbound", "", "", 0)
	chain("egress_nat", "nat", "postrouting", 100)
	rule := func(chain string, expr ...object) {
		out = append(out, object{"rule": object{"family": "inet", "table": table, "chain": chain, "expr": expr}})
	}
	drop := object{"drop": nil}
	accept := object{"accept": nil}
	rule("host_input", match(meta("iifname"), "==", iface), drop)
	// Related ICMP errors are required for path MTU and diagnostics. These
	// cannot open new host/private connections or permit outbound ICMP probes.
	relatedICMP := []object{match(meta("nfproto"), "==", "ipv4"), match(object{"ct": object{"key": "state"}}, "==", "related"), match(payload("icmp", "type"), "in", object{"set": []string{"destination-unreachable", "time-exceeded", "parameter-problem"}}), accept}
	rule("host_output", append([]object{match(meta("oifname"), "==", iface)}, relatedICMP...)...)
	rule("host_output", match(meta("oifname"), "==", iface), drop)
	rule("transit", match(meta("iifname"), "==", iface), object{"jump": object{"target": "outbound"}})
	rule("transit", match(meta("oifname"), "==", iface), object{"jump": object{"target": "inbound"}})
	rule("outbound", match(meta("nfproto"), "!=", "ipv4"), drop)
	rule("outbound", match(payload("ip", "saddr"), "!=", p.GuestAddress()), drop)
	rule("inbound", match(meta("nfproto"), "!=", "ipv4"), drop)
	rule("inbound", match(payload("ip", "daddr"), "!=", p.GuestAddress()), drop)
	rule("inbound", relatedICMP...)
	for _, s := range append(append([]string{}, forbidden...), p.spec.ControlPlaneCIDRs...) {
		rule("outbound", match(payload("ip", "daddr"), "==", cidr(s)), drop)
		rule("inbound", match(payload("ip", "saddr"), "==", cidr(s)), drop)
	}
	rule("outbound", match(object{"ct": object{"key": "state"}}, "==", "invalid"), drop)
	for _, dns := range p.spec.DNS {
		for _, proto := range []string{"tcp", "udp"} {
			rule("outbound", match(payload("ip", "daddr"), "==", dns), match(payload(proto, "dport"), "==", 53), accept)
		}
	}
	if p.spec.Mode == PublicInternet {
		for _, proto := range []string{"tcp", "udp"} {
			rule("outbound", match(meta("l4proto"), "==", proto), accept)
		}
	} else {
		for _, r := range p.spec.Rules {
			rule("outbound", match(payload("ip", "daddr"), "==", cidr(r.CIDR)), match(payload(r.Protocol, "dport"), "==", int(r.Port)), accept)
		}
	}
	rule("outbound", drop)
	rule("inbound", match(object{"ct": object{"key": "state"}}, "in", object{"set": []string{"established", "related"}}), accept)
	rule("inbound", drop)
	rule("egress_nat", match(meta("iifname"), "==", iface), match(payload("ip", "saddr"), "==", p.GuestAddress()), object{"masquerade": nil})
	return out, nil
}
func firewallInput(objects []object) []byte {
	a := make([]object, 0, len(objects))
	for _, o := range objects {
		a = append(a, object{"add": o})
	}
	raw, _ := json.Marshal(object{"nftables": a})
	return raw
}
func sameFirewall(raw []byte, want []object) bool {
	var v struct {
		Items []object `json:"nftables"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	filtered := []object{}
	for _, o := range v.Items {
		if len(o) != 1 {
			return false
		}
		if _, ok := o["metainfo"]; ok {
			continue
		}
		for k, x := range o {
			if k != "table" && k != "chain" && k != "rule" {
				return false
			}
			m, ok := x.(map[string]any)
			if !ok {
				return false
			}
			delete(m, "handle")
		}
		filtered = append(filtered, o)
	}
	// Roundtrip normalizes JSON numeric values only; no unknown rule or property
	// is ignored. A differing kernel representation fails closed for qualification.
	b, _ := json.Marshal(want)
	var normalized []object
	json.Unmarshal(b, &normalized)
	return reflect.DeepEqual(filtered, normalized)
}

// bridgeGuard makes a later accidental master assignment fail closed even when
// bridge-nf-call-iptables is disabled. These are native nft bridge-family hooks,
// not routed inet hooks. No legitimate capsule traffic uses a host bridge.
func (p Policy) bridgeGuard(id string) ([]object, error) {
	iface, table, e := p.names(id)
	if e != nil {
		return nil, e
	}
	out := []object{{"table": object{"family": "bridge", "name": table, "comment": p.digest}}}
	for _, hook := range []string{"input", "output", "forward"} {
		out = append(out, object{"chain": object{"family": "bridge", "table": table, "name": hook, "type": "filter", "hook": hook, "prio": -310, "policy": "accept"}})
	}
	for _, entry := range [][2]string{{"input", "iifname"}, {"output", "oifname"}, {"forward", "iifname"}, {"forward", "oifname"}} {
		out = append(out, object{"rule": object{"family": "bridge", "table": table, "chain": entry[0], "expr": []object{match(meta(entry[1]), "==", iface), {"drop": nil}}}})
	}
	return out, nil
}
