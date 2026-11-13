package incusapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ProjectAbsenceRecord reports two matching project-scoped READS, not atomic
// physical absence. Authorization filtering, other projects/daemons, old queued
// writers, job assignment and deletion history remain unqualified. No boolean in
// this record is an operator assertion or a retirement/finalization permission.
type ProjectAbsenceRecord struct {
	Identity                      InstanceIdentityObservation `json:"identity"`
	StartedAt                     time.Time                   `json:"started_at"`
	CompletedAt                   time.Time                   `json:"completed_at"`
	InstanceInventoryFingerprint  string                      `json:"instance_inventory_fingerprint"`
	OperationInventoryFingerprint string                      `json:"operation_inventory_fingerprint"`
	ObservedInstances             int                         `json:"observed_instances"`
	ObservedTerminalOperations    int                         `json:"observed_terminal_operations"`
	MatchingPasses                int                         `json:"matching_passes"`
	SnapshotAtomic                bool                        `json:"snapshot_atomic"`
	ProjectOnly                   bool                        `json:"project_only"`
}

// ProjectAbsenceObservation is minted only from actual authenticated GETs. Valid
// means structured matching observations, NEVER job safety/absence lease/release.
// Its outstanding gates cannot be cleared by JSON, counts, timeouts or a caller.
type ProjectAbsenceObservation struct {
	data     ProjectAbsenceRecord
	verified bool
}

func (o ProjectAbsenceObservation) Valid() bool {
	return o.verified && o.data.MatchingPasses == 2 && !o.data.SnapshotAtomic && o.data.ProjectOnly
}
func (o ProjectAbsenceObservation) Record() ProjectAbsenceRecord {
	if !o.Valid() {
		return ProjectAbsenceRecord{}
	}
	return o.data
}
func (o ProjectAbsenceObservation) Blockers() []string {
	return []string{"external_writer_fence_unqualified", "job_assignment_and_post_job_fence_unqualified", "authorization_visibility_and_whole_host_scope_unqualified", "terminal_delete_history_and_post_delete_barrier_unqualified", "retirement_finalizer_unimplemented"}
}

type absenceInstance struct {
	Name, UUID, Generation, Controller, Pool, Key string
	CreatedAt                                     time.Time
	CPU, RAM                                      int64
	Status                                        int
}
type absenceOperation struct {
	ID        string
	Status    int
	Instances []string
}

