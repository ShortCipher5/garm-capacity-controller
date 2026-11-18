package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNativePlanningExplicitReadSaveAndSeparateAuthority(t *testing.T) {
	h, _, read := fixture(t)
	fixture := bootstrapFixture(t)
	resolved := 0
	inventory, e := sshinventory.NewService(map[string]sshinventory.Config{"candidate": {Address: fixture.Plan.Address, Port: 22, User: "inventory", HostKey: fixture.Plan.HostKey, CredentialRef: "restricted"}}, func(context.Context, string) (ssh.Signer, error) { resolved++; return nil, sshinventory.ErrInventory }, func(context.Context, string, string, string, string, bool) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if h.SetSSHInventory(inventory, strings.Repeat("c", 64)) != nil {
		t.Fatal("inventory")
	}
	cwd, _ := os.Getwd()
	dir, e := os.MkdirTemp(cwd, ".planning-api-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	binding := nativeplanning.Binding{PreparationID: strings.Repeat("a", 64), Host: "host-one", InventoryTarget: inventory.Targets()[0], NativeTarget: nativeprovision.Target{Address: fixture.Plan.Address, Port: 22, AdminUser: fixture.Plan.AdminUser, HostKey: fixture.Plan.HostKey, AdminKeyFingerprint: fixture.Plan.AdminKeyFingerprint}}
	bindings := map[string]nativeplanning.Binding{"one": binding}
	if h.ConfigureNativePlanning(bindings, dir, read) == nil {
		t.Fatal("read authority reused")
	}
	token := strings.Repeat("d", 64)
	if e = h.ConfigureNativePlanning(bindings, dir, token); e != nil {
		t.Fatal(e)
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("reverse role collision")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/native-planning", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://other.invalid").Code != 403 {
		t.Fatal("authority/origin")
	}
	if send("GET", "", token, "").Code != 200 {
		t.Fatal("targets")
	}
	body := map[string]any{"action": "probe", "target_id": "one", "binding_digest": nativeplanning.Digest(binding), "request_id": "11111111-1111-4111-8111-111111111111"}
	encode := func() string { raw, _ := json.Marshal(body); return string(raw) }
	if send("POST", encode(), token, "").Code != 400 {
		t.Fatal("unconfirmed read")
	}
	body["confirm_read"] = true
	body["binding_digest"] = "wrong"
	if send("POST", encode(), token, "").Code != 409 || resolved != 0 {
		t.Fatal("changed binding reached credentials")
	}
	if send("POST", `{"action":"history","target_id":"one"}`, token, "").Code != 200 {
		t.Fatal("saved history")
	}
	if send("POST", `{"action":"mix-proposal","target_id":"one","selection_digest":"`+strings.Repeat("e", 64)+`","disk_mib":4096}`, token, "").Code != 409 || resolved != 0 {
		t.Fatal("unsaved proposal reached credentials")
	}
	if send("POST", `{"action":"mix-proposal","target_id":"one","selection_digest":"`+strings.Repeat("e", 64)+`","disk_mib":4096}`, read, "").Code != 401 {
		t.Fatal("proposal used read role")
	}
	if send("POST", `{"action":"history","target_id":"one","disk_mib":4096}`, token, "").Code != 400 {
		t.Fatal("irrelevant mix fields accepted")
	}
	if send("POST", `{"action":"save","target_id":"one","review_digest":"bad","choice":{},"confirm_save":true}`, token, "").Code != 409 || resolved != 0 {
		t.Fatal("forged save")
	}
}
