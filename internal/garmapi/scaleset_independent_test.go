package garmapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestIndependentScaleSetMalformedTailNoPartialObservation(t *testing.T) {
	for _, bad := range []string{`{"id":12,"provider_name":"capacity","state":"created","name":"bad","org_id":"invalid"}`, `{"id":3,"provider_name":"capacity"}`, `{"id":"12","provider_name":"capacity"}`} {
		server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/api/v1/scalesets" {
				t.Error("unexpected effect")
			}
			first, _ := json.Marshal(scaleSetRow(3, "capacity"))
			w.Write([]byte("[" + string(first) + "," + bad + "]"))
		}))
		c, e := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
		if e != nil {
			t.Fatal(e)
		}
		got, e := c.DiscoverScaleSets(context.Background())
		if e == nil || len(got.ScaleSets) != 0 {
			t.Fatal("partial collection exposed", e, got)
		}
		c.Close()
		server.Close()
	}
}
func TestIndependentScaleSetNumericIdentityProviderAndRedaction(t *testing.T) {
	row := scaleSetRow(41, "capacity")
	row["scale_set_id"] = 999
	other := scaleSetRow(999, "other")
	other["name"] = "private-foreign-name"
	server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode([]any{row, other}) }))
	defer server.Close()
	c, e := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	got, e := c.DiscoverScaleSets(context.Background())
	if e != nil || len(got.ScaleSets) != 1 || got.ScaleSets[0].ID != 41 {
		t.Fatal("numeric scope confusion", got, e)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "scale_set_id") {
		t.Fatal("identity or private field leak")
	}
	if len(c.management.pools) != 0 || len(c.management.client.scalesets) != 0 || c.management.writes {
		t.Fatal("read expanded authorization")
	}
}
