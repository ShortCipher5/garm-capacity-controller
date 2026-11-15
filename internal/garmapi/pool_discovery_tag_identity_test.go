package garmapi

import (
	"context"
	"net/http"
	"testing"
)

func TestDiscoveryRejectsAmbiguousTagNames(t *testing.T) {
	for _, tags := range []string{`[{"name":"linux","Name":"windows"}]`, `[{"name":"linux","NAME":"windows"}]`, `[{"name":"linux","name":"windows"}]`} {
		t.Run(tags, func(t *testing.T) {
			body := `[{"id":"` + managedPoolID + `","provider_name":"capacity","generation":1,"tags":` + tags + `}]`
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			client, err := NewPoolDiscovery(server.URL, "token", trust, []string{"capacity"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.DiscoverPools(context.Background())
			if err == nil || result.Pools != nil || result.MissingConfiguredPoolIDs != nil {
				t.Fatal("ambiguous tag observation accepted")
			}
		})
	}
}
