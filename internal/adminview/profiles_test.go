package adminview

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func managementFixture(t *testing.T) (*Handler, string, string) {
	t.Helper()
	h, _, read := fixture(t)
	write := strings.Repeat("b", 64)
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	return h, read, write
}

func post(t *testing.T, h *Handler, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://127.0.0.1"+target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const validProfileBody = `{"name":"small","cpu_millis":2000,"ram_mib":4096,"disk_mib":10240}`

func TestProfilesReadAuthentication(t *testing.T) {
	h, _, read := fixture(t)
	for _, route := range []string{"/admin/api/v1/profiles", "/admin/api/v1/profiles/small/estimate/host-a", "/admin/api/v1/profiles/small/estimate"} {
		if response := request(t, h, "GET", route, ""); response.Code != 401 {
			t.Fatalf("unauthenticated profile read %d", response.Code)
		}
		if response := request(t, h, "GET", route, strings.Repeat("c", 64)); response.Code != 401 {
			t.Fatalf("bad token profile read %d", response.Code)
		}
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles", read); response.Code != 200 {
		t.Fatalf("read token list rejected %d", response.Code)
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles/nope/estimate/host-a", read); response.Code != 404 {
		t.Fatalf("unknown profile estimate %d", response.Code)
	}
}

func TestProfileCRUDAndEstimates(t *testing.T) {
	h, read, write := managementFixture(t)
	if response := post(t, h, "POST", "/admin/api/v1/profiles", write, validProfileBody); response.Code != 200 {
		t.Fatalf("upsert rejected %d %s", response.Code, response.Body.String())
	}
	response := request(t, h, "GET", "/admin/api/v1/profiles", read)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var listed struct {
		Profiles []struct {
			admission.Profile
			Estimates []struct {
				Host     string `json:"host"`
				Estimate int64  `json:"estimate"`
			} `json:"estimates"`
		} `json:"profiles"`
	}
	if json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed.Profiles) != 1 {
		t.Fatalf("invalid list %s", response.Body.String())
	}
	profile := listed.Profiles[0]
	if profile.Name != "small" || profile.CPUMillis != 2000 || profile.RAMMiB != 4096 || profile.DiskMiB != 10240 {
		t.Fatalf("bad profile wire shape %+v", profile)
	}
	if len(profile.Estimates) != 1 || profile.Estimates[0].Host != "host-a" || profile.Estimates[0].Estimate != 2 {
		t.Fatalf("bad estimates %+v", profile.Estimates)
	}
	response = request(t, h, "GET", "/admin/api/v1/profiles/small/estimate/host-a", read)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var one struct {
		Name     string `json:"name"`
		Host     string `json:"host"`
		Estimate int64  `json:"estimate"`
	}
	if json.Unmarshal(response.Body.Bytes(), &one) != nil || one.Name != "small" || one.Host != "host-a" || one.Estimate != 2 {
		t.Fatalf("bad single estimate %s", response.Body.String())
	}
	response = request(t, h, "GET", "/admin/api/v1/profiles/small/estimate/ghost-host", read)
	if response.Code != 404 {
		t.Fatalf("unknown host estimate %d", response.Code)
	}
	if response := request(t, h, "DELETE", "/admin/api/v1/profiles/small", write); response.Code != 200 {
		t.Fatalf("delete rejected %d", response.Code)
	} else if !strings.Contains(response.Body.String(), `"deleted":true`) || !strings.Contains(response.Body.String(), "fail closed") {
		t.Fatalf("delete response missing outcome/comment %s", response.Body.String())
	}
	if response := request(t, h, "DELETE", "/admin/api/v1/profiles/small", write); response.Code != 404 {
		t.Fatalf("unknown profile delete %d", response.Code)
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles", read); response.Code != 200 || strings.Contains(response.Body.String(), "small") {
		t.Fatalf("profile survived deletion %s", response.Body.String())
	}
}

func TestProfileWriteRequiresManagementCredential(t *testing.T) {
	h, _, read := fixture(t)
	if response := post(t, h, "POST", "/admin/api/v1/profiles", read, validProfileBody); response.Code != 404 {
		t.Fatalf("write without management %d", response.Code)
	}
	h, read, write := managementFixture(t)
	if response := post(t, h, "POST", "/admin/api/v1/profiles", read, validProfileBody); response.Code != 401 {
		t.Fatalf("write with read token %d", response.Code)
	}
	if response := post(t, h, "POST", "/admin/api/v1/profiles", write, validProfileBody); response.Code != 200 {
		t.Fatalf("write with management token %d", response.Code)
	}
	if response := request(t, h, "DELETE", "/admin/api/v1/profiles/small", read); response.Code != 401 {
		t.Fatalf("delete with read token %d", response.Code)
	}
}

func TestProfileValidationAndMethodErrors(t *testing.T) {
	h, read, write := managementFixture(t)
	cases := map[string]int{
		`{"name":"bad","cpu_millis":1500,"ram_mib":4096,"disk_mib":0}`:              400, // fractional vCPU
		`{"name":"bad","cpu_millis":2000,"ram_mib":4096,"disk_mib":-1}`:             400, // negative disk
		`{"name":"BAD","cpu_millis":2000,"ram_mib":4096,"disk_mib":0}`:              400, // invalid name
		`{"name":"bad","cpu_millis":2000,"ram_mib":0,"disk_mib":0}`:                 400, // non-positive RAM
		`{"name":"bad","cpu_millis":2000,"ram_mib":4096,"disk_mib":0,"x":1}`:        400, // unknown field
		`{"name":"bad","name":"bad","cpu_millis":2000,"ram_mib":4096,"disk_mib":0}`: 400, // duplicate key
	}
	for body, expected := range cases {
		if response := post(t, h, "POST", "/admin/api/v1/profiles", write, body); response.Code != expected {
			t.Fatalf("body %s -> %d want %d", body, response.Code, expected)
		}
	}
	r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/profiles", strings.NewReader(validProfileBody))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Authorization", "Bearer "+write)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("non-JSON upsert %d", w.Code)
	}
	if response := request(t, h, "PUT", "/admin/api/v1/profiles", write); response.Code != 405 {
		t.Fatalf("PUT profiles %d", response.Code)
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles/small", read); response.Code != 405 {
		t.Fatalf("GET named profile %d", response.Code)
	}
	if response := request(t, h, "DELETE", "/admin/api/v1/profiles/Bad", write); response.Code != 400 {
		t.Fatalf("invalid name delete %d", response.Code)
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles/small/extra/segments", read); response.Code != 404 {
		t.Fatalf("unknown profile route %d", response.Code)
	}
	if response := request(t, h, "GET", "/admin/api/v1/profiles/small/estimate?host=host-a", read); response.Code != 400 {
		t.Fatalf("query string accepted %d", response.Code)
	}
}
