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

type absenceFixture struct {
	client                               *Client
	phase                                atomic.Bool
	visible                              atomic.Bool
	instances, operations, named, writes atomic.Int32
	oldWrites                            atomic.Int32
	accepted, release                    chan struct{}
}

func otherAbsenceInstance() map[string]any {
	body := instanceEvidenceBody()
	body["name"] = "other-guest"
	config := body["expanded_config"].(map[string]any)
	config["volatile.uuid"] = evidenceOtherUUID
	config["volatile.uuid.generation"] = "44444444-4444-4444-8444-444444444444"
	config["user.garm-capacity.reservation"] = strings.Repeat("c", 64)
	return body
}
func newAbsenceFixture(t *testing.T, mode string) (*absenceFixture, VerifiedInstanceIdentity) {
	t.Helper()
	f := &absenceFixture{accepted: make(chan struct{}), release: make(chan struct{})}
	f.visible.Store(true)
	var server *httptest.Server
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "queued-writer" && r.Method == http.MethodPost && r.URL.Path == "/1.0/instances" {
			f.oldWrites.Add(1)
			close(f.accepted)
			select {
			case <-f.release:
			case <-r.Context().Done():
				return
			}
			f.visible.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "async", "status_code": 100, "operation": "/1.0/operations/" + operation + "?project=runners", "metadata": map[string]any{"id": operation}})
			return
		}
		if r.Method != http.MethodGet {
			f.writes.Add(1)
			http.Error(w, "write forbidden", 500)
			return
		}
		if r.URL.Query().Get("project") != "runners" {
			t.Error("wrong project")
		}
		switch r.URL.Path {
		case "/1.0":
			root := physicalRoot(server)
			if f.phase.Load() {
				if mode == "clustered" {
					root["environment"].(map[string]any)["server_clustered"] = true
				}
				if mode == "name-drift" && f.instances.Load() >= 2 {
					root["environment"].(map[string]any)["server_name"] = "replacement"
				}
			}
			writeSync(w, root)
		case "/1.0/instances/runner-1":
			f.named.Add(1)
			if !f.visible.Load() {
				http.NotFound(w, r)
				return
			}
			writeSync(w, instanceEvidenceBody())
		case "/1.0/instances":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("missing recursive instances")
			}
			pass := f.instances.Add(1)
			if mode == "slow" {
				<-r.Context().Done()
				return
			}
			if mode == "null-instances" {
				writeSync(w, nil)
				return
			}
			rows := []any{}
			if f.visible.Load() && mode != "filtered" {
				rows = append(rows, instanceEvidenceBody())
			}
			if strings.HasPrefix(mode, "other-") || mode == "changed-inventory" {
				body := otherAbsenceInstance()
				config := body["expanded_config"].(map[string]any)
				switch mode {
				case "other-renamed-uuid":
					config["volatile.uuid"] = evidenceUUID
				case "other-generation":
					config["volatile.uuid.generation"] = evidenceGeneration
				case "other-key":
					config["user.garm-capacity.reservation"] = strings.Repeat("b", 64)
				case "other-reused-name":
					body["name"] = "runner-1"
				case "other-missing-uuid":
					delete(config, "volatile.uuid")
				case "other-null-generation":
					config["volatile.uuid.generation"] = nil
				case "other-casefold":
					config["VOLATILE.UUID"] = evidenceUUID
				case "other-unbounded":
					config["limits.memory"] = "50%"
				case "changed-inventory":
					if pass == 2 {
						config["limits.cpu"] = "2"
					}
				}
				rows = append(rows, body)
				if mode == "other-order" {
					second := otherAbsenceInstance()
					second["name"] = "second-guest"
					cfg := second["expanded_config"].(map[string]any)
					cfg["volatile.uuid"] = "55555555-5555-4555-8555-555555555555"
					cfg["volatile.uuid.generation"] = "66666666-6666-4666-8666-666666666666"
					rows = append(rows, second)
					if pass == 2 {
						rows[0], rows[1] = rows[1], rows[0]
					}
				}
				if mode == "other-duplicate" {
					duplicate := otherAbsenceInstance()
					duplicate["name"] = "duplicate"
					rows = append(rows, duplicate)
				}
			}
			writeSync(w, rows)
		case "/1.0/operations":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("missing recursive operations")
			}
			pass := f.operations.Add(1)
			groups := map[string]any{}
			if strings.HasPrefix(mode, "op-") || mode == "terminal-delete" || mode == "terminal-failure" {
				body := operationEvidenceBody(RecoveryDelete)
				group := "success"
				switch mode {
				case "op-pending":
					group = "running"
					body["status_code"] = 103
					body["status"] = "Running"
				case "op-unknown":
					group = "future"
				case "op-casefold":
					body["STATUS_CODE"] = 103
				case "op-wrong-project":
					body["resources"] = map[string][]string{"instances": {"/1.0/instances/runner-1?project=other"}}
				case "op-extra-resource":
					body["resources"] = map[string][]string{"images": {}}
				case "op-null":
					body["resources"] = nil
				case "terminal-failure":
					group = "failure"
					body["status_code"] = 400
					body["status"] = "Failure"
					body["err"] = "private-must-not-be-retained"
				}
				groups[group] = []any{body}
				if mode == "op-changing" && pass == 2 {
					groups = map[string]any{}
				}
			}
			if mode == "late-between-passes" && pass == 1 {
				f.visible.Store(true)
			}
			// Deliberately demonstrate that repeated reads are NOT a writer fence.
			if mode == "late-after-last-instance-read" && pass == 2 {
				f.visible.Store(true)
			}
			if mode == "queued-writer" && pass == 2 {
				close(f.release)
			}
			writeSync(w, groups)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	c, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	f.client = c
	t.Cleanup(c.Close)
	prior, err := c.ObserveInstanceIdentity(context.Background(), expectedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	f.phase.Store(true)
	f.visible.Store(mode == "still-present" || mode == "filtered")
	return f, prior
}

