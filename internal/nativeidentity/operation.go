package nativeidentity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const NativeStartBody = `{"action":"start","timeout":-1,"force":false,"stateful":false}`

func NativeStartDigest() string { return Fingerprint([]byte(NativeStartBody)) }

type OperationRecord struct {
	PermitID       string `json:"permit_id"`
	EnvelopeDigest string `json:"envelope_digest"`
	ParentClaimID  string `json:"parent_claim_id"`
	Step           string `json:"step"`
	StepClaimID    string `json:"step_claim_id"`
	DomainID       string `json:"domain_id"`
	PolicyDigest   string `json:"policy_digest"`
	NativeName     string `json:"native_name"`
	OperationID    string `json:"operation_id"`
	Outcome        string `json:"outcome"`
	ResourceID     string `json:"resource_id"`
}
type OperationObservation struct {
	record     OperationRecord
	serverLeaf string
	at         time.Time
	valid      bool
}

func (o OperationObservation) Valid() bool { return o.valid && !o.at.IsZero() }
func (o OperationObservation) Record() OperationRecord {
	if !o.Valid() {
		return OperationRecord{}
	}
	return o.record
}
func (o OperationObservation) ObservedAt() time.Time   { return o.at }
func (o OperationObservation) OwnerServerLeaf() string { return o.serverLeaf }

type QuotaRecord struct {
	DatasetGUID       string `json:"dataset_guid"`
	QuotaKind         string `json:"quota_kind"`
	PermitID          string `json:"permit_id"`
	EnvelopeDigest    string `json:"envelope_digest"`
	ParentClaimID     string `json:"parent_claim_id"`
	CreateStepClaimID string `json:"create_step_claim_id"`
	DomainID          string `json:"domain_id"`
	PolicyDigest      string `json:"policy_digest"`
	NativeName        string `json:"native_name"`
	ResourceID        string `json:"resource_id"`
	CreateOperationID string `json:"create_operation_id"`
	Backend           string `json:"backend"`
	Dataset           string `json:"dataset"`
	RequestedBytes    int64  `json:"requested_bytes"`
	EnforcedBytes     int64  `json:"enforced_bytes"`
	InstanceState     string `json:"instance_state"`
}
type QuotaObservation struct {
	record     QuotaRecord
	serverLeaf string
	at         time.Time
	valid      bool
}

func (o QuotaObservation) Valid() bool { return o.valid && !o.at.IsZero() }
func (o QuotaObservation) Record() QuotaRecord {
	if !o.Valid() {
		return QuotaRecord{}
	}
	return o.record
}
func (o QuotaObservation) ObservedAt() time.Time   { return o.at }
func (o QuotaObservation) OwnerServerLeaf() string { return o.serverLeaf }

