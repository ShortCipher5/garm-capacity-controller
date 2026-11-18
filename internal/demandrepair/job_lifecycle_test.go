package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	common "github.com/cloudbase/garm-provider-common/params"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticJobLifecycleExactScopeAndIdentity(t *testing.T) {
	for _, mode := range []string{"completed", "in_progress", "wrong-runner", "wrong-name", "wrong-url", "duplicate", "missing-attempt", "queued", "redirect", "zero-observation", "wrong-repository"} {
		t.Run(mode, func(t *testing.T) {
			var endpoint string
			jobReads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/metadata/credentials/runner" {
					if r.Header.Get("Authorization") != "Bearer metadata-token" {
						t.Error("wrong metadata token")
					}
					fmt.Fprint(w, `{"agentId":17,"agentName":"runner-a","gitHubUrl":"https://github.example/owner/repo","ephemeral":true}`)
					return
				}
				jobReads++
				if r.Method != "GET" || r.URL.Path != "/repos/owner/repo/actions/jobs/91" || r.Header.Get("Authorization") != "Bearer github-token" {
					t.Error("wrong scoped request")
				}
				if mode == "redirect" {
					w.Header().Set("Location", endpoint+"/elsewhere")
					w.WriteHeader(302)
					return
				}
				now := time.Now().UTC()
				status := "completed"
				var conclusion any = "success"
				var completed any = now.Add(-time.Second)
				if mode == "in_progress" {
					status = "in_progress"
					conclusion = nil
					completed = nil
				}
				if mode == "queued" {
					status = "queued"
				}
				data := map[string]any{"id": 91, "run_id": 12, "run_attempt": 1, "runner_id": 17, "runner_name": "runner-a", "url": endpoint + "/repos/owner/repo/actions/jobs/91", "run_url": endpoint + "/repos/owner/repo/actions/runs/12", "status": status, "conclusion": conclusion, "started_at": now.Add(-time.Minute), "completed_at": completed}
				switch mode {
				case "wrong-runner":
					data["runner_id"] = 18
				case "wrong-name":
					data["runner_name"] = "other"
				case "wrong-url":
					data["url"] = endpoint + "/repos/other/repo/actions/jobs/91"
				case "missing-attempt":
					delete(data, "run_attempt")
				}
				raw, _ := json.Marshal(data)
				if mode == "duplicate" {
					raw = append([]byte(`{"runner_id":17,`), raw[1:]...)
				}
				w.Write(raw)
			}))
			defer server.Close()
			endpoint = server.URL
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			trust := &tls.Config{RootCAs: roots}
			metadata, e := stockjit.New(stockjit.Policy{MetadataURL: endpoint + "/api/v1/metadata", ControllerID: "controller", PoolID: "pool", GitHubURL: "https://github.example/owner/repo"}, trust)
			if e != nil {
				t.Fatal(e)
			}
			defer metadata.Close()
			identity, e := metadata.Observe(context.Background(), "controller", common.BootstrapInstance{Name: "runner-a", PoolID: "pool", MetadataURL: endpoint + "/api/v1/metadata", RepoURL: "https://github.example/owner/repo", InstanceToken: "metadata-token", JitConfigEnabled: true})
			if e != nil {
				t.Fatal(e)
			}
			repo := Repository{GarmID: "repo", Host: "github.example", Owner: "owner", Name: "repo"}
			client, e := NewGitHub(endpoint, "github.example", "private", func(context.Context, string) (string, error) { return "github-token", nil }, trust, []Repository{repo})
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			if mode == "zero-observation" {
				identity = stockjit.Observation{}
			}
			if mode == "wrong-repository" {
				repo.Name = "other"
			}
			o, e := client.ObserveJobLifecycle(context.Background(), repo, 91, identity)
			if mode == "completed" || mode == "in_progress" {
				if e != nil || !o.Valid() || o.Record().Runner.RunnerID != 17 || o.Record().Status != mode {
					t.Fatal(o.Record(), e)
				}
				if o.Record().CompletedAt != nil {
					r := o.Record()
					*r.CompletedAt = time.Time{}
					if o.Record().CompletedAt.IsZero() {
						t.Fatal("mutable observation")
					}
				}
			} else if e == nil || o.Valid() {
				t.Fatal("untrusted lifecycle accepted")
			}
			if (mode == "zero-observation" || mode == "wrong-repository") && jobReads != 0 {
				t.Fatal("scope denied after contact")
			}
		})
	}
}
func TestLifecycleParserRejectsAliasesAndMissingFields(t *testing.T) {
	for _, raw := range []string{`{"ID":1}`, `{"id":1,"id":1}`, `null`, `[]`, strings.Repeat("x", 12)} {
		var v lifecycleResponse
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatal("ambiguous input")
		}
	}
}
