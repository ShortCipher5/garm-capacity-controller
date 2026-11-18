package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"reflect"
	"testing"
	"time"
)

type loopStatusFixture struct {
	value brokerinput.LoopStatus
	reads int
}

func (s *loopStatusFixture) Snapshot() brokerinput.LoopStatus { s.reads++; return s.value }
func TestAllocatorStatusReadOnlyActualSource(t *testing.T) {
	h, store, token := fixture(t)
	before, _ := store.ReadSnapshot(context.Background())
	const path = "/admin/api/v1/allocator-status"
	source := &loopStatusFixture{value: brokerinput.LoopStatus{State: "blocked", ObservedAt: time.Now().UTC(), ReasonCode: "native_management_authority_unavailable"}}
	h.SetAllocatorStatusSource(source)
	for _, check := range []struct {
		method, token string
		code          int
	}{{"GET", "", 401}, {"POST", token, 405}, {"DELETE", token, 405}} {
		if w := request(t, h, check.method, path, check.token); w.Code != check.code {
			t.Fatal(w.Code)
		}
	}
	if source.reads != 0 {
		t.Fatal("unauthorized call observed source")
	}
	w := request(t, h, "GET", path, token)
	var got struct {
		Scope  string                 `json:"scope"`
		Status brokerinput.LoopStatus `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Scope != "process-grant-loop" || got.Status.State != "blocked" || got.Status.AttemptCount != 0 || !got.Status.LastAttemptAt.IsZero() {
		t.Fatal(w.Code, w.Body.String())
	}
	source.value.State = "stopped"
	if w = request(t, h, "GET", path, token); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status.State != "stopped" {
		t.Fatal("cached status", w.Body.String())
	}
	after, _ := store.ReadSnapshot(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("status changed ledger")
	}
}
func TestAllocatorMissingServiceNeverClaimsRunning(t *testing.T) {
	h, _, token := fixture(t)
	var empty *brokerinput.Service
	h.SetAllocatorStatusSource(empty)
	w := request(t, h, "GET", "/admin/api/v1/allocator-status", token)
	var got struct {
		Status brokerinput.LoopStatus `json:"status"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status.State != "unconfigured" || got.Status.InFlight || got.Status.AttemptCount != 0 || !got.Status.StartedAt.IsZero() {
		t.Fatal(w.Code, w.Body.String())
	}
}
