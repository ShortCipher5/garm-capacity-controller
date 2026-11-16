package garmapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestInventoryNativeRowIdentityUniqueness(t *testing.T) {
	const first = "11111111-1111-4111-8111-111111111111"
	const second = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name, id1, id2, provider2 string
		fail                      bool
	}{
		{"duplicate-under-different-names", first, first, "capacity", true},
		{"duplicate-across-scoped-providers", first, first, "capacity-two", true},
		{"distinct", first, second, "capacity", false},
		{"legacy-omitted", "", "", "capacity", false},
		{"mixed", first, "", "capacity", false},
		{"unrelated-provider-excluded", first, first, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []map[string]any{{"provider_name": "capacity", "name": "one", "pool_id": "pool-a", "status": "running"}, {"provider_name": tc.provider2, "name": "two", "pool_id": "pool-b", "status": "running"}}
			if tc.id1 != "" {
				rows[0]["id"] = tc.id1
			}
			if tc.id2 != "" {
				rows[1]["id"] = tc.id2
			}
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(rows) }))
			client, e := New(server.URL, "disposable-test-token", trust, []string{"capacity", "capacity-two"}, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			got, e := client.Instances(context.Background())
			if tc.fail {
				if !errors.Is(e, ErrInventory) || got != nil {
					t.Fatal("ambiguous identity returned partial inventory", got, e)
				}
				return
			}
			want := 2
			if tc.provider2 == "other" {
				want = 1
			}
			if e != nil || len(got) != want {
				t.Fatal(got, e)
			}
		})
	}
}
