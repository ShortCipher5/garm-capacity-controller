package incusapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const evidenceUUID = "11111111-1111-4111-8111-111111111111"
const evidenceGeneration = "22222222-2222-4222-8222-222222222222"
const evidenceOtherUUID = "33333333-3333-4333-8333-333333333333"

func expectedIdentity() IdentityExpectation {
	return IdentityExpectation{Name: "runner-1", ControllerID: "controller-1", PoolID: "pool-1", ReservationKey: strings.Repeat("b", 64), CPUMillis: 1000, RAMMiB: 1024}
}
func instanceEvidenceBody() map[string]any {
	return map[string]any{"name": "runner-1", "status_code": 103, "created_at": "2026-01-01T00:00:00Z", "expanded_config": map[string]any{"limits.cpu": "1", "limits.memory": "1024MiB", "user.runner-controller-id": "controller-1", "user.runner-pool-id": "pool-1", "user.garm-capacity.reservation": strings.Repeat("b", 64), "volatile.uuid": evidenceUUID, "volatile.uuid.generation": evidenceGeneration, "user.user-data": "secret-bootstrap-must-not-retain"}}
}
func operationEvidenceBody(kind RecoveryOperationKind) map[string]any {
	description := "Creating instance"
	if kind == RecoveryDelete {
		description = "Deleting instance"
	}
	return map[string]any{"id": operation, "class": "task", "description": description, "status_code": 200, "status": "Success", "err": "", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:01:00Z", "resources": map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}}, "metadata": map[string]any{"secret": "private-metadata-must-not-retain"}}
}

type terminalFixture struct {
	client                        *Client
	capture                       atomic.Bool
	opSeen                        atomic.Bool
	writes, instances, operations atomic.Int32
}

func newTerminalFixture(t *testing.T, mode string, kind RecoveryOperationKind) *terminalFixture {
	t.Helper()
	f := &terminalFixture{}
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.writes.Add(1)
			http.Error(w, "write forbidden", 500)
			return
		}
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong request project")
		}
		switch r.URL.Path {
		case "/1.0":
			body := physicalRoot(server)
			if f.capture.Load() {
				switch mode {
				case "server-name-drift":
					body["environment"].(map[string]any)["server_name"] = "replacement"
				case "unqualified-version":
					body["environment"].(map[string]any)["server_version"] = "6.0.5"
				}
			}
			writeSync(w, body)
		case "/1.0/instances/runner-1":
			f.instances.Add(1)
			if f.capture.Load() && mode == "delete-success" {
				http.Error(w, "not-found-not-absence-proof", 404)
				return
			}
			body := instanceEvidenceBody()
			config := body["expanded_config"].(map[string]any)
			if f.capture.Load() {
				switch mode {
				case "missing-uuid":
					delete(config, "volatile.uuid")
				case "bad-uuid":
					config["volatile.uuid"] = "not-a-uuid"
				case "null-generation":
					config["volatile.uuid.generation"] = nil
				case "missing-generation":
					delete(config, "volatile.uuid.generation")
				case "missing-created":
					delete(body, "created_at")
				case "null-created":
					body["created_at"] = nil
				case "wrong-controller":
					config["user.runner-controller-id"] = "other"
				case "wrong-pool":
					config["user.runner-pool-id"] = "other"
				case "wrong-key":
					config["user.garm-capacity.reservation"] = strings.Repeat("c", 64)
				case "bounds-drift":
					config["limits.memory"] = "2GiB"
				case "generation-drift":
					if f.opSeen.Load() {
						config["volatile.uuid.generation"] = evidenceOtherUUID
					}
				case "uuid-drift":
					if f.opSeen.Load() {
						config["volatile.uuid"] = evidenceOtherUUID
					}
				case "created-drift":
					if f.opSeen.Load() {
						body["created_at"] = "2026-01-02T00:00:00Z"
					}
				}
			}
			writeSync(w, body)
		case "/1.0/operations/" + operation:
			f.operations.Add(1)
			f.opSeen.Store(true)
			if mode == "slow-operation" {
				<-r.Context().Done()
				return
			}
			if mode == "expired-operation" {
				http.Error(w, "secret-operation-not-found", 404)
				return
			}
			body := operationEvidenceBody(kind)
			switch mode {
			case "wrong-operation":
				body["id"] = evidenceOtherUUID
			case "wrong-class":
				body["class"] = "token"
			case "wrong-kind":
				body["description"] = "Executing command"
			case "pending":
				body["status_code"] = 103
				body["status"] = "Running"
			case "failed":
				body["status_code"] = 400
				body["status"] = "Failure"
			case "cancelled":
				body["status_code"] = 401
				body["status"] = "Cancelled"
			case "reported-error":
				body["err"] = "secret-error-must-not-retain"
			case "missing-error":
				delete(body, "err")
			case "null-error":
				body["err"] = nil
			case "contradictory-status":
				body["status"] = "Failure"
			case "missing-project":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1"}}
			case "wrong-project":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1?project=other"}}
			case "duplicate-project":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners&project=runners"}}
			case "escaped-resource":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/%72unner-1?project=runners"}}
			case "extra-resource":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners"}, "images": {}}
			case "multiple-resources":
				body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1?project=runners", "/1.0/instances/runner-2?project=runners"}}
			case "null-time":
				body["updated_at"] = nil
			case "reverse-time":
				body["updated_at"] = "2025-01-01T00:00:00Z"
			}
			writeSync(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	c, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	f.client = c
	t.Cleanup(c.Close)
	return f
}

func TestRealTerminalCreateObservationIsOpaqueRedactedAndReadOnly(t *testing.T) {
	f := newTerminalFixture(t, "success", RecoveryCreate)
	identity, err := f.client.ObserveInstanceIdentity(context.Background(), expectedIdentity())
	if err != nil || !identity.Valid() {
		t.Fatalf("identity: %v", err)
	}
	f.capture.Store(true)
	observation, err := f.client.CaptureTerminalOperation(context.Background(), identity, operation, RecoveryCreate)
	if err != nil || !observation.Valid() {
		t.Fatalf("capture: %v", err)
	}
	record := observation.Record()
	if record.OperationID != operation || record.Kind != RecoveryCreate || record.StatusCode != 200 || !record.ResourceReadbackMatched || !record.RequiresExclusiveFence || record.Identity.Subject.UUID != evidenceUUID || record.Identity.Subject.Generation != evidenceGeneration || record.CapturedAt.IsZero() {
		t.Fatalf("incomplete record: %+v", record)
	}
	data, _ := json.Marshal(record)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "CERTIFICATE") {
		t.Fatal("unwhitelisted metadata retained")
	}
	opaque, _ := json.Marshal(observation)
	if string(opaque) != "{}" {
		t.Fatal("opaque observation auto-serialized")
	}
	var forged TerminalOperationObservation
	if err := json.Unmarshal(data, &forged); err != nil || forged.Valid() {
		t.Fatal("pasted record manufactured verified observation")
	}
	if f.writes.Load() != 0 || f.instances.Load() != 2 || f.operations.Load() != 1 {
		t.Fatal("capture wrote or failed incarnation re-read")
	}
}

