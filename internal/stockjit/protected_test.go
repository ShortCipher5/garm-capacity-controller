package stockjit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	common "github.com/cloudbase/garm-provider-common/params"
)

func TestFetchProtectedAuthenticFilesAndRejection(t *testing.T) {
	for _, mode := range []string{"ok", "changed-runner", "wrong-name", "mutable-runner", "missing-file", "redirect", "wrong-origin", "wrong-image", "large"} {
		t.Run(mode, func(t *testing.T) {
			calls := []string{}
			runner := `{"agentId":123,"agentName":"runner-one","gitHubUrl":"https://github.com/example/repo","ephemeral":true,"workFolder":"_work","disableUpdate":true}`
			if mode == "wrong-name" {
				runner = strings.ReplaceAll(runner, "runner-one", "other")
			}
			if mode == "mutable-runner" {
				runner = strings.ReplaceAll(runner, `"disableUpdate":true`, `"disableUpdate":false`)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || (strings.HasPrefix(r.URL.Path, "/api/v1/metadata") && r.Header.Get("Authorization") != "Bearer private-token") {
					t.Error("incorrect credential scope")
				}
				calls = append(calls, r.URL.Path)
				if mode == "redirect" {
					http.Redirect(w, r, "https://elsewhere.invalid", 302)
					return
				}
				switch r.URL.Path {
				case "/api/v1/pools/11111111-1111-4111-8111-111111111111":
					json.NewEncoder(w).Encode(map[string]any{"id": "11111111-1111-4111-8111-111111111111", "repo_id": "22222222-2222-4222-8222-222222222222", "provider_name": "stock", "flavor": "small", "image": "image", "os_type": "linux", "os_arch": "amd64"})
				case "/api/v1/instances/runner-one":
					json.NewEncoder(w).Encode(map[string]any{"id": "33333333-3333-4333-8333-333333333333", "name": "runner-one", "pool_id": "11111111-1111-4111-8111-111111111111", "provider_name": "stock", "agent_id": 123, "status": "creating", "runner_status": "pending"})
				case "/api/v1/metadata/credentials/runner":
					body := runner
					if mode == "changed-runner" && len(calls) == 6 {
						body = strings.ReplaceAll(body, "123", "124")
					}
					fmt.Fprint(w, body)
				case "/api/v1/metadata/credentials/credentials":
					if mode == "missing-file" {
						w.WriteHeader(404)
						return
					}
					if mode == "large" {
						fmt.Fprint(w, strings.Repeat("x", (1<<19)+1))
						return
					}
					fmt.Fprint(w, `{"scheme":"OAuth","secret":"credential-fixture"}`)
				case "/api/v1/metadata/credentials/credentials_rsaparams":
					fmt.Fprint(w, `{"D":"private-rsa-fixture"}`)
				default:
					t.Error("unexpected path")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			policy := Policy{MetadataURL: server.URL + "/api/v1/metadata", ControllerID: "controller", PoolID: "11111111-1111-4111-8111-111111111111", GitHubURL: "https://github.com/example/repo"}
			client, e := New(policy, &tls.Config{RootCAs: roots})
			if e != nil {
				t.Fatal(e)
			}
			defer client.Close()
			b := common.BootstrapInstance{Name: "runner-one", PoolID: "11111111-1111-4111-8111-111111111111", RepoURL: policy.GitHubURL, MetadataURL: policy.MetadataURL, JitConfigEnabled: true, InstanceToken: "private-token"}
			files := map[string]string{"bin/Runner.Listener": strings.Repeat("1", 64), "bin/Runner.Listener.dll": strings.Repeat("2", 64)}
			raw, _ := json.Marshal(files)
			image := protectedboot.ImageDescriptor{InitSHA256: strings.Repeat("3", 64), LauncherSHA256: strings.Repeat("4", 64), BundleDigest: protectedboot.Hash(raw), RunnerRelease: "2.321.0", UID: 1001, GID: 1001, Files: files}
			if mode == "wrong-origin" {
				b.MetadataURL = "https://attacker.invalid/api/v1/metadata"
			}
			if mode == "wrong-image" {
				image.UID = 0
			}
			result, e := client.FetchProtected(context.Background(), "controller", b, image, stockidentity.Configuration{Endpoint: server.URL, Token: "admin", Provider: "stock", Pool: policy.PoolID, TLS: &tls.Config{RootCAs: roots}})
			if mode != "ok" {
				if e == nil || result.Valid() || result.Payload() != "" {
					t.Fatal("invalid source produced bootstrap")
				}
				if (mode == "wrong-origin" || mode == "wrong-image") && len(calls) != 0 {
					t.Fatal("credentials transmitted before validation")
				}
				return
			}
			if e != nil || !result.Valid() || result.Observation().Record().RunnerID != 123 || len(calls) != 8 {
				t.Fatal("valid source rejected", e, len(calls))
			}
			payload, e := protectedboot.Decode([]byte(result.Payload()))
			if e != nil {
				t.Fatal(e)
			}
			jit, _ := base64.StdEncoding.DecodeString(payload.JIT)
			var got map[string]string
			json.Unmarshal(jit, &got)
			actual, _ := base64.StdEncoding.DecodeString(got[".runner"])
			if string(actual) != runner {
				t.Fatal("runner flags rewritten")
			}
			if _, e = json.Marshal(result); e == nil {
				t.Fatal("secret serialized")
			}
			if strings.Contains(fmt.Sprintf("%v %#v", result, result), "credential-fixture") {
				t.Fatal("format leaks credentials")
			}
		})
	}
}
