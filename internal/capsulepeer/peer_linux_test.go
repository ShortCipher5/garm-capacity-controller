//go:build linux

package capsulepeer

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeboundary"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeids"
	"testing"
)

func TestExactNonHostMapping(t *testing.T) {
	if !mapping([]byte("         0  100000000 16777216\n"), 100000000, 16777216) {
		t.Fatal("kernel map")
	}
	for _, v := range []string{"0 0 16777216\n", "0 100000000 16777216\n1 2 3\n", "1 100000000 16777216\n", "0 100000000 1\n"} {
		if mapping([]byte(v), 100000000, 16777216) {
			t.Fatal(v)
		}
	}
}
func TestCgroupCannotEscapeParentNamespace(t *testing.T) {
	if !containedCgroup([]byte("0::/.capacity-owner/payload\n")) {
		t.Fatal("child")
	}
	for _, v := range []string{"0::/\n", "0::/../payload\n", "0::/x/../y\n", "1::/x\n", "0::/x\n0::/y\n", "0::/x"} {
		if containedCgroup([]byte(v)) {
			t.Fatal(v)
		}
	}
}
func TestActualNamespaceInitRequired(t *testing.T) {
	if !namespaceInit([]byte("Name:\tstage\nNSpid:\t121\t1\n"), 121) {
		t.Fatal("init")
	}
	for _, v := range []string{"NSpid: 121\n", "NSpid: 121 2\n", "NSpid: 122 1\n", "NSpid: 121 1\nNSpid: 121 1\n"} {
		if namespaceInit([]byte(v), 121) {
			t.Fatal(v)
		}
	}
}
func TestZeroValuesNeverMintPeer(t *testing.T) {
	if _, e := Observe(nil, nativeids.Reservation{}, "", nativeboundary.Observation{}); e == nil {
		t.Fatal("fake peer")
	}
	var p *Peer
	if p.Alive() {
		t.Fatal("nil live")
	}
	if _, e := p.UserNamespace(); e == nil {
		t.Fatal("nil namespace")
	}
	p.Close()
}
