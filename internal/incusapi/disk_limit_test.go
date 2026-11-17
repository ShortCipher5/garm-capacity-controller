package incusapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// A disk bound must arrive as an instance-level root device size override.
// Absent bounds must not send a devices map at all.
func TestCreateSendsRootDiskSizeOnlyWhenBounded(t *testing.T) {
	var bodies atomic.Int32
	var last map[string]any
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, rootMetadata())
		case "/1.0/instances":
			bodies.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("invalid request")
			}
			last = body
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	bounded := validSpec()
	bounded.DiskMiB = 5120
	if _, err := client.Create(context.Background(), bounded); err != nil {
		t.Fatal(err)
	}
	devices, ok := last["devices"].(map[string]any)
	if !ok {
		t.Fatalf("disk bound not sent as devices: %+v", last)
	}
	root, ok := devices["root"].(map[string]any)
	if !ok || root["type"] != "disk" || root["path"] != "/" || root["size"] != "5120MiB" {
		t.Fatalf("unsafe root disk device: %+v", devices)
	}

	unbounded := validSpec()
	if _, err := client.Create(context.Background(), unbounded); err != nil {
		t.Fatal(err)
	}
	if _, present := last["devices"]; present {
		t.Fatalf("unbounded spec sent devices: %+v", last)
	}
	if bodies.Load() != 2 {
		t.Fatalf("unexpected create count %d", bodies.Load())
	}
}

func TestValidateSpecRejectsNegativeDisk(t *testing.T) {
	spec := validSpec()
	spec.DiskMiB = -1
	if ValidateSpec(spec) == nil {
		t.Fatal("negative disk bound accepted")
	}
	spec.DiskMiB = 0
	if ValidateSpec(spec) != nil {
		t.Fatal("zero disk bound rejected")
	}
}
