package incusapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const deleteOperation = "87654321-4321-4321-a321-cba987654321"

func TestDeleteSendsExactlyOneForceDeleteAndReturnsOperation(t *testing.T) {
	var deletes, posts atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0" {
			writeSync(w, rootMetadata())
			return
		}
		if r.URL.Path != "/1.0/instances/runner-1" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("force") != "true" || r.URL.Query().Get("project") != "runners" {
			t.Errorf("unsafe delete scope: %s", r.URL.RawQuery)
		}
		deletes.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + deleteOperation + "?project=runners", "metadata": map[string]any{"id": deleteOperation}})
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deletion, err := client.Delete(context.Background(), "runner-1")
	if err != nil || deletion.OperationID != deleteOperation || deletion.ResourceID != "runner-1" || deletes.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("deletion %+v %v deletes=%d", deletion, err, deletes.Load())
	}
}

func TestDeleteRejectsUnsafeNamesAndSyncResponses(t *testing.T) {
	var deletes atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0" {
			writeSync(w, rootMetadata())
			return
		}
		deletes.Add(1)
		writeSync(w, map[string]any{"id": deleteOperation})
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, bad := range []string{"", "../runner-1", "runner_1", strings.Repeat("a", 64), "runner-1?project=other"} {
		if _, err := client.Delete(context.Background(), bad); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("unsafe name %q accepted: %v", bad, err)
		}
	}
	if err := client.WaitDeletion(context.Background(), Deletion{OperationID: deleteOperation, ResourceID: "../x"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unsafe wait deletion accepted: %v", err)
	}
	if err := client.WaitDeletion(context.Background(), Deletion{OperationID: "not-a-uuid", ResourceID: "runner-1"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unsafe wait operation accepted: %v", err)
	}
	// A sync (non-async) DELETE response is not an accepted dispatch.
	if _, err := client.Delete(context.Background(), "runner-1"); !errors.Is(err, ErrResponse) {
		t.Fatalf("sync delete accepted: %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("unexpected delete attempts %d", deletes.Load())
	}
}

func TestWaitDeletionPollsOperationToTerminalSuccess(t *testing.T) {
	var polls atomic.Int32
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, rootMetadata())
		case "/1.0/operations/" + deleteOperation:
			status := 200
			if polls.Add(1) == 1 {
				status = 103
			}
			writeSync(w, map[string]any{"id": deleteOperation, "status_code": status, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.WaitDeletion(context.Background(), Deletion{OperationID: deleteOperation, ResourceID: "runner-1"}); err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls=%d want 2", polls.Load())
	}
}

func TestWaitDeletionFailureMismatchAndDroppedResponseNeverConfirm(t *testing.T) {
	for _, mode := range []string{"failure", "wrong-id", "wrong-instance", "no-resources", "transport"} {
		t.Run(mode, func(t *testing.T) {
			var polls atomic.Int32
			server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/1.0" {
					writeSync(w, rootMetadata())
					return
				}
				polls.Add(1)
				switch mode {
				case "transport":
					http.Error(w, "private-delete-fault", http.StatusBadGateway)
				case "failure":
					writeSync(w, map[string]any{"id": deleteOperation, "status_code": 400, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
				case "wrong-id":
					writeSync(w, map[string]any{"id": operation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}})
				case "wrong-instance":
					writeSync(w, map[string]any{"id": deleteOperation, "status_code": 200, "resources": map[string][]string{"instances": {"/1.0/instances/other?project=runners"}}})
				default:
					writeSync(w, map[string]any{"id": deleteOperation, "status_code": 200, "resources": map[string][]string{}})
				}
			}))
			client, err := New(server.URL, "runners", credentials)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			err = client.WaitDeletion(context.Background(), Deletion{OperationID: deleteOperation, ResourceID: "runner-1"})
			switch mode {
			case "failure":
				if !errors.Is(err, ErrOperation) {
					t.Fatalf("failure not typed: %v", err)
				}
			case "transport":
				if !errors.Is(err, ErrTransport) || strings.Contains(err.Error(), "private-delete-fault") {
					t.Fatalf("unsafe transport error: %v", err)
				}
			default:
				if !errors.Is(err, ErrResponse) {
					t.Fatalf("malformed terminal accepted: %v", err)
				}
			}
			if polls.Load() != 1 {
				t.Fatalf("failure retried polls=%d", polls.Load())
			}
		})
	}
}
