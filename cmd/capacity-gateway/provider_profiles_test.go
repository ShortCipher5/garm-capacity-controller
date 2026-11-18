package main

import (
	"crypto/tls"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"strings"
	"testing"
)

func TestProviderInspectionRoleSeparation(t *testing.T) {
	pin := strings.Repeat("a", 64)
	server := tls.Certificate{Certificate: [][]byte{[]byte("distinct-server")}}
	base := configuration{ProviderInspectionFingerprint: pin}
	if validateProviderProfileRole(base, server) != nil {
		t.Fatal("distinct role refused")
	}
	for _, c := range []configuration{
		{ProviderInspectionFingerprint: "bad"},
		{ProviderInspectionFingerprint: pin, ProviderFingerprints: []string{pin}},
		{ProviderInspectionFingerprint: pin, SourceFingerprints: []string{pin}},
		{ProviderInspectionFingerprint: pin, StockProviderScopes: []gatewayrpc.StockScope{{ClientFingerprint: pin}}},
	} {
		if validateProviderProfileRole(c, server) == nil {
			t.Fatal("aliased role accepted")
		}
	}
	if validateProviderProfileRole(base, tls.Certificate{}) == nil {
		t.Fatal("missing server identity accepted")
	}
}
