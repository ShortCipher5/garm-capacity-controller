package incusapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// IdentityExpectation comes only from trusted controller placement policy, not
// job/bootstrap payloads. Observing it authorizes no lifecycle or budget change.
type IdentityExpectation struct {
	Name           string
	ControllerID   string
	PoolID         string
	ReservationKey string
	CPUMillis      int64
	RAMMiB         int64
}

// InstanceIdentitySubject is a bounded non-secret incarnation binding. Its
// mutable tags/name alone are not identity proof; UUID, generation, creation,
// actual backend key/scope and bounds must all stay consistent under fencing.
type InstanceIdentitySubject struct {
	BackendKeyFingerprint string    `json:"backend_key_fingerprint"`
	OriginFingerprint     string    `json:"origin_fingerprint"`
	ScopeFingerprint      string    `json:"scope_fingerprint"`
	ServerName            string    `json:"server_name"`
	ServerVersion         string    `json:"server_version"`
	Project               string    `json:"project"`
	Name                  string    `json:"name"`
	UUID                  string    `json:"uuid"`
	Generation            string    `json:"generation"`
	CreatedAt             time.Time `json:"created_at"`
	ControllerID          string    `json:"controller_id"`
	PoolID                string    `json:"pool_id"`
	ReservationKey        string    `json:"reservation_key"`
	CPUMillis             int64     `json:"cpu_millis"`
	RAMMiB                int64     `json:"ram_mib"`
}

type InstanceIdentityObservation struct {
	Subject            InstanceIdentitySubject `json:"subject"`
	SubjectFingerprint string                  `json:"subject_fingerprint"`
	ObservedAt         time.Time               `json:"observed_at"`
	InstanceStatus     int                     `json:"instance_status"`
}

// VerifiedInstanceIdentity can only be minted by authenticated transport reads.
// Operator JSON and zero values cannot manufacture one. It is an observation,
// NOT a lease, job-idle proof, hardware attestation or delete/release authority.
type VerifiedInstanceIdentity struct {
	physical PhysicalServerIdentity
	data     InstanceIdentityObservation
	verified bool
}

func (i VerifiedInstanceIdentity) Valid() bool {
	return i.verified && i.physical.Valid() && fingerprint(i.data.SubjectFingerprint) && !i.data.ObservedAt.IsZero()
}
func (i VerifiedInstanceIdentity) Observation() InstanceIdentityObservation {
	if !i.Valid() {
		return InstanceIdentityObservation{}
	}
	return i.data
}

func validIdentityExpectation(e IdentityExpectation) bool {
	return name(e.Name) && label(e.ControllerID) && label(e.PoolID) && fingerprint(e.ReservationKey) && e.CPUMillis > 0 && e.RAMMiB > 0
}
func identityTime(fields map[string]json.RawMessage, key string) (time.Time, error) {
	value, err := field[time.Time](fields, key)
	if err != nil || value.IsZero() {
		return time.Time{}, ErrResponse
	}
	return value.UTC(), nil
}

