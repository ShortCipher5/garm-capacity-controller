package managedgarm

import (
	"encoding/json"
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

func TestStockPoolUpdateExactExistingWireShape(t *testing.T) {
	for _, wire := range []string{
		`{"max_runners":0}`, `{"max_runners":4}`, `{"max_runners":1000000}`,
		`{"enabled":false}`, `{"max_runners":0,"enabled":false}`, `{"max_runners":4,"enabled":false}`,
	} {
		u, err := decodeStockPoolUpdate([]byte(wire))
		if err != nil {
			t.Fatalf("rejected existing wire shape %s: %v", wire, err)
		}
		got, err := json.Marshal(u)
		if err != nil || string(got) != wire {
			t.Fatalf("noncanonical emitted stock shape %s: %s, %v", wire, got, err)
		}
	}

	for _, bad := range []string{
		`{}`, `null`, `[]`, `{"max_runners":0}{}`,
		`{"max_runners":1,"max_runners":2}`, `{"enabled":false,"enabled":false}`,
		`{"MAX_RUNNERS":2}`, `{"max_runnerſ":2}`, `{"Enabled":false}`,
		`{"max_runners":1,"MAX_RUNNERS":2}`, `{"max_runners":1,"max_runner\u0073":2}`,
		`{"max_runners":null,"enabled":false}`, `{"max_runners":0,"enabled":null}`, `{"max_runners":1000001}`,
		`{"max_runners":-1}`, `{"max_runners":"2"}`, `{"max_runners":2.0}`, `{"max_runners":2e0}`,
		`{"enabled":true}`, `{"max_runners":4,"min_idle_runners":1}`, `{"max_runners":4,"priority":0}`,
		`{"max_runners":4,"image":"other"}`, `{"max_runners":4,"tags":[]}`,
		strings.Repeat(" ", 4097) + `{"max_runners":4}`,
	} {
		if u, err := decodeStockPoolUpdate([]byte(bad)); err == nil || u.Maximum != nil || u.Enabled != nil {
			t.Fatalf("malformed or overbroad request retained a partial update: %q", bad)
		}
	}
}

func TestFreshMaximumReductionUsesOneExactStockReadShape(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	raw := func(fields string) []byte { return []byte(fmt.Sprintf(`{"id":%q,%s}`, pool, fields)) }
	if err := validateMaximumReduction(raw(`"max_runners":10,"min_idle_runners":3,"enabled":true,"provider_name":"fixture"`), pool, 4); err != nil {
		t.Fatalf("canonical stock response includes other provider fields: %v", err)
	}
	for _, fields := range []string{
		`"max_runners":10,"min_idle_runners":3,"min_idle_runnerſ":0`,
		`"max_runners":10,"min_idle_runnerſ":0`,
		`"MAX_RUNNERS":10,"min_idle_runners":3`,
		`"max_runners":10,"max_runnerſ":20,"min_idle_runners":3`,
		`"max_runners":10,"min_idle_runners":3,"ID":null`,
		`"max_runners":10,"min_idle_runners":3,"unrelated":1,"UNRELATED":2`,
	} {
		if err := validateMaximumReduction(raw(fields), pool, 4); err == nil {
			t.Fatalf("ambiguous stock evidence accepted: %s", fields)
		}
	}
}

func TestFreshMaximumReductionDoesNotInferZeroFromStockOmission(t *testing.T) {
	pool := "11111111-1111-4111-8111-111111111111"
	// This is also the shape emitted for a zero minimum by the pinned stock
	// Pool serializer. Without running serializer provenance the same shape
	// could be incomplete evidence; the protected session must not infer zero.
	raw := []byte(fmt.Sprintf(`{"id":%q,"max_runners":10,"provider_name":"fixture"}`, pool))
	if err := validateMaximumReduction(raw, pool, 4); err == nil {
		t.Fatal("missing minimum treated as an authenticated zero")
	}
}
