package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFirstbootAPISeparateAuthorityAndExactReview(t *testing.T) {
	h, store, read := fixture(t)
	ctx := context.Background()
	if e := store.InitializeFirstboot(ctx); e != nil {
		t.Fatal(e)
	}
	original := bootstrapFixture(t)
	plan := firstboot.Plan{Base: original.Plan, ListenAddress: original.Plan.Address, InventoryAddress: original.Plan.Address, InventoryPort: 19922, InstallerSHA256: firstboot.InstallerSHA256(), Manifest: firstboot.Manifest{Profile: "ubuntu-24.04-systemd-nftables/v1", Architecture: "amd64", ArtifactSHA256: original.Plan.ArtifactSHA256, InstallerSHA256: firstboot.InstallerSHA256()}}
	r := firstboot.Request{Plan: plan, ReviewDigest: firstboot.Digest(plan), Artifact: original.Artifact}
	s, e := firstboot.NewService(map[string]firstboot.DeploymentTarget{"one": {Request: r, CredentialAlias: "admin"}}, store, func(context.Context, string) (ssh.Signer, error) { return nil, firstboot.ErrInvalid })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	token := strings.Repeat("e", 64)
	if h.SetFirstboot(s, read) == nil {
		t.Fatal("read role reused")
	}
	if h.SetFirstboot(s, token) != nil {
		t.Fatal("firstboot role")
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse management collision")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/firstboot", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("authority/origin escaped")
	}
	if send("GET", "", token, "").Code != 200 {
		t.Fatal("targets unavailable")
	}
	body := map[string]any{"target_id": "one", "review_digest": r.ReviewDigest, "request_id": "11111111-1111-4111-8111-111111111111", "retry_unknown": false}
	encode := func() string { b, _ := json.Marshal(body); return string(b) }
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("unconfirmed execution")
	}
	body["confirm_install"] = true
	body["command"] = "id"
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("arbitrary command accepted")
	}
	delete(body, "command")
	body["review_digest"] = strings.Repeat("f", 64)
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("changed review")
	}
	body["review_digest"] = r.ReviewDigest
	if w := send("POST", encode(), token, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.Close()
	ops, e := store.FirstbootOperations(ctx)
	if e != nil || ops["one"].State != "unknown" {
		t.Fatal("unknown not durable", ops, e)
	}
}

// Integration guard: adding a privileged route must not replace an existing
// read-only route or mix their authorities on the same handler.
func TestFirstbootAndCapacityStateRoutesCoexist(t *testing.T) {
	h, store, read := fixture(t)
	baseline(t, store, []admission.InventoryResource{{ID: "one", Host: "host-a", Pool: "pool-a", State: "unknown", Resources: admission.Resources{CPU: 1000, RAM: 1000, Runners: 1}}})
	if e := store.InitializeFirstboot(context.Background()); e != nil {
		t.Fatal(e)
	}
	original := bootstrapFixture(t)
	plan := firstboot.Plan{Base: original.Plan, ListenAddress: original.Plan.Address, InventoryAddress: original.Plan.Address, InventoryPort: 19922, InstallerSHA256: firstboot.InstallerSHA256(), Manifest: firstboot.Manifest{Profile: "ubuntu-24.04-systemd-nftables/v1", Architecture: "amd64", ArtifactSHA256: original.Plan.ArtifactSHA256, InstallerSHA256: firstboot.InstallerSHA256()}}
	r := firstboot.Request{Plan: plan, ReviewDigest: firstboot.Digest(plan), Artifact: original.Artifact}
	service, e := firstboot.NewService(map[string]firstboot.DeploymentTarget{"prepared": {Request: r, CredentialAlias: "admin"}}, store, func(context.Context, string) (ssh.Signer, error) {
		t.Error("read-only GET resolved credential")
		return nil, firstboot.ErrInvalid
	})
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	token := strings.Repeat("e", 64)
	if e = h.SetFirstboot(service, token); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ path, credential, field string }{{"/admin/api/v1/capacity-states", read, "source"}, {"/admin/api/v1/firstboot", token, "targets"}} {
		w := request(t, h, "GET", tc.path, tc.credential)
		if w.Code != 200 {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body[tc.field] == nil {
			t.Fatal("wrong endpoint dispatched", tc.path, w.Body.String())
		}
	}
	if w := request(t, h, "GET", "/admin/api/v1/capacity-states", token); w.Code != 401 {
		t.Fatal("firstboot token became read authority", w.Code)
	}
	if w := request(t, h, "GET", "/admin/api/v1/firstboot", read); w.Code != 401 {
		t.Fatal("read token became firstboot authority", w.Code)
	}
}
