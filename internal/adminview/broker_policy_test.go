package adminview

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrokerPolicyRoutes(t *testing.T) {
	h, read, write := managementFixture(t)
	const path = "/admin/api/v1/broker-policy"
	if w := request(t, h, "GET", path, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(t, h, "GET", path, read); w.Code != 200 || !strings.Contains(w.Body.String(), `"advisory_only":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	const body = `{"revision":0,"repositories":[]}`
	if w := post(t, h, "PUT", path, read, body); w.Code != 401 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("PUT", "https://127.0.0.1"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+write)
	r.Header.Set("Origin", "https://evil.invalid")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := post(t, h, "PUT", path, write, body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := post(t, h, "PUT", path, write, body); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"revision":1,"revision":1,"repositories":[]}`, `{"revision":1,"repositories":[],"unexpected":true}`, `{"revision":1,"repositories":null}`} {
		if w := post(t, h, "PUT", path, write, bad); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
