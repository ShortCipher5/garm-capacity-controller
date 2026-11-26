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
	call := func() []byte {
		r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/admin/api/v1/ci-health", nil)
		r.Header.Set("Authorization", "Bearer "+token)
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
	wire := json.RawMessage(`{"state":"unavailable","reason":"job_too_short"}`)
	s.attachJobUsage(now, map[cihealth.JobKey]ciUsageJob{foreign: {wire, active.Load}})
	if !bytes.Equal(call(), base) {
		t.Fatal("foreign data rendered")
	}
	s.attachJobUsage(now, map[cihealth.JobKey]ciUsageJob{key: {wire, active.Load}})
	var shown ciTimingResponse
	raw := call()
	if err := json.Unmarshal(raw, &shown); err != nil {
		t.Fatal(err)
	}
	first, second := shown.View.Attempts[0].Jobs[0], shown.View.Attempts[1].Jobs[0]
	if first.Usage != nil || second.ID != ciInt(key.JobID) || !bytes.Equal(second.Usage, wire) || second.LinkHandle == "" {
		t.Fatalf("wrong exact row: %s", raw)
	}
	active.Store(false)
	if !bytes.Equal(call(), base) {
		t.Fatal("fallback changed original timing or handles")
	}
	active.Store(true)
	if !bytes.Equal(call(), base) {
		t.Fatal("revoked response resurrected")
	}
	if err := s.Publish(now.Add(time.Second), facts); err == nil {
		t.Fatal("accepted future timing")
	}
}
