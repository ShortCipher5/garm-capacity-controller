package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/crypto/ssh"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreparedDiagnosticSeparateApprovalAndNoUnpreparedContact(t *testing.T) {
	h, _, read := fixture(t)
	s := preparationFixture(t)
	calls := 0
	token := strings.Repeat("b", 64)
	if e := h.SetNativePreparation(map[string]NativePreparationTarget{"one": {Service: s, Resolve: func() (ssh.Signer, error) { calls++; return nil, nativeprovision.ErrInvalid }}}, token); e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"target_id": "one", "preparation_id": strings.Repeat("a", 64), "review_digest": s.ReviewDigest(), "confirm_read": true}
	send := func(credential, origin string) int {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/native-preparation/diagnostic", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if send(read, "") != 401 || send(token, "https://other.invalid") != 403 || send(token, "") != 409 {
		t.Fatal("authority/association/completion fence")
	}
	body["confirm_read"] = false
	if send(token, "") != 400 {
		t.Fatal("approval")
	}
	body["confirm_read"] = true
	body["path"] = "/tmp/custom"
	if send(token, "") != 400 {
		t.Fatal("arbitrary path")
	}
	if calls != 0 {
		t.Fatal("unprepared network resolver", calls)
	}
	state, e := s.Status()
	if e != nil || state.Status != "not_started" || state.Attempt != nil {
		t.Fatal("read wrote preparation", state, e)
	}
}
