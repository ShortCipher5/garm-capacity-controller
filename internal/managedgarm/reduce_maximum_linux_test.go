package managedgarm

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestFreshMaximumReductionNeverRaisesOrChangesIdle(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	for _, c := range []struct {
		max, min, target uint64
		ok               bool
	}{{10, 2, 5, true}, {10, 2, 2, true}, {10, 2, 1, false}, {10, 0, 0, false}, {10, 0, 10, false}, {10, 0, 11, false}, {0, 0, 1, false}, {2, 3, 1, false}, {1000001, 0, 1, false}} {
		raw := []byte(fmt.Sprintf(`{"id":%q,"max_runners":%d,"min_idle_runners":%d}`, pool, c.max, c.min))
		if (validateMaximumReduction(raw, pool, c.target) == nil) != c.ok {
			t.Fatal(c)
		}
	}
	for _, raw := range []string{`{}`, `{"id":"other","max_runners":10,"min_idle_runners":0}`, fmt.Sprintf(`{"id":%q,"max_runners":10,"max_runners":20,"min_idle_runners":0}`, pool), fmt.Sprintf(`{"id":%q,"max_runners":null,"min_idle_runners":0}`, pool), fmt.Sprintf(`{"id":%q,"max_runners":10,"min_idle_runners":null}`, pool)} {
		if validateMaximumReduction([]byte(raw), pool, 2) == nil {
			t.Fatal("ambiguous current pool accepted")
		}
	}
}

func TestPositiveReductionStillRequiresActualSessionBoundary(t *testing.T) {
	s := &Session{token: "synthetic-unqualified"}
	r, _ := http.NewRequest("PUT", "https://managed-garm.invalid/api/v1/pools/11111111-1111-4111-8111-111111111111", strings.NewReader(`{"max_runners":2}`))
	if _, e := s.RoundTrip(r); e == nil {
		t.Fatal("unqualified session gained positive dispatch")
	}
}