// ObserveInstanceIdentity makes only verified root and named-instance GETs. The
// evidence contract is pinned to reported server_version exactly 6.0.4. Older,
// newer or distribution-suffixed versions need explicit qualification first.
func (c *Client) ObserveInstanceIdentity(ctx context.Context, expected IdentityExpectation) (VerifiedInstanceIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !validIdentityExpectation(expected) {
		return VerifiedInstanceIdentity{}, ErrConfiguration
	}
	physical, err := c.PhysicalIdentity(ctx)
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	if physical.ServerVersion() != "6.0.4" {
		return VerifiedInstanceIdentity{}, ErrUnsupported
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+expected.Name, nil, nil)
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	if response.kind != "sync" || response.serverKeyFingerprint != physical.ServerKeyFingerprint() {
		return VerifiedInstanceIdentity{}, ErrResponse
	}
	instance, err := parseInstance(response.metadata)
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	if instance.Name != expected.Name || instance.ControllerID != expected.ControllerID || instance.PoolID != expected.PoolID || instance.ReservationKey != expected.ReservationKey || instance.CPUMillis != expected.CPUMillis || instance.RAMMiB != expected.RAMMiB {
		return VerifiedInstanceIdentity{}, ErrResponse
	}
	fields, err := object(response.metadata)
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	createdAt, err := identityTime(fields, "created_at")
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	config, err := object(fields["expanded_config"])
	if err != nil {
		return VerifiedInstanceIdentity{}, err
	}
	uuid, err := field[string](config, "volatile.uuid")
	if err != nil || !operationID(uuid) {
		return VerifiedInstanceIdentity{}, ErrResponse
	}
	generation, err := field[string](config, "volatile.uuid.generation")
	if err != nil || !operationID(generation) {
		return VerifiedInstanceIdentity{}, ErrResponse
	}
	subject := InstanceIdentitySubject{BackendKeyFingerprint: physical.ServerKeyFingerprint(), OriginFingerprint: physical.OriginFingerprint(), ScopeFingerprint: physical.ScopeFingerprint(), ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(), Project: c.project, Name: expected.Name, UUID: uuid, Generation: generation, CreatedAt: createdAt, ControllerID: expected.ControllerID, PoolID: expected.PoolID, ReservationKey: expected.ReservationKey, CPUMillis: expected.CPUMillis, RAMMiB: expected.RAMMiB}
	encoded, err := json.Marshal(subject)
	if err != nil {
		return VerifiedInstanceIdentity{}, ErrResponse
	}
	sum := sha256.Sum256(encoded)
	return VerifiedInstanceIdentity{physical: physical, data: InstanceIdentityObservation{Subject: subject, SubjectFingerprint: fmt.Sprintf("%x", sum), ObservedAt: time.Now().UTC(), InstanceStatus: instance.Status}, verified: true}, nil
}

type RecoveryOperationKind string

const (
	RecoveryCreate RecoveryOperationKind = "create"
	RecoveryDelete RecoveryOperationKind = "delete"
)

// TerminalOperationRecord is a whitelisted authenticated observation. In
// particular, operation resources carry NAME, not instance UUID, for both kinds.
// A create readback proves capture-window consistency, not exclusive history.
// Prior identity plus terminal delete is NOT immutable-incarnation deletion
// proof without an exclusive no-rename/restore/recreate fence and fresh complete
// post-barrier absence. No field here grants permission to release capacity.
// Server times are reported values, not a qualified clock or lifecycle order.
// Incus 6.0.4 does not advance UpdatedAt on terminal status: it is NOT completion
// time and may precede the instance's CreatedAt. Valid only means authenticated
// structured observation; a future finalizer must establish its own chronology.
type TerminalOperationRecord struct {
	Identity                InstanceIdentityObservation `json:"identity"`
	OperationID             string                      `json:"operation_id"`
	Kind                    RecoveryOperationKind       `json:"kind"`
	Class                   string                      `json:"class"`
	Description             string                      `json:"description"`
	StatusCode              int                         `json:"status_code"`
	CreatedAt               time.Time                   `json:"created_at"`
	UpdatedAt               time.Time                   `json:"updated_at"`
	CapturedAt              time.Time                   `json:"captured_at"`
	ResourceReadbackMatched bool                        `json:"resource_readback_matched"`
	RequiresExclusiveFence  bool                        `json:"requires_exclusive_fence"`
}

// TerminalOperationObservation has no public constructor or JSON input route.
// It cannot be forged from a pasted receipt. Its redacted Record is serializable
// for a future immutable evidence journal, not a replacement for that journal.
type TerminalOperationObservation struct {
	data     TerminalOperationRecord
	verified bool
}

func (o TerminalOperationObservation) Valid() bool {
	return o.verified && operationID(o.data.OperationID) && o.data.StatusCode == 200 && o.data.RequiresExclusiveFence
}
func (o TerminalOperationObservation) Record() TerminalOperationRecord {
	if !o.Valid() {
		return TerminalOperationRecord{}
	}
	return o.data
}

