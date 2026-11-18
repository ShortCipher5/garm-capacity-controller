package main

import (
	"crypto/x509"
	"testing"
)

func TestManagedWriterRejectsCrossRoleSPKIReuse(t *testing.T) {
	cert := func(key, der string) *x509.Certificate {
		return &x509.Certificate{Raw: []byte(der), RawSubjectPublicKeyInfo: []byte(key)}
	}
	if !separateRoleKeys(cert("client", "a"), cert("proxy", "b"), cert("stock", "c")) {
		t.Fatal("separate keys rejected")
	}
	for _, set := range [][3]*x509.Certificate{{cert("same", "a"), cert("same", "b"), cert("stock", "c")}, {cert("same", "a"), cert("proxy", "b"), cert("same", "c")}, {cert("client", "a"), cert("same", "b"), cert("same", "c")}, {nil, cert("proxy", "b"), cert("stock", "c")}} {
		if separateRoleKeys(set[0], set[1], set[2]) {
			t.Fatal("different certificate bypassed key role separation")
		}
	}
}