// readOwnerEvidence first confirms the actual descriptor, then reads evidence
// from the same pinned owner role. Configuration is trusted, not request JSON.
// The owner endpoint must mint responses only from its actual private native /
// root-volume read. There is no constructor promoting a pasted receipt.
func readOwnerEvidence(ctx context.Context, c Configuration, path string) ([]byte, time.Time, error) {
	if !validHash(c.DomainID) {
		return nil, time.Time{}, ErrObservation
	}
	if _, e := Observe(ctx, c); e != nil {
		return nil, time.Time{}, e
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil {
		return nil, time.Time{}, ErrObservation
	}
	u.Path = path
	cfg := c.TLS.Clone()
	cfg.MinVersion = tls.VersionTLS13
	cfg.KeyLogWriter = nil
	previous := cfg.VerifyConnection
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if s.Version < tls.VersionTLS13 || len(s.PeerCertificates) == 0 || len(s.VerifiedChains) == 0 || Fingerprint(s.PeerCertificates[0].Raw) != c.OwnerServerLeaf {
			return ErrObservation
		}
		if previous != nil {
			return previous(s)
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: cfg, Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, time.Time{}, ErrObservation
	}
	at := time.Now().UTC()
	response, e := client.Do(request)
	if e != nil {
		return nil, time.Time{}, ErrObservation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.TLS == nil || !response.TLS.HandshakeComplete {
		return nil, time.Time{}, ErrObservation
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 16385))
	if e != nil || len(data) > 16384 {
		return nil, time.Time{}, ErrObservation
	}
	return data, at, nil
}

// strictEvidence rejects duplicate, missing, unknown and trailing fields before
// decoding, including duplicate JSON keys that encoding/json otherwise accepts.
func strictEvidence(data []byte, target any, fields []string) error {
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return ErrObservation
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	seen := map[string]bool{}
	for d.More() {
		token, e := d.Token()
		k, ok := token.(string)
		if e != nil || !ok || !allowed[k] || seen[k] {
			return ErrObservation
		}
		seen[k] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrObservation
		}
	}
	token, e = d.Token()
	if e != nil || token != json.Delim('}') || len(seen) != len(allowed) {
		return ErrObservation
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrObservation
	}
	if json.Unmarshal(data, target) != nil {
		return ErrObservation
	}
	return nil
}
func validOperationID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validPermit(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n/\\")
}
func ObserveOperation(ctx context.Context, c Configuration, stepClaimID string) (OperationObservation, error) {
	return observeOperation(ctx, c, stepClaimID, false)
}
func ObserveOperationTerminal(ctx context.Context, c Configuration, stepClaimID string) (OperationObservation, error) {
	return observeOperation(ctx, c, stepClaimID, true)
}
func observeOperation(ctx context.Context, c Configuration, stepClaimID string, terminal bool) (OperationObservation, error) {
	if !validHash(stepClaimID) {
		return OperationObservation{}, ErrObservation
	}
	path := "/v1/native/operations/" + stepClaimID
	if terminal {
		path += "/terminal"
	}
	data, at, e := readOwnerEvidence(ctx, c, path)
	if e != nil {
		return OperationObservation{}, e
	}
	var r OperationRecord
	if strictEvidence(data, &r, []string{"permit_id", "envelope_digest", "parent_claim_id", "step", "step_claim_id", "domain_id", "policy_digest", "native_name", "operation_id", "outcome", "resource_id"}) != nil {
		return OperationObservation{}, ErrObservation
	}
	if !validPermit(r.PermitID) || !validHash(r.EnvelopeDigest) || !validHash(r.ParentClaimID) || r.StepClaimID != stepClaimID || r.DomainID != c.DomainID || r.PolicyDigest != c.PolicyDigest || !validName(r.NativeName) || (r.Step != "create_stopped" && r.Step != "start") {
		return OperationObservation{}, ErrObservation
	}
	if !terminal && r.Outcome != "accepted" && r.Outcome != "unknown" {
		return OperationObservation{}, ErrObservation
	}
	switch r.Outcome {
	case "accepted":
		if !validOperationID(r.OperationID) {
			return OperationObservation{}, ErrObservation
		}
	case "succeeded":
		if !validOperationID(r.OperationID) || !validOperationID(r.ResourceID) {
			return OperationObservation{}, ErrObservation
		}
	case "failed":
		if !validOperationID(r.OperationID) {
			return OperationObservation{}, ErrObservation
		}
	case "unknown":
		if r.OperationID != "" && !validOperationID(r.OperationID) {
			return OperationObservation{}, ErrObservation
		}
	default:
		return OperationObservation{}, ErrObservation
	}
	if r.ResourceID != "" && !validOperationID(r.ResourceID) {
		return OperationObservation{}, ErrObservation
	}
	return OperationObservation{record: r, serverLeaf: c.OwnerServerLeaf, at: at, valid: true}, nil
}
func ObserveQuota(ctx context.Context, c Configuration, createStepClaimID string) (QuotaObservation, error) {
	if !validHash(createStepClaimID) {
		return QuotaObservation{}, ErrObservation
	}
	data, at, e := readOwnerEvidence(ctx, c, "/v1/native/quotas/"+createStepClaimID)
	if e != nil {
		return QuotaObservation{}, e
	}
	var r QuotaRecord
	if strictEvidence(data, &r, []string{"dataset_guid", "quota_kind", "permit_id", "envelope_digest", "parent_claim_id", "create_step_claim_id", "domain_id", "policy_digest", "native_name", "resource_id", "create_operation_id", "backend", "dataset", "requested_bytes", "enforced_bytes", "instance_state"}) != nil {
		return QuotaObservation{}, ErrObservation
	}
	guid, ge := strconv.ParseUint(r.DatasetGUID, 10, 64)
	if ge != nil || guid == 0 || strconv.FormatUint(guid, 10) != r.DatasetGUID || r.QuotaKind != "quota" || !validPermit(r.PermitID) || !validHash(r.EnvelopeDigest) || !validHash(r.ParentClaimID) || r.CreateStepClaimID != createStepClaimID || r.DomainID != c.DomainID || r.PolicyDigest != c.PolicyDigest || !validName(r.NativeName) || !validOperationID(r.ResourceID) || !validOperationID(r.CreateOperationID) || r.Backend != "zfs" || !validName(r.Dataset) || r.RequestedBytes <= 0 || r.EnforcedBytes != r.RequestedBytes || r.InstanceState != "stopped" {
		return QuotaObservation{}, ErrObservation
	}
	return QuotaObservation{record: r, serverLeaf: c.OwnerServerLeaf, at: at, valid: true}, nil
}

// Valid checks record shape only; it does not manufacture an Observation.
func (r OperationRecord) Valid() bool {
	if !validPermit(r.PermitID) || !validHash(r.EnvelopeDigest) || !validHash(r.ParentClaimID) || !validHash(r.StepClaimID) || !validHash(r.DomainID) || !validHash(r.PolicyDigest) || !validName(r.NativeName) || (r.Step != "create_stopped" && r.Step != "start") || (r.ResourceID != "" && !validOperationID(r.ResourceID)) {
		return false
	}
	switch r.Outcome {
	case "accepted", "failed":
		return validOperationID(r.OperationID)
	case "succeeded":
		return validOperationID(r.OperationID) && validOperationID(r.ResourceID)
	case "unknown":
		return r.OperationID == "" || validOperationID(r.OperationID)
	}
	return false
}
func (r QuotaRecord) Valid() bool {
	guid, e := strconv.ParseUint(r.DatasetGUID, 10, 64)
	return e == nil && guid > 0 && strconv.FormatUint(guid, 10) == r.DatasetGUID && r.QuotaKind == "quota" && validPermit(r.PermitID) && validHash(r.EnvelopeDigest) && validHash(r.ParentClaimID) && validHash(r.CreateStepClaimID) && validHash(r.DomainID) && validHash(r.PolicyDigest) && validName(r.NativeName) && validOperationID(r.ResourceID) && validOperationID(r.CreateOperationID) && r.Backend == "zfs" && validName(r.Dataset) && r.RequestedBytes > 0 && r.EnforcedBytes == r.RequestedBytes && r.InstanceState == "stopped"
}
