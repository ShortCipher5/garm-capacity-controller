package adminview

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

func TestBrokerProfileConsoleReadAndAuth(t *testing.T) {
	h, read, write := managementFixture(t)
	const path = "/admin/api/v1/broker-profiles"
	if w := request(t, h, "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(t, h, "GET", path, read)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"configuration_only":true`) || !strings.Contains(w.Body.String(), `"writes_enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := post(t, h, "POST", path, read, `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := post(t, h, "POST", path, write, `{}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "https://127.0.0.1"+path, strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.invalid")
	r.Header.Set("Authorization", "Bearer "+write)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "DELETE", path, read); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func recipeFixture(t *testing.T) BrokerProfileRecipe {
	t.Helper()
	const pool = "11111111-1111-4111-8111-111111111111"
	p, e := nativeapi.NewPolicy(nativeapi.PolicySpec{Version: nativeapi.PolicyVersion, Controller: "controller", Host: "host", DomainID: strings.Repeat("a", 64), NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion, Pools: map[string]nativeapi.Pool{pool: {Flavor: "small", Image: "image", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"runner"}, StoragePool: "storage", Bridge: "bridge"}}})
	if e != nil {
		t.Fatal(e)
	}
	return BrokerProfileRecipe{ID: "small-template", TrustIdentity: strings.Repeat("a", 64), Transport: stockidentity.Configuration{Endpoint: "https://localhost.invalid", Token: "PRIVATE-TOKEN", Provider: "capacity", Pool: pool, TLS: &tls.Config{RootCAs: x509.NewCertPool()}}, Request: admission.BrokerPoolProfileRequest{Authority: admission.StockProviderAuthority{PoolID: pool, ProviderName: "capacity"}, Execution: admission.ExecutionImage{Profiles: []string{"runner"}}}, Policies: map[int64]nativeapi.Policy{1: p}}
}
func TestBrokerProfileConsolePrivateRecipesAndStrictRequests(t *testing.T) {
	h, read, write := managementFixture(t)
	recipe := recipeFixture(t)
	if err := h.SetBrokerProfileRecipes([]BrokerProfileRecipe{recipe}); err != nil {
		t.Fatal(err)
	}
	recipe.Request.Execution.Profiles[0] = "mutated"
	delete(recipe.Policies, 1)
	if h.brokerProfiles[recipe.ID].Request.Execution.Profiles[0] != "runner" || len(h.brokerProfiles[recipe.ID].Policies) != 1 {
		t.Fatal("caller mutation changed recipe")
	}
	w := request(t, h, "GET", "/admin/api/v1/broker-profiles", read)
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE-TOKEN") || strings.Contains(w.Body.String(), "localhost.invalid") || strings.Contains(w.Body.String(), "Fingerprint") || !strings.Contains(w.Body.String(), `"writes_enabled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	good := `{"recipe":"small-template","size_profile":"small","expected_sequence":0,"resources":{"cpu_millis":1000,"ram_mib":1024,"disk_mib":4096,"runners":1}}`
	for _, body := range []string{`{}`, strings.Replace(good, `"expected_sequence":0`, `"expected_sequence":0,"expected_sequence":0`, 1), strings.Replace(good, `"resources":`, `"endpoint":"https://evil.invalid","resources":`, 1), strings.Replace(good, `"disk_mib":4096`, `"disk_mib":0`, 1), strings.Replace(good, `"small-template"`, `"unknown"`, 1)} {
		w := post(t, h, "POST", "/admin/api/v1/broker-profiles", write, body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	good = strings.Replace(good, `"recipe":"small-template"`, `"recipe":"small-template","recipe_revision":"`+brokerRecipeRevision(h.brokerProfiles["small-template"])+`"`, 1)
	stale := strings.Replace(good, `"expected_sequence":0`, `"expected_sequence":1`, 1)
	if w := post(t, h, "POST", "/admin/api/v1/broker-profiles", write, stale); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v map[string]any
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("bad JSON")
	}
}

func TestBrokerProfileConsoleObservesOnlyAfterAuthorizedReview(t *testing.T) {
	h, read, write := managementFixture(t)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer PRIVATE-TOKEN" {
			t.Error("wrong transport")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"11111111-1111-4111-8111-111111111111","repo_id":"22222222-2222-4222-8222-222222222222","provider_name":"capacity","flavor":"small","image":"image","os_type":"linux","os_arch":"amd64"}`)
	}))
	defer server.Close()
	recipe := recipeFixture(t)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	recipe.Transport.Endpoint = server.URL
	recipe.Transport.TLS.RootCAs = roots
	if err := h.SetBrokerProfileRecipes([]BrokerProfileRecipe{recipe}); err != nil {
		t.Fatal(err)
	}
	body := `{"recipe":"small-template","size_profile":"small","expected_sequence":0,"resources":{"cpu_millis":1000,"ram_mib":1024,"disk_mib":4096,"runners":1}}`
	body = strings.Replace(body, `"recipe":"small-template"`, `"recipe":"small-template","recipe_revision":"`+brokerRecipeRevision(h.brokerProfiles["small-template"])+`"`, 1)
	request(t, h, "GET", "/admin/api/v1/broker-profiles", read)
	post(t, h, "POST", "/admin/api/v1/broker-profiles", read, body)
	if calls != 0 {
		t.Fatal("unapproved observation")
	}
	w := post(t, h, "POST", "/admin/api/v1/broker-profiles", write, body)
	// Actual verified observation occurs, but the test ledger has no native
	// enrollment/pool template. No configured recipe can manufacture it.
	if calls != 1 || w.Code != 409 {
		t.Fatal(calls, w.Code, w.Body.String())
	}
	profiles, err := h.store.CurrentBrokerPoolProfiles(context.Background())
	if err != nil || len(profiles) != 0 {
		t.Fatal("fake authority accepted", err)
	}
}

func TestBrokerRecipeRevisionRejectsRestartRetarget(t *testing.T) {
	h, _, write := managementFixture(t)
	r := recipeFixture(t)
	old := brokerRecipeRevision(r)
	r.Transport.Endpoint = "https://other.invalid"
	if brokerRecipeRevision(r) == old {
		t.Fatal("endpoint not revision bound")
	}
	r = recipeFixture(t)
	r.TrustIdentity = strings.Repeat("f", 64)
	if brokerRecipeRevision(r) == old {
		t.Fatal("trust not revision bound")
	}
	if err := h.SetBrokerProfileRecipes([]BrokerProfileRecipe{r}); err != nil {
		t.Fatal(err)
	}
	body := `{"recipe":"small-template","recipe_revision":"` + old + `","size_profile":"small","expected_sequence":0,"resources":{"cpu_millis":1000,"ram_mib":1024,"disk_mib":4096,"runners":1}}`
	if w := post(t, h, "POST", "/admin/api/v1/broker-profiles", write, body); w.Code != 409 || !strings.Contains(w.Body.String(), "recipe changed") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestBrokerRecipeReviewBindsManagedDestination(t *testing.T) {
	r := recipeFixture(t)
	original := brokerRecipeRevision(r)
	r.Transport.ManagedServerKeyPin = strings.Repeat("a", 64)
	if brokerRecipeRevision(r) == original {
		t.Fatal("managed key pin omitted from review")
	}
	pinned := brokerRecipeRevision(r)
	r.Transport.ManagedControllerID = "11111111-1111-1111-1111-111111111111"
	if brokerRecipeRevision(r) == pinned {
		t.Fatal("controller identity omitted from review")
	}
}
