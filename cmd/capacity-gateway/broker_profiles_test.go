package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManagedBrokerProfileRecipe(t *testing.T) {
	dir := t.TempDir()
	s := httptest.NewTLSServer(nil)
	defer s.Close()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, data, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	ca := write("ca", cert)
	client := write("cert", cert)
	key, e := x509.MarshalPKCS8PrivateKey(s.TLS.Certificates[0].PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	keyfile := write("key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	pool := "11111111-1111-4111-8111-111111111111"
	controller := "22222222-2222-4222-8222-222222222222"
	digest := sha256.Sum256(s.Certificate().RawSubjectPublicKeyInfo)
	policy := nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: controller, Host: "host", DomainID: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{pool: {Flavor: "small", Image: "image", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge"}}}
	c := map[string]any{"id": "small", "endpoint": s.URL, "ca_file": ca, "client_certificate_file": client, "client_key_file": keyfile, "managed_server_key_pin": hex.EncodeToString(digest[:]), "managed_controller_id": controller, "request": admission.BrokerPoolProfileRequest{Authority: admission.StockProviderAuthority{Controller: controller, PoolID: pool, ProviderName: "native"}}, "domains": []any{map[string]any{"sequence": 1, "policy": policy}}}
	save := func() string { raw, _ := json.Marshal([]any{c}); return write("recipe", raw) }
	recipes, e := loadBrokerProfileRecipes(save())
	if e != nil || len(recipes) != 1 {
		t.Fatalf("valid managed recipe: %v", e)
	}
	if recipes[0].Transport.Token != "" || len(recipes[0].Transport.TLS.Certificates) != 1 {
		t.Fatal("wrong transport")
	}
	c["token_file"] = write("token", []byte("secret"))
	if _, e := loadBrokerProfileRecipes(save()); e == nil {
		t.Fatal("managed stock token accepted")
	}
	delete(c, "token_file")
	c["managed_server_key_pin"] = "invalid"
	if _, e := loadBrokerProfileRecipes(save()); e == nil {
		t.Fatal("invalid pin accepted")
	}
}