func TestProjectAbsenceReadsAreOpaqueRedactedAndNeverRetirementAuthority(t *testing.T) {
	for _, mode := range []string{"empty", "other-unrelated", "other-order", "terminal-delete", "terminal-failure"} {
		t.Run(mode, func(t *testing.T) {
			f, prior := newAbsenceFixture(t, mode)
			observed, err := f.client.ObserveProjectAbsence(context.Background(), prior)
			if err != nil || !observed.Valid() {
				t.Fatalf("observation unavailable %+v %v", observed, err)
			}
			record := observed.Record()
			if record.SnapshotAtomic || !record.ProjectOnly || record.MatchingPasses != 2 || record.Identity != prior.Observation() || len(observed.Blockers()) != 5 || f.instances.Load() != 2 || f.operations.Load() != 2 || f.named.Load() != 1 || f.writes.Load() != 0 {
				t.Fatal("reads became a fence, 404 proof or write", record)
			}
			if !fingerprint(record.InstanceInventoryFingerprint) || !fingerprint(record.OperationInventoryFingerprint) || record.CompletedAt.Before(record.StartedAt) {
				t.Fatal("invalid bounded record")
			}
			if mode == "other-unrelated" && record.ObservedInstances != 1 {
				t.Fatal("inventory omitted unrelated bounded resource")
			}
			encoded, _ := json.Marshal(observed)
			if string(encoded) != "{}" {
				t.Fatal("opaque observation exposed constructor")
			}
			var forged ProjectAbsenceObservation
			raw, _ := json.Marshal(record)
			if err := json.Unmarshal(raw, &forged); err != nil || forged.Valid() || len(forged.Blockers()) != 5 {
				t.Fatal("JSON manufactured retirement authority")
			}
			if strings.Contains(string(raw), "secret-bootstrap") || strings.Contains(string(raw), "private-must-not") || strings.Contains(string(raw), "other-guest") {
				t.Fatal("raw unrelated metadata retained")
			}
			gates := observed.Blockers()
			gates[0] = "cleared"
			if observed.Blockers()[0] == "cleared" {
				t.Fatal("caller cleared an external fence")
			}
		})
	}
}

