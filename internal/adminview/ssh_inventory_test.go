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

func TestSSHInventoryHTTPRequiresIndependentRoleReviewAndAudit(t *testing.T) {
	h, store, read := fixture(t)
	token := strings.Repeat("c", 64)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(private)
	calls := 0
	service, e := sshinventory.NewService(map[string]sshinventory.Config{"candidate": {Address: "192.0.2.20", Port: 22, User: "inventory", HostKey: string(ssh.MarshalAuthorizedKey(key.PublicKey())), CredentialRef: "approved-key"}}, func(context.Context, string) (ssh.Signer, error) { calls++; return nil, sshinventory.ErrInventory }, store.RecordSSHInventoryAudit)
	if e != nil {
		t.Fatal(e)
	}
	if h.SetSSHInventory(service, read) == nil {
		t.Fatal("read role gained SSH access")
	}
	if e = h.SetSSHInventory(service, token); e != nil {
		t.Fatal(e)
	}
	if h.EnableManagement(token) == nil {
		t.Fatal("SSH role gained management access after setup")
	}
	send := func(method, body, credential, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://127.0.0.1/admin/api/v1/ssh-inventory", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if send("GET", "", read, "").Code != 401 || send("GET", "", token, "https://elsewhere.invalid").Code != 403 {
		t.Fatal("role/origin escaped")
	}
	w := send("GET", "", token, "")
	var view struct {
		Targets []sshinventory.Target `json:"targets"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Targets) != 1 {
		t.Fatal(w.Body.String())
	}
	body := `{"target_id":"candidate","review_digest":"` + view.Targets[0].ReviewDigest + `","request_id":"11111111-1111-4111-8111-111111111111","verified_host_key":true}`
	for _, bad := range []string{strings.Replace(body, `true`, `false`, 1), strings.Replace(body, `"candidate"`, `"other"`, 1), strings.TrimSuffix(body, "}") + `,"address":"192.0.2.99"}`, strings.TrimSuffix(body, "}") + `,"target_id":"candidate"}`} {
		if send("POST", bad, token, "").Code < 400 {
			t.Fatal("unreviewed or ambiguous request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("rejected inputs contacted credential")
	}
	if w = send("POST", body, token, ""); w.Code != 503 || calls != 1 {
		t.Fatal(w.Body.String(), calls)
	}
	snapshot, e := store.ReadSnapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	kinds := map[string]bool{}
	for _, event := range snapshot.Audit {
		kinds[event.Kind] = true
		if strings.HasPrefix(event.Kind, "ssh_inventory_") && (!strings.Contains(event.RequestKey, view.Targets[0].ReviewDigest) || strings.Contains(event.RequestKey, "192.0.2.20")) {
			t.Fatal("audit omitted tuple digest or leaked destination")
		}
	}
	if !kinds["ssh_inventory_requested"] || !kinds["ssh_inventory_completed"] {
		t.Fatal("missing durable audit", kinds)
	}
}