func observationFingerprint(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", ErrResponse
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func rejectFolded(fields map[string]json.RawMessage, critical ...string) error {
	for key := range fields {
		for _, want := range critical {
			if key != want && strings.EqualFold(key, want) {
				return ErrResponse
			}
		}
	}
	return nil
}
func (c *Client) absenceRoot(ctx context.Context, prior VerifiedInstanceIdentity) error {
	actual, err := c.PhysicalIdentity(ctx)
	if err != nil {
		return err
	}
	if actual.ServerVersion() != "6.0.4" {
		return ErrUnsupported
	}
	if actual != prior.physical {
		return ErrResponse
	}
	return nil
}
func (c *Client) absenceInstances(ctx context.Context, prior VerifiedInstanceIdentity) (string, int, error) {
	if err := c.absenceRoot(ctx, prior); err != nil {
		return "", 0, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return "", 0, err
	}
	if response.kind != "sync" || response.serverKeyFingerprint != prior.physical.ServerKeyFingerprint() {
		return "", 0, ErrResponse
	}
	var rows []json.RawMessage
	body := bytes.TrimSpace(response.metadata)
	if len(body) == 0 || body[0] != '[' || json.Unmarshal(body, &rows) != nil {
		return "", 0, ErrResponse
	}
	names, uuids, generations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	result := make([]absenceInstance, 0, len(rows))
	subject := prior.data.Subject
	for _, raw := range rows {
		fields, err := object(raw)
		if err != nil {
			return "", 0, err
		}
		if rejectFolded(fields, "name", "status_code", "created_at", "expanded_config") != nil {
			return "", 0, ErrResponse
		}
		value, err := parseInstance(raw)
		if err != nil {
			return "", 0, err
		}
		if value.Status < 100 || value.Status > 113 {
			return "", 0, ErrResponse
		}
		created, err := identityTime(fields, "created_at")
		if err != nil {
			return "", 0, err
		}
		config, err := object(fields["expanded_config"])
		if err != nil {
			return "", 0, err
		}
		if rejectFolded(config, "volatile.uuid", "volatile.uuid.generation", "user.runner-controller-id", "user.runner-pool-id", "user.garm-capacity.reservation", "limits.cpu", "limits.memory") != nil {
			return "", 0, ErrResponse
		}
		uuid, err := field[string](config, "volatile.uuid")
		if err != nil || !operationID(uuid) {
			return "", 0, ErrResponse
		}
		generation, err := field[string](config, "volatile.uuid.generation")
		if err != nil || !operationID(generation) {
			return "", 0, ErrResponse
		}
		if names[value.Name] || uuids[uuid] || generations[generation] {
			return "", 0, ErrResponse
		}
		names[value.Name] = true
		uuids[uuid] = true
		generations[generation] = true
		if value.Name == subject.Name || uuid == subject.UUID || generation == subject.Generation || (value.ReservationKey != "" && value.ReservationKey == subject.ReservationKey) {
			return "", 0, ErrResponse
		}
		if (value.ControllerID != "" && !label(value.ControllerID)) || (value.PoolID != "" && !label(value.PoolID)) || (value.ReservationKey != "" && !fingerprint(value.ReservationKey)) {
			return "", 0, ErrResponse
		}
		result = append(result, absenceInstance{value.Name, uuid, generation, value.ControllerID, value.PoolID, value.ReservationKey, created, value.CPUMillis, value.RAMMiB, value.Status})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	digest, err := observationFingerprint(result)
	return digest, len(result), err
}
func (c *Client) absenceOperations(ctx context.Context, prior VerifiedInstanceIdentity) (string, int, error) {
	if err := c.absenceRoot(ctx, prior); err != nil {
		return "", 0, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/operations", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return "", 0, err
	}
	if response.kind != "sync" || response.serverKeyFingerprint != prior.physical.ServerKeyFingerprint() {
		return "", 0, ErrResponse
	}
	groups, err := object(response.metadata)
	if err != nil {
		return "", 0, err
	}
	names := map[string]int{"operation created": 100, "started": 101, "stopped": 102, "running": 103, "cancelling": 104, "pending": 105, "starting": 106, "stopping": 107, "aborting": 108, "freezing": 109, "frozen": 110, "thawed": 111, "error": 112, "ready": 113, "success": 200, "failure": 400, "cancelled": 401}
	seen := map[string]bool{}
	result := []absenceOperation{}
	for group, raw := range groups {
		expected, known := names[group]
		if !known {
			return "", 0, ErrUnsupported
		}
		var rows []json.RawMessage
		body := bytes.TrimSpace(raw)
		if len(body) == 0 || body[0] != '[' || json.Unmarshal(body, &rows) != nil {
			return "", 0, ErrResponse
		}
		if len(rows) > 0 && expected != 200 && expected != 400 && expected != 401 {
			return "", 0, ErrOperation
		}
		for _, raw := range rows {
			fields, err := object(raw)
			if err != nil {
				return "", 0, err
			}
			if rejectFolded(fields, "id", "status_code", "resources", "class", "err") != nil {
				return "", 0, ErrResponse
			}
			id, err := field[string](fields, "id")
			if err != nil || !operationID(id) || seen[id] {
				return "", 0, ErrResponse
			}
			seen[id] = true
			status, err := field[int](fields, "status_code")
			if err != nil || status != expected {
				return "", 0, ErrResponse
			}
			class, err := field[string](fields, "class")
			if err != nil || class != "task" {
				return "", 0, ErrUnsupported
			}
			operationError, err := field[string](fields, "err")
			if err != nil || (status == 200 && operationError != "") {
				return "", 0, ErrResponse
			}
			resources, err := operationResources(fields)
			if err != nil {
				return "", 0, err
			}
			for key := range resources {
				if key != "instances" {
					return "", 0, ErrUnsupported
				}
			}
			instances := []string{}
			resourceSeen := map[string]bool{}
			for _, resource := range resources["instances"] {
				instance, err := c.resourcePath(resource)
				if err != nil || c.exactEvidenceResource(resource, instance) != nil || resourceSeen[instance] {
					return "", 0, ErrResponse
				}
				resourceSeen[instance] = true
				instances = append(instances, instance)
			}
			sort.Strings(instances)
			result = append(result, absenceOperation{id, status, instances})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	digest, err := observationFingerprint(result)
	return digest, len(result), err
}

// ObserveProjectAbsence performs no writes or single-name 404 inference. Two
// matching recursive collections detect visible rename/reuse and some races;
// they CANNOT fence a writer, attest authorization completeness, cover other
// projects/daemons, stop job assignment or establish an atomic absence interval.
// Blockers remain present EVEN on empty stable responses. No ledger barrier,
// DELETE receipt, deletion action or release authority is accepted/returned.
func (c *Client) ObserveProjectAbsence(ctx context.Context, prior VerifiedInstanceIdentity) (ProjectAbsenceObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !prior.Valid() || prior.data.Subject.Project != c.project {
		return ProjectAbsenceObservation{}, ErrConfiguration
	}
	if prior.physical.ServerVersion() != "6.0.4" {
		return ProjectAbsenceObservation{}, ErrUnsupported
	}
	started := time.Now().UTC()
	var firstInstances, firstOperations string
	var countInstances, countOperations int
	for pass := 0; pass < 2; pass++ {
		instances, n, err := c.absenceInstances(ctx, prior)
		if err != nil {
			return ProjectAbsenceObservation{}, err
		}
		operations, m, err := c.absenceOperations(ctx, prior)
		if err != nil {
			return ProjectAbsenceObservation{}, err
		}
		if pass == 0 {
			firstInstances, firstOperations, countInstances, countOperations = instances, operations, n, m
		} else if instances != firstInstances || operations != firstOperations || n != countInstances || m != countOperations {
			return ProjectAbsenceObservation{}, ErrResponse
		}
	}
	if err := c.absenceRoot(ctx, prior); err != nil {
		return ProjectAbsenceObservation{}, err
	}
	completed := time.Now().UTC()
	if completed.Before(started) {
		return ProjectAbsenceObservation{}, ErrResponse
	}
	return ProjectAbsenceObservation{verified: true, data: ProjectAbsenceRecord{Identity: prior.data, StartedAt: started, CompletedAt: completed, InstanceInventoryFingerprint: firstInstances, OperationInventoryFingerprint: firstOperations, ObservedInstances: countInstances, ObservedTerminalOperations: countOperations, MatchingPasses: 2, SnapshotAtomic: false, ProjectOnly: true}}, nil
}