func TestDeleteObservationExplicitlyDoesNotProveIncarnationOrAbsence(t *testing.T) {
	f := newTerminalFixture(t, "delete-success", RecoveryDelete)
	identity, err := f.client.ObserveInstanceIdentity(context.Background(), expectedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	f.capture.Store(true)
	observation, err := f.client.CaptureTerminalOperation(context.Background(), identity, operation, RecoveryDelete)
	if err != nil || !observation.Valid() {
		t.Fatal(err)
	}
	record := observation.Record()
	if record.ResourceReadbackMatched || !record.RequiresExclusiveFence || record.Kind != RecoveryDelete {
		t.Fatal("name-only delete claimed UUID-bound authority")
	}
	if f.instances.Load() != 1 || f.writes.Load() != 0 {
		t.Fatal("delete capture inferred absence from GET404 or performed a write")
	}
}

func TestIdentityObservationRequiresExactTaggedIncarnationAndBounds(t *testing.T) {
	for _, mode := range []string{"missing-uuid", "bad-uuid", "null-generation", "missing-generation", "missing-created", "null-created", "wrong-controller", "wrong-pool", "wrong-key", "bounds-drift", "unqualified-version"} {
		t.Run(mode, func(t *testing.T) {
			f := newTerminalFixture(t, mode, RecoveryCreate)
			f.capture.Store(true)
			identity, err := f.client.ObserveInstanceIdentity(context.Background(), expectedIdentity())
			if err == nil || identity.Valid() {
				t.Fatal("partial/different incarnation accepted")
			}
			if f.writes.Load() != 0 {
				t.Fatal("identity observation wrote")
			}
		})
	}
}

func TestTerminalCaptureFailsClosedForExpiredMalformedFailedOrChangedEvidence(t *testing.T) {
	for _, mode := range []string{"expired-operation", "wrong-operation", "wrong-class", "wrong-kind", "pending", "failed", "cancelled", "reported-error", "missing-error", "null-error", "contradictory-status", "missing-project", "wrong-project", "duplicate-project", "escaped-resource", "extra-resource", "multiple-resources", "null-time", "reverse-time", "generation-drift", "uuid-drift", "created-drift", "server-name-drift", "unqualified-version"} {
		t.Run(mode, func(t *testing.T) {
			f := newTerminalFixture(t, mode, RecoveryCreate)
			identity, err := f.client.ObserveInstanceIdentity(context.Background(), expectedIdentity())
			if err != nil {
				t.Fatal(err)
			}
			f.capture.Store(true)
			observation, err := f.client.CaptureTerminalOperation(context.Background(), identity, operation, RecoveryCreate)
			if err == nil || observation.Valid() {
				t.Fatal("unqualified terminal proof accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("private error disclosed")
			}
			if mode == "expired-operation" && !errors.Is(err, ErrTransport) {
				t.Fatal("expired record became absence/success")
			}
			if f.writes.Load() != 0 {
				t.Fatal("failure recovery wrote or retried create")
			}
		})
	}
}

func TestInvalidCaptureDoesNotReachNetwork(t *testing.T) {
	f := newTerminalFixture(t, "success", RecoveryCreate)
	if _, err := f.client.CaptureTerminalOperation(context.Background(), VerifiedInstanceIdentity{}, operation, RecoveryCreate); !errors.Is(err, ErrConfiguration) {
		t.Fatal("zero identity accepted")
	}
	if f.instances.Load() != 0 || f.operations.Load() != 0 {
		t.Fatal("invalid capture reached resources")
	}
	zero := VerifiedInstanceIdentity{}.Observation()
	if !zero.ObservedAt.Equal(time.Time{}) {
		t.Fatal("zero identity exposed proof")
	}
}

func TestEvidenceResourceUsesPinnedCanonicalProjectRepresentation(t *testing.T) {
	c := &Client{project: "default"}
	if err := c.exactEvidenceResource("/1.0/instances/runner-1", "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.exactEvidenceResource("/1.0/instances/runner-1?project=default", "runner-1"); err == nil {
		t.Fatal("noncanonical default project accepted")
	}
	c.project = "runners"
	if err := c.exactEvidenceResource("/1.0/instances/runner-1?project=runners", "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.exactEvidenceResource("https://other.invalid/1.0/instances/runner-1?project=runners", "runner-1"); err == nil {
		t.Fatal("absolute resource authority accepted")
	}
}

func TestTerminalCaptureHonorsEarlierDeadlineWithoutWrites(t *testing.T) {
	f := newTerminalFixture(t, "slow-operation", RecoveryCreate)
	identity, err := f.client.ObserveInstanceIdentity(context.Background(), expectedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	observation, err := f.client.CaptureTerminalOperation(ctx, identity, operation, RecoveryCreate)
	if !errors.Is(err, context.DeadlineExceeded) || observation.Valid() || time.Since(start) > 2*time.Second || f.writes.Load() != 0 {
		t.Fatalf("unbounded or unsafe capture: %v", err)
	}
}
