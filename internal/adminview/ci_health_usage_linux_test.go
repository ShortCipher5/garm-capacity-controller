package adminview

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func TestCIHealthUsageOnExactJobFallsBackToOriginalTiming(t *testing.T) {
	h, _, token := fixture(t)
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil || h.SetCIHealthSnapshot(s) != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.clock = func() time.Time { return now }
	facts := ciJobLinkFacts(now)
	selectLink := func(k cihealth.JobKey) (func() (string, bool), bool) {
		return func() (string, bool) { return "fixture", true }, true
	}
	if err := s.publishTiming(now, facts, false, selectLink, nil); err != nil {
		t.Fatal(err)
	}
	call := func(leases ...string) []byte {
		r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/ci-health", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		for _, lease := range leases {
			r.Header.Add("X-GARM-CI-Usage", lease)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		return w.Body.Bytes()
	}
	base := call()
	var active atomic.Bool
	active.Store(true)
	key := facts[1].Jobs[0].Key
	foreign := key
	foreign.Attempt.RepositoryID++
	wire := json.RawMessage(`{"state":"unavailable","reason":"job_too_short_uncaptured"}`)
	s.attachJobUsage(now, map[cihealth.JobKey]ciUsageJob{foreign: {wire, now.Add(time.Second), active.Load}})
	if !bytes.Equal(call(), base) {
		t.Fatal("foreign data rendered")
	}
	s.attachJobUsage(now, map[cihealth.JobKey]ciUsageJob{key: {wire, now.Add(time.Second), active.Load}})
	var shown ciTimingResponse
	raw := call("leased-v1")
	if err := json.Unmarshal(raw, &shown); err != nil {
		t.Fatal(err)
	}
	if shown.UsageExpiresAt == nil || !shown.UsageExpiresAt.Equal(now.Add(time.Second)) {
		t.Fatal("lost shorter usage deadline")
	}
	for _, headers := range [][]string{nil, {"unrecognized"}, {"leased-v1", "leased-v1"}} {
		if !bytes.Equal(call(headers...), base) {
			t.Fatal("legacy or ambiguous request received decorated bytes")
		}
	}
	first, second := shown.View.Attempts[0].Jobs[0], shown.View.Attempts[1].Jobs[0]
	if first.Usage != nil || second.ID != ciInt(key.JobID) || !bytes.Equal(second.Usage, wire) || second.LinkHandle == "" {
		t.Fatalf("wrong exact row: %s", raw)
	}
	active.Store(false)
	if !bytes.Equal(call("leased-v1"), base) {
		t.Fatal("fallback changed original timing or handles")
	}
	active.Store(true)
	if !bytes.Equal(call("leased-v1"), base) {
		t.Fatal("revoked response resurrected")
	}
	s.attachJobUsage(now, map[cihealth.JobKey]ciUsageJob{key: {wire, now.Add(time.Second), active.Load}})
	now = now.Add(time.Second)
	if !bytes.Equal(call("leased-v1"), base) {
		t.Fatal("expired lease was offered to a new client")
	}
	if err := s.Publish(now.Add(time.Second), facts); err == nil {
		t.Fatal("accepted future timing")
	}
}
