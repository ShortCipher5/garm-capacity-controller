package incusapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var runnerRootExpectedFixture = RunnerRootVolumeExpected{
	Name: "fixture-runner", ControllerID: "11111111-1111-4111-8111-111111111111", PoolID: "22222222-2222-4222-8222-222222222222",
}

const rootVolumeFixture = `{
  "name":"fixture-runner","project":"fixture-project","type":"container","location":"fixture.daemon",
  "config":{"user.runner-controller-id":"11111111-1111-4111-8111-111111111111","user.runner-pool-id":"22222222-2222-4222-8222-222222222222","user.user-data":"test-private-bootstrap"},
  "expanded_config":{"user.runner-controller-id":"11111111-1111-4111-8111-111111111111","user.runner-pool-id":"22222222-2222-4222-8222-222222222222","user.user-data":"test-private-bootstrap"},
  "expanded_devices":{"actual-root":{"type":"disk","path":"/","pool":"fixture-storage","size":"10GiB"},"network":{"type":"nic"},"cache":{"type":"disk","path":"/cache","pool":"fixture-unrelated","source":"test-private-cache"}}
}`

func TestRunnerRootVolumeReadsExactlyOneScopedInstanceAndRedacts(t *testing.T) {
	var roots, instances atomic.Int32
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "project=fixture-project" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unscoped, non-read, or unauthenticated request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/1.0":
			roots.Add(1)
			writeSync(w, physicalRoot(server))
		case "/1.0/instances/fixture-runner":
			if roots.Load() != 2 {
				t.Error("instance read preceded trusted standalone check")
			}
			instances.Add(1)
			writeSync(w, json.RawMessage(rootVolumeFixture))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	client, err := New(server.URL, "fixture-project", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, invalid := range []RunnerRootVolumeExpected{
		{}, {Name: "/other/runner", ControllerID: runnerRootExpectedFixture.ControllerID, PoolID: runnerRootExpectedFixture.PoolID},
		{Name: "fixture-runner", ControllerID: "00000000-0000-0000-0000-000000000000", PoolID: runnerRootExpectedFixture.PoolID},
	} {
		if got, err := client.RunnerRootVolume(t.Context(), invalid); !errors.Is(err, ErrConfiguration) || got != (RunnerRootVolume{}) {
			t.Fatal("invalid subject did not fail before network")
		}
	}
	// Valid target arguments are not permission to choose a peer.
	if got, err := client.RunnerRootVolume(t.Context(), runnerRootExpectedFixture); !errors.Is(err, ErrConfiguration) || got != (RunnerRootVolume{}) || roots.Load() != 0 || instances.Load() != 0 {
		t.Fatalf("unpinned call read the backend: %+v %v, roots=%d instances=%d", got, err, roots.Load(), instances.Load())
	}
	identity, err := client.PhysicalIdentity(t.Context()) // separate fixture review
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PinPhysicalIdentity(identity); err != nil {
		t.Fatal(err)
	}
	got, err := client.RunnerRootVolume(t.Context(), runnerRootExpectedFixture)
	want := RunnerRootVolume{Instance: "fixture-runner", Project: "fixture-project", Location: "fixture.daemon", StoragePool: "fixture-storage"}
	if err != nil || got != want || roots.Load() != 2 || instances.Load() != 1 {
		t.Fatalf("expected exact scoped projection and 2 GETs after separate review: %+v %v, roots=%d instances=%d", got, err, roots.Load(), instances.Load())
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "test-private") || strings.Contains(string(encoded), "fixture-unrelated") || strings.Contains(string(encoded), "10GiB") {
		t.Fatal("projection retained non-allowlisted content")
	}
}

func TestRunnerRootVolumeRejectsAmbiguousIdentityAndRoot(t *testing.T) {
	raw := func(old, replacement string) string { return strings.Replace(rootVolumeFixture, old, replacement, 1) }
	for _, tc := range []struct{ name, payload string }{
		{"instance", raw(`"name":"fixture-runner"`, `"name":"other-runner"`)},
		{"project", raw(`"project":"fixture-project"`, `"project":"other-project"`)},
		{"missing project", raw(`"project":"fixture-project",`, "")},
		{"project alias", raw(`"project":"fixture-project"`, `"project":"fixture-project","Project":"other-project"`)},
		{"member path", raw(`"location":"fixture.daemon"`, `"location":"/private/root"`)},
		{"member null", raw(`"location":"fixture.daemon"`, `"location":null`)},
		{"VM", raw(`"type":"container"`, `"type":"virtual-machine"`)},
		{"missing direct stock tags", raw(`"config"`, `"ignored-config"`)},
		{"controller differs from effective tags", raw(runnerRootExpectedFixture.ControllerID, runnerRootExpectedFixture.PoolID)},
		{"stock pool alias", raw(`"user.runner-pool-id"`, `"user.runner-pool-id":"x","User.runner-pool-id"`)},
		{"missing root", raw(`"path":"/","pool":"fixture-storage"`, `"path":"/not-root","pool":"fixture-storage"`)},
		{"duplicate root", raw(`"cache":{"type":"disk","path":"/cache"`, `"cache":{"type":"disk","path":"/"`)},
		{"alternate root path", raw(`"cache":{"type":"disk","path":"/cache"`, `"cache":{"type":"disk","path":"//./"`)},
		{"path alias", raw(`"path":"/"`, `"path":"/","Path":"/other"`)},
		{"duplicate pool key", raw(`"pool":"fixture-storage"`, `"pool":"fixture-storage","pool":"other"`)},
		{"missing named pool", raw(`"pool":"fixture-storage",`, "")},
		{"root data source override", raw(`"pool":"fixture-storage"`, `"pool":"fixture-storage","source":"/private/root"`)},
		{"nonpath projected pool", raw(`"pool":"fixture-storage"`, `"pool":"../forbidden"`)},
		{"not disk", raw(`"type":"disk","path":"/"`, `"type":"proxy","path":"/"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseRunnerRootVolume([]byte(tc.payload), "fixture-project", runnerRootExpectedFixture); !errors.Is(err, ErrResponse) || got != (RunnerRootVolume{}) {
				t.Fatalf("mis-scoped reply retained parts of projected data: %+v %v", got, err)
			}
		})
	}
	allowed := raw(`"location":"fixture.daemon",`, "")
	allowed = strings.Replace(allowed, `"pool":"fixture-storage"`, `"pool":"fixture-storage","source":""`, 1)
	if got, err := parseRunnerRootVolume([]byte(allowed), "fixture-project", runnerRootExpectedFixture); err != nil || got.StoragePool != "fixture-storage" || got.Location != "" {
		t.Fatal("standalone omitted location/explicit empty source were not supported", err)
	}
}

func TestRunnerRootVolumeRefusesClusterBeforeInstanceRequest(t *testing.T) {
	var other atomic.Int32
	var clustered atomic.Bool
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0" {
			other.Add(1)
			http.NotFound(w, r)
			return
		}
		root := physicalRoot(server)
		root["environment"].(map[string]any)["server_clustered"] = clustered.Load()
		writeSync(w, root)
	}))
	client, err := New(server.URL, "fixture-project", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	identity, err := client.PhysicalIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PinPhysicalIdentity(identity); err != nil {
		t.Fatal(err)
	}
	clustered.Store(true)
	if got, err := client.RunnerRootVolume(t.Context(), runnerRootExpectedFixture); !errors.Is(err, ErrUnsupported) || got != (RunnerRootVolume{}) || other.Load() != 0 {
		t.Fatalf("unsupported cluster was queried: %+v %v", got, err)
	}
}
