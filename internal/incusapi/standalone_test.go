package incusapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestTopologyMustExplicitlyReportStandaloneBeforeCreateOrInventory(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		want           error
	}{
		{"clustered", `{"api_version":"1.0","auth":"trusted","api_extensions":["instance_create_start"],"environment":{"server_clustered":true}}`, ErrUnsupported},
		{"missing-environment", `{"api_version":"1.0","auth":"trusted","api_extensions":["instance_create_start"]}`, ErrResponse},
		{"null-environment", `{"api_version":"1.0","auth":"trusted","environment":null}`, ErrResponse},
		{"array-environment", `{"api_version":"1.0","auth":"trusted","environment":[]}`, ErrResponse},
		{"missing-clustered", `{"api_version":"1.0","auth":"trusted","environment":{}}`, ErrResponse},
		{"null-clustered", `{"api_version":"1.0","auth":"trusted","environment":{"server_clustered": null }}`, ErrResponse},
		{"string-clustered", `{"api_version":"1.0","auth":"trusted","environment":{"server_clustered":"false"}}`, ErrResponse},
		{"number-clustered", `{"api_version":"1.0","auth":"trusted","environment":{"server_clustered":0}}`, ErrResponse},
		{"casefold-environment", `{"api_version":"1.0","auth":"trusted","Environment":{"server_clustered":false}}`, ErrResponse},
		{"casefold-clustered", `{"api_version":"1.0","auth":"trusted","environment":{"Server_Clustered":false}}`, ErrResponse},
		{"duplicate-clustered", `{"api_version":"1.0","auth":"trusted","environment":{"server_clustered":true,"server_clustered":false}}`, ErrResponse},
		{"duplicate-environment", `{"api_version":"1.0","auth":"trusted","environment":{"server_clustered":true},"environment":{"server_clustered":false}}`, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var roots, other atomic.Int32
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/1.0" {
					roots.Add(1)
					_, _ = w.Write([]byte(`{"type":"sync","status_code":200,"metadata":` + tc.metadata + `}`))
					return
				}
				other.Add(1)
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			for _, call := range []func() error{
				func() error { _, err := client.Create(context.Background(), validSpec()); return err },
				func() error { _, err := client.Instance(context.Background(), "runner-1"); return err },
				func() error { _, err := client.Instances(context.Background()); return err },
				func() error { _, err := client.Operations(context.Background()); return err },
				func() error {
					return client.Wait(context.Background(), Created{OperationID: operation, ResourceID: "runner-1"})
				},
			} {
				if err := call(); !errors.Is(err, tc.want) {
					t.Fatalf("topology accepted or wrong classification: %v", err)
				}
			}
			if roots.Load() != 5 || other.Load() != 0 {
				t.Fatalf("unsafe route reached before topology proof: roots=%d other=%d", roots.Load(), other.Load())
			}
		})
	}
}

func TestStandaloneTopologyIsRecheckedAndNotCached(t *testing.T) {
	var clustered atomic.Bool
	var rootCalls atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0" {
			t.Error("unexpected route")
			http.NotFound(w, r)
			return
		}
		rootCalls.Add(1)
		value := "false"
		if clustered.Load() {
			value = "true"
		}
		_, _ = w.Write([]byte(`{"type":"sync","status_code":200,"metadata":{"api_version":"1.0","auth":"trusted","api_extensions":["instance_create_start"],"environment":{"server_clustered":` + value + `}}}`))
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.trusted(context.Background(), false); err != nil {
		t.Fatalf("explicit false rejected: %v", err)
	}
	clustered.Store(true)
	if err := client.trusted(context.Background(), false); !errors.Is(err, ErrUnsupported) {
		t.Fatal("topology change was cached")
	}
	if rootCalls.Load() != 2 {
		t.Fatal("root not rechecked")
	}
}

func TestWaitRechecksTrustAndTopologyAfterCreateAndAcrossPolls(t *testing.T) {
	for _, tc := range []struct {
		name              string
		create, pending   bool
		loss              string
		failAt, wantWaits int32
		want              error
	}{
		{"cluster-after-create", true, false, "cluster", 2, 0, ErrUnsupported},
		{"unknown-after-create", true, false, "unknown", 2, 0, ErrResponse},
		{"untrusted-after-create", true, false, "untrusted", 2, 0, ErrUnsupported},
		{"cluster-after-pending-poll", true, true, "cluster", 3, 1, ErrUnsupported},
		{"unknown-after-pending-poll", true, true, "unknown", 3, 1, ErrResponse},
		{"cluster-before-final-state", false, false, "cluster", 2, 1, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var roots, posts, waits, states atomic.Int32
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/1.0":
					n := roots.Add(1)
					metadata := map[string]any{"api_version": "1.0", "auth": "trusted", "api_extensions": []string{"instance_create_start"}, "environment": map[string]any{"server_clustered": false}}
					if n >= tc.failAt {
						switch tc.loss {
						case "cluster":
							metadata["environment"] = map[string]any{"server_clustered": true}
						case "unknown":
							delete(metadata, "environment")
						case "untrusted":
							metadata["auth"] = "untrusted"
						}
					}
					writeSync(w, metadata)
				case "/1.0/instances":
					if r.Method != http.MethodPost {
						t.Error("unexpected method")
						return
					}
					posts.Add(1)
					_, _ = w.Write([]byte(`{"type":"async","status_code":100,"operation":"/1.0/operations/` + operation + `?project=runners","metadata":{"id":"` + operation + `"}}`))
				case "/1.0/operations/" + operation + "/wait":
					waits.Add(1)
					status := 200
					if tc.pending {
						status = 103
					}
					writeSync(w, map[string]any{"id": operation, "status_code": status, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
				case "/1.0/instances/runner-1/state":
					states.Add(1)
					writeSync(w, map[string]any{"status_code": 103})
				default:
					t.Error("unexpected route")
					http.NotFound(w, r)
				}
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			created := Created{OperationID: operation, ResourceID: "runner-1"}
			if tc.create {
				created, err = client.Create(context.Background(), validSpec())
				if err != nil {
					t.Fatalf("initial standalone Create failed: %v", err)
				}
			}
			if err := client.Wait(context.Background(), created); !errors.Is(err, tc.want) {
				t.Fatalf("Wait accepted lost trust/topology: %v", err)
			}
			wantPosts := int32(0)
			if tc.create {
				wantPosts = 1
			}
			if posts.Load() != wantPosts || waits.Load() != tc.wantWaits || states.Load() != 0 {
				t.Fatalf("untrusted continuation: posts=%d waits=%d states=%d", posts.Load(), waits.Load(), states.Load())
			}
		})
	}
}
