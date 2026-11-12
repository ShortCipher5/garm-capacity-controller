package adminview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestManagementDistinctAuthorizationAndBoundedRequests(t *testing.T) {
	h, store, read := fixture(t)
	write := strings.Repeat("c", 64)
	if h.EnableManagement(read) == nil || h.EnableManagement("bad") == nil {
		t.Fatal("non-independent token accepted")
	}
	state, _ := store.ReadSnapshot(context.Background())
	change := admission.PolicyChange{Revision: admission.PolicyRevision(state.Config), Action: "host_mode", Target: "host-a", Mode: "disabled", Reason: "maintenance"}
	body, _ := json.Marshal(change)
	send := func(token, content, origin string, body []byte) int {
		r := httptest.NewRequest("POST", "https://127.0.0.1/admin/api/v1/policy", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", content)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if send(write, "application/json", "", body) != 404 {
		t.Fatal("management enabled by default")
	}
	if err := h.EnableManagement(write); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{read, "", strings.Repeat("b", 64)} {
		if send(token, "application/json", "", body) != 401 {
			t.Fatal("read/invalid token authorized write")
		}
	}
	if request(t, h, "GET", "/admin/api/v1/overview", write).Code != 401 {
		t.Fatal("write credential silently granted separate read role")
	}
	for _, origin := range []string{"https://evil.example", "null", "http://127.0.0.1", "https://127.0.0.1/path"} {
		if send(write, "application/json", origin, body) != 403 {
			t.Fatal("cross-origin mutation accepted")
		}
	}
	if send(write, "text/plain", "", body) != 415 {
		t.Fatal("simple-content request accepted")
	}
	if send(write, "application/json", "", []byte(strings.Repeat("x", 8193))) != 413 {
		t.Fatal("unbounded request")
	}
	for _, bad := range []string{`{"revision":"a","action":"host_mode","action":"global_budget"}`, `{"unknown":true}`, `{"Action":"host_mode"}`, `{"action":"host_mode","Action":"global_budget"}`, `{"budget":{"CPU_MILLIS":1}}`, `{} {}`, `{"budget":{"cpu_millis":1,"cpu_millis":2}}`} {
		if send(write, "application/json", "", []byte(bad)) != 400 {
			t.Fatalf("bad JSON accepted: %s", bad)
		}
	}
	before, _ := store.ReadSnapshot(context.Background())
	if before.Config.Hosts["host-a"].Mode != "enabled" {
		t.Fatal("rejected request changed policy")
	}
	if send(write, "application/json", "https://127.0.0.1", body) != 200 {
		t.Fatal("authorized edit failed")
	}
	if send(write, "application/json", "", body) != 409 {
		t.Fatal("stale edit accepted")
	}
	after, _ := store.ReadSnapshot(context.Background())
	if after.Config.Hosts["host-a"].Mode != "disabled" || after.Ready {
		t.Fatal("edit not persisted/barrier not closed")
	}
	overviewResponse := request(t, h, "GET", "/admin/api/v1/overview", read)
	var view Overview
	if json.Unmarshal(overviewResponse.Body.Bytes(), &view) != nil || !view.ManagementEnabled || view.PolicyRevision == change.Revision {
		t.Fatal("management capabilities/revision missing")
	}
	if strings.Contains(overviewResponse.Body.String(), write) || strings.Contains(overviewResponse.Body.String(), read) {
		t.Fatal("credential leaked")
	}
}
