//go:build linux

package nativeegress

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestInstallOrdersFirewallBeforeLinkAndUp(t *testing.T) {
	p := fixture(t)
	id := strings.Repeat("a", 64)
	fw, _ := p.firewall(id)
	firewall, _ := json.Marshal(object{"nftables": fw})
	bg, _ := p.bridgeGuard(id)
	bridgeJSON, _ := json.Marshal(object{"nftables": bg})
	for fail := 0; fail < 35; fail++ {
		events := []string{}
		step := 0
		installed := false
		created := false
		guest := false
		run := func(fd int, a []string, in []byte) ([]byte, error) {
			step++
			events = append(events, strings.Join(a, " "))
			if fail > 0 && step == fail {
				return nil, ErrUnknown
			}
			s := strings.Join(a, " ")
			switch s {
			case "-j list ruleset":
				return []byte(`{"nftables":[]}`), nil
			case "-j address show":
				return []byte(`[{"ifname":"lo","addr_info":[]}]`), nil
			case "-4 -j route show table all":
				return []byte(`[]`), nil
			case "-j link show":
				if !guest {
					t.Fatal("not guest")
				}
				return []byte(`[{"ifname":"lo"}]`), nil
			case "-j -f -":
				installed = true
				return nil, nil
			}
			if strings.Contains(s, "list table") {
				if strings.Contains(s, "table bridge") {
					return bridgeJSON, nil
				}
				return firewall, nil
			}
			if strings.HasPrefix(s, "link set dev gc") && !created {
				t.Fatal("link up before create")
			}
			return nil, nil
		}
		e := install(p, id, run, func() error { guest = true; return nil }, func() error { guest = false; return nil }, func(name string) error {
			if !installed {
				t.Fatal("unprotected veth")
			}
			created = true
			events = append(events, "create")
			return nil
		})
		if fail > 0 && fail <= step && e == nil {
			t.Fatal("ignored failure", fail)
		}
		if fail == 0 && (e != nil || !created || guest) {
			t.Fatal("happy path", e)
		}
		for _, s := range events {
			if strings.Contains(s, "delete") || strings.Contains(s, "flush") {
				t.Fatal("cleanup")
			}
		}
	}
}
func TestNoFabricatedEgressCapability(t *testing.T) {
	if _, e := CreateFresh(context.Background(), nil, fixture(t), Configuration{}); e == nil {
		t.Fatal("fabricated")
	}
}
func TestRegistryPermanentlyReservesUnknownTransit(t *testing.T) {
	d := t.TempDir()
	r := Record{TransitCIDR: "10.251.0.0/30", BirthID: strings.Repeat("a", 64)}
	if reserveNetwork(d, r) != nil {
		t.Fatal("reserve")
	}
	r.BirthID = strings.Repeat("b", 64)
	if reserveNetwork(d, r) == nil {
		t.Fatal("unknown reused")
	}
	r.TransitCIDR = "10.251.0.4/30"
	if reserveNetwork(d, r) != nil {
		t.Fatal("distinct")
	}
	if reserveNetwork(d, r) == nil {
		t.Fatal("replay")
	}
}
