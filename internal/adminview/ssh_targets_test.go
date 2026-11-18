package adminview

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSHTargetHTTPRequiresSeparateAuthorityAndExactReviewedIdentity(t *testing.T) {
	h, store, read := fixture(t)
	ctx := context.Background()
	if e := store.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(private)
	c := sshinventory.Config{Address: "192.0.2.20", Port: 22, User: "inventory", CredentialRef: "key", HostKey: string(ssh.MarshalAuthorizedKey(key.PublicKey()))}
	calls := 0
	resolve := func(context.Context, string) (ssh.Signer, error) { calls++; return nil, sshinventory.ErrInventory }
	service, _ := sshinventory.NewService(nil, resolve, store.RecordSSHInventoryAudit)
	inventoryToken := strings.Repeat("c", 64)
	token := strings.Repeat("d", 64)
	if e := h.SetSSHInventory(service, inventoryToken); e != nil {
		t.Fatal(e)
	}
	registry, e := sshinventory.NewRegistry(map[string]sshinventory.Scope{"key": {Networks: []string{"192.0.2.0/24"}, Users: []string{"inventory"}, Ports: []uint16{22}}}, store, resolve, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{read, inventoryToken, "bad"} {
		if h.SetSSHTargetManagement(registry, bad) == nil {
			t.Fatal("role collision")
		}
	}
	if e = h.SetSSHTargetManagement(registry, token); e != nil {
		t.Fatal(e)
	}
	service.SetRegistry(registry)
	if h.SetSSHInventory(service, token) == nil || h.EnableManagement(token) == nil {
		t.Fatal("reverse role collision")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/ssh-targets", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", inventoryToken, "").Code != 401 || send("GET", "", token, "https://elsewhere.invalid").Code != 403 {
		t.Fatal("authorization escaped")
	}
	body := map[string]any{"action": "review", "target_id": "new", "config": c}
	encode := func() string { b, _ := json.Marshal(body); return string(b) }
	w := send("POST", encode(), token, "")
	var review struct {
		Target sshinventory.Target `json:"target"`
		Saved  bool                `json:"saved"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil || review.Saved || calls != 0 {
		t.Fatal(w.Body.String())
	}
	list, e := store.ListSSHTargets(ctx)
	if e != nil || len(list) != 0 {
		t.Fatal("review saved")
	}
	body["action"] = "approve"
	body["review_digest"] = review.Target.ReviewDigest
	body["request_id"] = "11111111-1111-4111-8111-111111111111"
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("unverified saved")
	}
	body["verified_host_key"] = true
	changed := c
	changed.Address = "192.0.2.21"
	body["config"] = changed
	if send("POST", encode(), token, "").Code != 409 {
		t.Fatal("substitution saved")
	}
	body["config"] = c
	duplicate := strings.TrimSuffix(encode(), "}") + `,"action":"approve"}`
	if send("POST", duplicate, token, "").Code != 400 {
		t.Fatal("duplicate field accepted")
	}
	if w = send("POST", encode(), token, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	list, e = store.ListSSHTargets(ctx)
	if e != nil || len(list) != 1 || calls != 0 {
		t.Fatal("registration contacted server", e, calls)
	}
	targets, e := service.ListTargets(ctx)
	if e != nil || len(targets) != 1 || targets[0] != review.Target {
		t.Fatal("registered target not inspectable", e)
	}
}
