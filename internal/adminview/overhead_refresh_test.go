package adminview

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/overheadrefresh"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOverheadRefreshRoleAndStrictInput(t *testing.T) {
	h, read, write := managementFixture(t)
	path := "/admin/api/v1/overhead-refresh"
	if w := request(t, h, "GET", path, read); w.Code != 404 {
		t.Fatal(w.Code)
	}
	// Deliberately uninitialized source: this can test rejection, never success.
	h.overheadRefresh = &overheadrefresh.Service{}
	if w := request(t, h, "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "GET", path, read); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := post(t, h, "POST", path, read, `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"endpoint":"https://evil.invalid"}`, `{"recipe":"x","recipe":"y"}`, `{"Recipe":"x"}`, `{"proof":{}}`, `{} {}`} {
		if w := post(t, h, "POST", path, write, body); w.Code != 400 {
			t.Fatal(w.Code, body)
		}
	}
	r := httptest.NewRequest("POST", "https://127.0.0.1"+path, strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+write)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.invalid")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
