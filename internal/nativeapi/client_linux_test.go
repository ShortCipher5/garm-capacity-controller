//go:build linux

package nativeapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativedomain"
)

func rootFixture(t *testing.T) []byte {
	t.Helper()
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, template, template, public, key)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(der)
	raw, e := json.Marshal(map[string]any{"type": "sync", "status_code": 200, "metadata": map[string]any{"auth": "trusted", "api_version": "1.0", "api_status": "stable", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false, "server_name": "fixture", "server_version": NativeVersion, "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "certificate_fingerprint": fmt.Sprintf("%x", hash)}}})
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestPrivateRootIdentityIsDomainScopedAndStrict(t *testing.T) {
	raw := rootFixture(t)
	domain := strings.Repeat("a", 64)
	now := time.Now().UTC()
	identity, e := parseIdentity(raw, domain, now)
	if e != nil || !identity.Valid() {
		t.Fatal(e)
	}
	if identity.DomainID() != domain || identity.ServerVersion() != NativeVersion || identity.ServerName() != "fixture" || identity.ObservedAt() != now {
		t.Fatal("identity")
	}
	other, e := parseIdentity(raw, strings.Repeat("b", 64), now)
	if e != nil {
		t.Fatal(e)
	}
	if other.Origin() == identity.Origin() || other.Scope() == identity.Scope() {
		t.Fatal("domain aliases collide")
	}
	for _, pair := range [][2]string{{`"server_clustered":false`, `"server_clustered":true`}, {`"auth":"trusted"`, `"auth":"untrusted"`}, {`"server_version":"6.0.4"`, `"server_version":"6.0.5"`}, {`"status_code":200`, `"status_code":200,"status_code":200`}, {`"server_name":"fixture"`, `"server_name":"fixture","server_name":"other"`}, {`"instance_create_start"`, `"missing"`}} {
		changed := strings.Replace(string(raw), pair[0], pair[1], 1)
		if changed == string(raw) {
			t.Fatal("broken fixture", pair)
		}
		if _, e = parseIdentity([]byte(changed), domain, now); e != ErrNative {
			t.Fatal("accepted invalid private identity", pair, e)
		}
	}
}
func TestRootCertificateFingerprintCannotBeSpoofed(t *testing.T) {
	raw := rootFixture(t)
	var envelope map[string]any
	json.Unmarshal(raw, &envelope)
	env := envelope["metadata"].(map[string]any)["environment"].(map[string]any)
	env["certificate_fingerprint"] = strings.Repeat("0", 64)
	changed, _ := json.Marshal(envelope)
	if _, e := parseIdentity(changed, strings.Repeat("a", 64), time.Now()); e != ErrNative {
		t.Fatal("accepted wrong DER fingerprint")
	}
}
func TestIdentityCannotBeManufacturedFromOperatorClaims(t *testing.T) {
	p, _ := fixturePolicy(t)
	if _, e := New(context.Background(), nil, p); e != ErrPolicy {
		t.Fatal(e)
	}
	if _, e := New(context.Background(), &nativedomain.Domain{}, p); e != ErrPolicy {
		t.Fatal(e)
	}
	if (Identity{}).Valid() {
		t.Fatal("zero identity valid")
	}
	var c Client
	if _, e := c.Identity(context.Background()); e != ErrNative {
		t.Fatal(e)
	}
}
