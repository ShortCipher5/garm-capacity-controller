package adminview

import (
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

func TestCIHealthAPIOptionalReadOnly(t *testing.T) {
	h, _, token := fixture(t)
	const route = "/admin/api/v1/ci-health"
	if request(t, h, "GET", route, token).Code != 404 {
		t.Fatal("CI route enabled by default")
	}
	approved := []CIHealthRepository{{"github", 12}}
	s, err := NewCIHealthSnapshot(approved)
	if err != nil || h.SetCIHealthSnapshot(s) != nil || h.SetCIHealthSnapshot(s) == nil {
		t.Fatalf("invalid startup attachment: %v", err)
	}
	approved[0].RepositoryID = 14
	if request(t, h, "GET", route, "").Code != 401 ||
		request(t, h, "POST", route, token).Code != 405 ||
		request(t, h, "GET", route+"?repository=14", token).Code != 400 {
		t.Fatal("CI owner read boundary changed")
	}
	if !strings.Contains(request(t, h, "GET", route, token).Body.String(), `"state":"unavailable"`) {
		t.Fatal("published data not required")
	}
	at := time.Now().UTC().Add(-time.Second)
	f := cihealth.AttemptFacts{Key: cihealth.AttemptKey{Source: "github", RepositoryID: 12, WorkflowID: 2, RunID: 3, Attempt: 1}, AsOf: at}
	foreign := f
	foreign.Key.RepositoryID = 14
	if s.Publish(at, []cihealth.AttemptFacts{foreign}) == nil {
		t.Fatal("mutating startup input expanded bound repo scope")
	}
	if err := s.Publish(at, []cihealth.AttemptFacts{f}); err != nil {
		t.Fatal(err)
	}
	res := request(t, h, "GET", route, token)
	if res.Code != 200 || res.Header().Get("Cache-Control") != "no-store" || !strings.Contains(res.Body.String(), `"repository_id":"12"`) {
		t.Fatalf("not the exact owner read: %d %s", res.Code, res.Body.String())
	}
	s.Withdraw()
	if s.Publish(at, []cihealth.AttemptFacts{f}) == nil {
		t.Fatal("an older publication resurrected the result")
	}
	if strings.Contains(request(t, h, "GET", route, token).Body.String(), `"repository_id"`) {
		t.Fatal("withdrawn data still displayed")
	}
}

func TestCIHealthOverviewAdvertisesOnlyAnAttachedSourceToOwner(t *testing.T) {
	h, _, token := fixture(t)
	const path = "/admin/api/v1/overview"
	assertOverview := func(wantMarker bool) {
		t.Helper()
		auth := request(t, h, "GET", path, token)
		found := strings.Contains(auth.Body.String(), `"ci_health_available":true`)
		if auth.Code != 200 || found != wantMarker ||
			(!wantMarker && strings.Contains(auth.Body.String(), `"ci_health_available"`)) {
			t.Fatal("incorrect CI Health overview availability")
		}
		for _, rejected := range []string{"", strings.Repeat("0", 64)} {
			unauth := request(t, h, "GET", path, rejected)
			if unauth.Code != 401 || strings.Contains(unauth.Body.String(), "ci_health_available") {
				t.Fatal("unauthorized overview leaked CI availability")
			}
		}
	}
	assertOverview(false)
	if h.SetCIHealthSnapshot(nil) == nil || h.SetCIHealthSnapshot(&CIHealthSnapshot{}) == nil {
		t.Fatal("invalid source attachment")
	}
	assertOverview(false)
	source, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 17}})
	if err != nil || h.SetCIHealthSnapshot(source) != nil {
		t.Fatalf("attach configured source: %v", err)
	}
	assertOverview(true)
	// Availability advertises the attached owner read lane, not data presence.
	source.Withdraw()
	assertOverview(true)
}

func TestCIHealthPublisherNeverResurrectsExpiredView(t *testing.T) {
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := at
	s.clock = func() time.Time { return now }
	if err := s.Publish(at, nil); err != nil {
		t.Fatal(err)
	}
	now = at.Add(ciDisplayLifetime - time.Nanosecond)
	if s.read() == nil {
		t.Fatal("unexpired snapshot unavailable")
	}
	now = at.Add(time.Second)
	if s.read() != nil {
		t.Fatal("rollback to after publication resurrected a reading")
	}
	now = at.Add(ciDisplayLifetime)
	if s.read() != nil {
		t.Fatal("expired snapshot remains visible")
	}
}

func TestCIHealthPublisherWithdrawInvalidatesInFlightLease(t *testing.T) {
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	at := now
	lease := s.begin(at)
	if lease == nil {
		t.Fatal("publication not reserved")
	}
	s.Withdraw()
	if s.commit(at, lease, []byte("old data"), nil) == nil || s.read() != nil {
		t.Fatal("withdrawal allowed older in-flight data")
	}
	now = now.Add(time.Second)
	if err := s.Publish(now, nil); err != nil || s.read() == nil {
		t.Fatalf("fresh publication blocked: %v", err)
	}
	now = now.Add(ciDisplayLifetime)
	if s.read() != nil {
		t.Fatal("read ignored exact expiry")
	}
}

func TestCIHealthWithdrawUnderRollbackFencesHighestObservedTime(t *testing.T) {
	s, err := NewCIHealthSnapshot([]CIHealthRepository{{"github", 12}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := at
	s.clock = func() time.Time { return now }
	if err := s.Publish(at, nil); err != nil {
		t.Fatal(err)
	}
	high := at.Add(40 * time.Second)
	now = high
	if s.read() == nil {
		t.Fatal("no initial read")
	}
	now = at.Add(10 * time.Second)
	s.Withdraw()
	now = high
	if s.Publish(at.Add(20*time.Second), nil) == nil || s.Publish(high, nil) == nil || s.read() != nil {
		t.Fatal("observation at or below rollback withdrawal watermark was published")
	}
	now = high.Add(time.Second)
	if err := s.Publish(now, nil); err != nil || s.read() == nil {
		t.Fatalf("fresh observation after watermark did not publish: %v", err)
	}
}
