package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedRotationAPIRequiresSeparateRoleAndExactApproval(t *testing.T) {
	h, store, read := fixture(t)
	ctx := context.Background()
	if e := store.InitializeSSHTargets(ctx); e != nil {
		t.Fatal(e)
	}
	if e := store.InitializeManagedRotation(ctx); e != nil {
		t.Fatal(e)
	}
	original := bootstrapFixture(t)
	other := bootstrapFixture(t)
	p := firstboot.Plan{Base: original.Plan, ListenAddress: original.Plan.Address, InventoryAddress: original.Plan.Address, InventoryPort: 19922, InstallerSHA256: firstboot.InstallerSHA256(), Manifest: firstboot.Manifest{Profile: "ubuntu-24.04-systemd-nftables/v1", Architecture: "amd64", ArtifactSHA256: original.Plan.ArtifactSHA256, InstallerSHA256: firstboot.InstallerSHA256()}}
	remote := firstboot.RotationPlan{RequestID: "11111111-1111-4111-8111-111111111111", Installation: p, InstallationDigest: firstboot.Digest(p), ListenerHostKey: p.Base.HostKey, OldKey: p.Base.InventoryKey, NewKey: other.Plan.InventoryKey, ProgramSHA256: firstboot.RotationProgramSHA256(), RestartInventorySessions: true}
	old := sshinventory.Config{Address: p.InventoryAddress, Port: 19922, User: "garm-inventory-listener", HostKey: p.Base.HostKey, CredentialRef: "old"}
	next := old
	next.CredentialRef = "new"
	plan := firstboot.ManagedRotationPlan{Remote: remote, AdminAlias: "admin", OldID: "old-target", OldConfig: old, NewID: "new-target", NewConfig: next}
	resolve := func(context.Context, string) (ssh.Signer, error) {
		t.Error("unapproved credential resolution")
		return nil, firstboot.ErrState
	}
	reg, e := sshinventory.NewRegistry(map[string]sshinventory.Scope{"new": {Networks: []string{"192.0.2.0/24"}, Users: []string{"garm-inventory-listener"}, Ports: []uint16{19922}}}, store, resolve, nil)
	if e != nil {
		t.Fatal(e)
	}
	service, e := firstboot.NewManagedRotationService(map[string]firstboot.ManagedRotationTarget{"one": {Plan: plan, Artifact: original.Artifact}}, store, reg, resolve, resolve)
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	token := strings.Repeat("f", 64)
	if h.SetManagedRotation(service, read) == nil {
		t.Fatal("read role reused")
	}
	if e = h.SetManagedRotation(service, token); e != nil {
		t.Fatal(e)
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse token collision")
	}
	send := func(method, body, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/inventory-key-rotation", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", read).Code != 401 || send("GET", "", token).Code != 200 {
		t.Fatal("role separation")
	}
	body := map[string]any{"target_id": "one", "review_digest": firstboot.ManagedRotationDigest(plan), "request_id": remote.RequestID, "retry_unknown": false}
	encode := func() string { b, _ := json.Marshal(body); return string(b) }
	if send("POST", encode(), token).Code != 400 {
		t.Fatal("no explicit rotation approval")
	}
	body["confirm_rotation"] = true
	body["command"] = "id"
	if send("POST", encode(), token).Code != 400 {
		t.Fatal("command accepted")
	}
	delete(body, "command")
	body["request_id"] = "22222222-2222-4222-8222-222222222222"
	if send("POST", encode(), token).Code != 409 {
		t.Fatal("request substitution")
	}
	body["request_id"] = remote.RequestID
	if w := send("POST", encode(), token); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	service.Close()
	ops, _ := store.ManagedRotations(ctx)
	if ops["one"].State != "unknown" || ops["one"].Phase != "approved" {
		t.Fatal("missing registry permission was not denied before credentials", ops)
	}
}
