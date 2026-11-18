// Package nativedns defines the immutable resolver bytes shared by capsule image
// packaging and its authenticated pre-exec bootstrap contract.
package nativedns

import (
	"errors"
	"net/netip"
	"strings"
)

var ErrDNS = errors.New("explicit public IPv4 resolvers required")

func Content(resolvers []string) ([]byte, error) {
	if len(resolvers) < 1 || len(resolvers) > 3 {
		return nil, ErrDNS
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, s := range resolvers {
		a, e := netip.ParseAddr(s)
		if e != nil || !a.Is4() || a.String() != s || !a.IsGlobalUnicast() || a.IsPrivate() || seen[s] {
			return nil, ErrDNS
		}
		for _, v := range []string{"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4"} {
			if netip.MustParsePrefix(v).Contains(a) {
				return nil, ErrDNS
			}
		}
		seen[s] = true
		b.WriteString("nameserver " + s + "\n")
	}
	return []byte(b.String()), nil
}