func (c *Client) exactEvidenceResource(raw string, expectedName string) error {
	actual, err := c.resourcePath(raw)
	if err != nil || actual != expectedName {
		return ErrResponse
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawPath != "" || parsed.Path != "/1.0/instances/"+expectedName {
		return ErrResponse
	}
	want := ""
	if c.project != "default" {
		want = url.Values{"project": {c.project}}.Encode()
	}
	if parsed.RawQuery != want {
		return ErrResponse
	}
	return nil
}

// CaptureTerminalOperation performs a bounded single authenticated operation GET,
// not polling, dispatch, cleanup or recovery-by-timeout. Incus 6.0.4 purges terminal
// operations after ~5s: missing/expired/pending/failed evidence remains UNKNOWN.
// Create additionally reobserves the same incarnation. Delete does NOT translate
// a later 404 into absence proof or bind its name-only resource to a UUID by fiat.
func (c *Client) CaptureTerminalOperation(ctx context.Context, prior VerifiedInstanceIdentity, id string, kind RecoveryOperationKind) (TerminalOperationObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !prior.Valid() || !operationID(id) || (kind != RecoveryCreate && kind != RecoveryDelete) || prior.data.Subject.Project != c.project {
		return TerminalOperationObservation{}, ErrConfiguration
	}
	physical, err := c.PhysicalIdentity(ctx)
	if err != nil {
		return TerminalOperationObservation{}, err
	}
	if physical.ServerVersion() != "6.0.4" {
		return TerminalOperationObservation{}, ErrUnsupported
	}
	if physical != prior.physical {
		return TerminalOperationObservation{}, ErrResponse
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/operations/"+id, nil, nil)
	if err != nil {
		return TerminalOperationObservation{}, err
	}
	if response.kind != "sync" || response.serverKeyFingerprint != physical.ServerKeyFingerprint() {
		return TerminalOperationObservation{}, ErrResponse
	}
	fields, err := object(response.metadata)
	if err != nil {
		return TerminalOperationObservation{}, err
	}
	returnedID, err := field[string](fields, "id")
	if err != nil || returnedID != id {
		return TerminalOperationObservation{}, ErrResponse
	}
	class, err := field[string](fields, "class")
	if err != nil || class != "task" {
		return TerminalOperationObservation{}, ErrResponse
	}
	description, err := field[string](fields, "description")
	want := "Creating instance"
	if kind == RecoveryDelete {
		want = "Deleting instance"
	}
	if err != nil || description != want {
		return TerminalOperationObservation{}, ErrResponse
	}
	status, err := field[int](fields, "status_code")
	if err != nil {
		return TerminalOperationObservation{}, err
	}
	if status != 200 {
		return TerminalOperationObservation{}, ErrOperation
	}
	statusText, err := field[string](fields, "status")
	if err != nil || statusText != "Success" {
		return TerminalOperationObservation{}, ErrResponse
	}
	operationError, err := field[string](fields, "err")
	if err != nil {
		return TerminalOperationObservation{}, ErrResponse
	}
	if operationError != "" {
		return TerminalOperationObservation{}, ErrOperation
	}
	resources, err := operationResources(fields)
	if err != nil || len(resources) != 1 || len(resources["instances"]) != 1 {
		return TerminalOperationObservation{}, ErrResponse
	}
	if c.exactEvidenceResource(resources["instances"][0], prior.data.Subject.Name) != nil {
		return TerminalOperationObservation{}, ErrResponse
	}
	createdAt, err := identityTime(fields, "created_at")
	if err != nil {
		return TerminalOperationObservation{}, err
	}
	updatedAt, err := identityTime(fields, "updated_at")
	if err != nil || updatedAt.Before(createdAt) {
		return TerminalOperationObservation{}, ErrResponse
	}
	readback := false
	if kind == RecoveryCreate {
		subject := prior.data.Subject
		current, err := c.ObserveInstanceIdentity(ctx, IdentityExpectation{Name: subject.Name, ControllerID: subject.ControllerID, PoolID: subject.PoolID, ReservationKey: subject.ReservationKey, CPUMillis: subject.CPUMillis, RAMMiB: subject.RAMMiB})
		if err != nil {
			return TerminalOperationObservation{}, err
		}
		if current.data.SubjectFingerprint != prior.data.SubjectFingerprint {
			return TerminalOperationObservation{}, ErrResponse
		}
		readback = true
	}
	record := TerminalOperationRecord{Identity: prior.data, OperationID: id, Kind: kind, Class: class, Description: description, StatusCode: status, CreatedAt: createdAt, UpdatedAt: updatedAt, CapturedAt: time.Now().UTC(), ResourceReadbackMatched: readback, RequiresExclusiveFence: true}
	return TerminalOperationObservation{data: record, verified: true}, nil
}
