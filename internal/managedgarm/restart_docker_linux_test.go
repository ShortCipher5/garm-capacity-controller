package managedgarm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests exercise HTTP classification with a private transport; public creation
// can only use a root-owned Unix socket and verifies the actual peer UID.
type rewriteDockerTransport struct{ target string }

func (r rewriteDockerTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	q = q.Clone(q.Context())
	q.URL.Scheme = "http"
	q.URL.Host = strings.TrimPrefix(r.target, "http://")
	return http.DefaultTransport.RoundTrip(q)
}
func TestRestartDockerRejectsUncertainOrAlreadyRunningStart(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, code := range []int{204, 304, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					if r.URL.Path != "/containers/"+id+"/start" || r.URL.RawQuery != "" {
						t.Error(r.URL)
					}
					w.WriteHeader(code)
					return
				}
				var c container
				c.ID = id
				c.State.Running = true
				c.State.Pid = 44
				json.NewEncoder(w).Encode(c)
			}))
			defer server.Close()
			d := restartDocker{client: &http.Client{Transport: rewriteDockerTransport{server.URL}}}
			err := d.transition(context.Background(), restartStep{id, "start"})
			if (err == nil) != (code == 204) || posts != 1 {
				t.Fatal(code, err, posts)
			}
		})
	}
}
func TestRestartDockerRefusesUnapprovedVerbsBeforeTransport(t *testing.T) {
	d := restartDocker{}
	for _, step := range []restartStep{{"bad", "stop"}, {strings.Repeat("a", 64), "remove"}} {
		if d.transition(context.Background(), step) == nil {
			t.Fatal("unapproved route accepted")
		}
	}
}