func TestProjectAbsenceRejectsPresenceAliasesIncompleteReadsAndRaces(t *testing.T) {
	for _, mode := range []string{"still-present", "other-renamed-uuid", "other-generation", "other-key", "other-reused-name", "other-missing-uuid", "other-null-generation", "other-casefold", "other-unbounded", "other-duplicate", "changed-inventory", "null-instances", "op-pending", "op-unknown", "op-casefold", "op-wrong-project", "op-extra-resource", "op-null", "op-changing", "late-between-passes", "clustered", "name-drift"} {
		t.Run(mode, func(t *testing.T) {
			f, prior := newAbsenceFixture(t, mode)
			value, err := f.client.ObserveProjectAbsence(context.Background(), prior)
			if err == nil || value.Valid() || value.Record() != (ProjectAbsenceRecord{}) || f.writes.Load() != 0 {
				t.Fatal("ambiguous/active/racing state became absence authority", mode, value, err)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("raw provider error exposed")
			}
		})
	}
}

func TestProjectReadSuccessDoesNotFenceQueuedOldWriter(t *testing.T) {
	f, prior := newAbsenceFixture(t, "late-after-last-instance-read")
	value, err := f.client.ObserveProjectAbsence(context.Background(), prior)
	if err != nil || !value.Valid() {
		t.Fatal(err)
	}
	// The source now contains the target, despite two agreeing empty instance
	// collections. Therefore Valid is only read provenance, not atomic absence.
	current, err := f.client.Instance(context.Background(), "runner-1")
	if err != nil || current.Name != "runner-1" || !f.visible.Load() {
		t.Fatal("fixture did not reproduce old-writer race", err)
	}
	if value.Record().SnapshotAtomic || !value.Record().ProjectOnly || len(value.Blockers()) != 5 || f.writes.Load() != 0 {
		t.Fatal("snapshot incorrectly fenced a queued writer")
	}
}

func TestProjectAbsenceZeroIdentityAndDeadlineFailClosed(t *testing.T) {
	f, prior := newAbsenceFixture(t, "slow")
	if value, err := f.client.ObserveProjectAbsence(context.Background(), VerifiedInstanceIdentity{}); !errors.Is(err, ErrConfiguration) || value.Valid() || f.instances.Load() != 0 {
		t.Fatal("zero identity performed reads/qualified absence", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	began := time.Now()
	value, err := f.client.ObserveProjectAbsence(ctx, prior)
	if err == nil || value.Valid() || time.Since(began) > time.Second || f.writes.Load() != 0 {
		t.Fatal("earlier cancellation ignored", err)
	}
}

// This is a real mutually authenticated queued HTTP request, not a release
// verifier or a configured claim that an external writer was fenced.
func TestProjectReadsDoNotFenceAlreadyAcceptedHTTPWrite(t *testing.T) {
	f, prior := newAbsenceFixture(t, "queued-writer")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.client.base.String()+"/1.0/instances?project=runners", strings.NewReader(`{"name":"runner-1"}`))
		if err != nil {
			finished <- err
			return
		}
		response, err := f.client.http.Do(request)
		if response != nil {
			response.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-f.accepted:
	case <-ctx.Done():
		t.Fatal("old write not accepted")
	}
	observed, err := f.client.ObserveProjectAbsence(ctx, prior)
	if err != nil || !observed.Valid() {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	current, err := f.client.Instance(ctx, "runner-1")
	if err != nil || current.Name != "runner-1" || f.oldWrites.Load() != 1 || f.writes.Load() != 0 {
		t.Fatal("queued writer fixture did not reproduce late completion", err)
	}
	if observed.Record().SnapshotAtomic || len(observed.Blockers()) != 5 {
		t.Fatal("accepted remote work was inferred fenced")
	}
}

func TestProjectReadSuccessCannotAttestAuthorizationVisibility(t *testing.T) {
	f, prior := newAbsenceFixture(t, "filtered")
	observed, err := f.client.ObserveProjectAbsence(context.Background(), prior)
	if err != nil || !observed.Valid() {
		t.Fatal(err)
	}
	current, err := f.client.Instance(context.Background(), "runner-1")
	if err != nil || current.Name != "runner-1" || observed.Record().ObservedInstances != 0 {
		t.Fatal("fixture did not model a permission-filtered collection", err)
	}
	found := false
	for _, gate := range observed.Blockers() {
		if gate == "authorization_visibility_and_whole_host_scope_unqualified" {
			found = true
		}
	}
	if !found || observed.Record().SnapshotAtomic || f.writes.Load() != 0 {
		t.Fatal("HTTP200/trusted TLS falsely certified complete visibility")
	}
}
