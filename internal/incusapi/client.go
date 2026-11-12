// Package incusapi is a narrow credential-verified Incus REST transport.
// It has no automatic retry, delete, quota mutation or arbitrary proxy route.
package incusapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrConfiguration = errors.New("invalid Incus transport configuration")
var ErrTransport = errors.New("Incus request outcome unavailable")
var ErrResponse = errors.New("invalid or incomplete Incus response")
var ErrUnsupported = errors.New("required Incus capability unavailable")
var ErrOperation = errors.New("Incus operation did not confirm success")
var ErrUnbounded = errors.New("Incus allocation cannot be bounded")

const maxBody = 8 << 20

// Client owns its TLS/HTTP configuration. Endpoint and project must come from
// trusted administration, never a job, repository label or provider request.
type Client struct {
	base    url.URL
	project string
	http    *http.Client
}

func New(endpoint, project string, credentials *tls.Config) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.Hostname() == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || !name(project) || credentials == nil || credentials.InsecureSkipVerify || credentials.RootCAs == nil || len(credentials.Certificates) == 0 {
		return nil, ErrConfiguration
	}
	for _, certificate := range credentials.Certificates {
		if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
			return nil, ErrConfiguration
		}
	}
	transportTLS := credentials.Clone()
	transportTLS.RootCAs = credentials.RootCAs.Clone()
	if transportTLS.MinVersion < tls.VersionTLS12 {
		transportTLS.MinVersion = tls.VersionTLS12
	}
	if transportTLS.MaxVersion != 0 && transportTLS.MaxVersion < transportTLS.MinVersion {
		return nil, ErrConfiguration
	}
	if transportTLS.ServerName != "" && transportTLS.ServerName != base.Hostname() {
		return nil, ErrConfiguration
	}
	transportTLS.ServerName = base.Hostname()
	transport := &http.Transport{TLSClientConfig: transportTLS, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConnsPerHost: 4}
	base.Path = ""
	return &Client{base: *base, project: project, http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func name(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0)) {
			return false
		}
	}
	return true
}
func label(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func fingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
func operationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// object avoids encoding/json's last-key-wins and case-folded struct behavior.
// Unknown API fields remain forward-compatible; critical fields are exact keys.
func object(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrResponse
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrResponse
		}
		if _, exists := fields[key]; exists {
			return nil, ErrResponse
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrResponse
		}
		fields[key] = value
	}
	if _, err = decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrResponse
	}
	return fields, nil
}
func field[T any](fields map[string]json.RawMessage, key string) (T, error) {
	var result T
	raw, ok := fields[key]
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &result) != nil {
		return result, ErrResponse
	}
	return result, nil
}

type envelope struct {
	serverKeyFingerprint string
	kind                 string
	operation            string
	metadata             json.RawMessage
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, input any) (envelope, error) {
	target := c.base
	target.Path = path
	if query == nil {
		query = url.Values{}
	}
	query.Set("project", c.project)
	target.RawQuery = query.Encode()
	var reader io.Reader
	if input != nil {
		body, err := json.Marshal(input)
		if err != nil {
			return envelope{}, ErrConfiguration
		}
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return envelope{}, ErrConfiguration
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return envelope{}, errors.Join(ErrTransport, ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return envelope{}, errors.Join(ErrTransport, context.DeadlineExceeded)
		}
		return envelope{}, ErrTransport
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))
		return envelope{}, fmt.Errorf("%w: HTTP %d", ErrTransport, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return envelope{}, ErrResponse
	}
	fields, err := object(body)
	if err != nil {
		return envelope{}, err
	}
	kind, err := field[string](fields, "type")
	if err != nil {
		return envelope{}, err
	}
	status, err := field[int](fields, "status_code")
	if err != nil {
		return envelope{}, err
	}
	if (kind != "sync" || status != 200) && (kind != "async" || status != 100) {
		return envelope{}, ErrResponse
	}
	metadata, ok := fields["metadata"]
	if !ok {
		return envelope{}, ErrResponse
	}
	result := envelope{kind: kind, metadata: metadata}
	if response.TLS != nil && len(response.TLS.VerifiedChains) > 0 && len(response.TLS.PeerCertificates) > 0 {
		key := sha256.Sum256(response.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo)
		result.serverKeyFingerprint = fmt.Sprintf("%x", key)
	}
	if kind == "async" {
		result.operation, err = field[string](fields, "operation")
		if err != nil {
			return envelope{}, err
		}
	}
	return result, nil
}

func (c *Client) trusted(ctx context.Context, requireCreate bool) error {
	response, err := c.request(ctx, http.MethodGet, "/1.0", nil, nil)
	if err != nil {
		return err
	}
	return validateTrustedRoot(response, requireCreate)
}

func validateTrustedRoot(response envelope, requireCreate bool) error {
	if response.kind != "sync" {
		return ErrResponse
	}
	fields, err := object(response.metadata)
	if err != nil {
		return err
	}
	version, err := field[string](fields, "api_version")
	if err != nil || version != "1.0" {
		return ErrUnsupported
	}
	auth, err := field[string](fields, "auth")
	if err != nil || auth != "trusted" {
		return ErrUnsupported
	}
	// Incus v6.0.4 cluster operation listing may omit unreachable members yet
	// return HTTP 200. This transport has no membership-completeness proof, so
	// only an explicitly reported standalone server is qualified for any read
	// or create. Missing/null/type-confused topology is not standalone evidence.
	environment, err := object(fields["environment"])
	if err != nil {
		return ErrResponse
	}
	clustered, exists := environment["server_clustered"]
	if !exists {
		return ErrResponse
	}
	switch string(bytes.TrimSpace(clustered)) {
	case "false":
	case "true":
		return ErrUnsupported
	default:
		return ErrResponse
	}
	if !requireCreate {
		return nil
	}
	extensions, err := field[[]string](fields, "api_extensions")
	if err != nil {
		return err
	}
	for _, extension := range extensions {
		if extension == "instance_create_start" {
			return nil
		}
	}
	return ErrUnsupported
}

// InstanceSpec is produced by trusted gateway policy. It deliberately has no
// arbitrary config/device/remote-image URL map. Explicit instance limits win
// over mutable profile limits. Initial transport supports whole vCPU budgets.
type InstanceSpec struct {
	Name             string
	Type             string
	Profiles         []string
	ImageFingerprint string
	CPUMillis        int64
	RAMMiB           int64
	UserData         string
	ControllerID     string
	PoolID           string
	ReservationKey   string
}

type Created struct {
	OperationID string
	ResourceID  string
}

// Create sends exactly one POST, never automatically retries or compensates by
// deletion. A returned error after dispatch is unknown, not proof of absence.
func ValidateSpec(spec InstanceSpec) error {
	if !name(spec.Name) || (spec.Type != "container" && spec.Type != "virtual-machine") || len(spec.Profiles) == 0 || len(spec.Profiles) > 16 || !fingerprint(spec.ImageFingerprint) || !fingerprint(spec.ReservationKey) || spec.CPUMillis <= 0 || spec.CPUMillis%1000 != 0 || spec.RAMMiB <= 0 || spec.RAMMiB > int64(1<<63-1)/(1<<20) || len(spec.UserData) == 0 || len(spec.UserData) > 1<<20 || !utf8.ValidString(spec.UserData) || !label(spec.ControllerID) || !label(spec.PoolID) {
		return ErrConfiguration
	}
	seen := map[string]bool{}
	for _, profile := range spec.Profiles {
		if !name(profile) || seen[profile] {
			return ErrConfiguration
		}
		seen[profile] = true
	}
	return nil
}

func (c *Client) Create(ctx context.Context, spec InstanceSpec) (Created, error) {
	if err := ValidateSpec(spec); err != nil {
		return Created{}, err
	}
	if err := c.trusted(ctx, true); err != nil {
		return Created{}, err
	}
	config := map[string]string{"limits.cpu": strconv.FormatInt(spec.CPUMillis/1000, 10), "limits.memory": strconv.FormatInt(spec.RAMMiB, 10) + "MiB", "user.user-data": spec.UserData, "user.runner-controller-id": spec.ControllerID, "user.runner-pool-id": spec.PoolID, "user.garm-capacity.reservation": spec.ReservationKey}
	if spec.Type == "container" {
		config["limits.memory.enforce"] = "hard"
	}
	payload := struct {
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Profiles []string          `json:"profiles"`
		Config   map[string]string `json:"config"`
		Source   map[string]string `json:"source"`
		Start    bool              `json:"start"`
	}{spec.Name, spec.Type, spec.Profiles, config, map[string]string{"type": "image", "fingerprint": spec.ImageFingerprint}, true}
	response, err := c.request(ctx, http.MethodPost, "/1.0/instances", nil, payload)
	if err != nil {
		return Created{}, err
	}
	if response.kind != "async" {
		return Created{}, ErrResponse
	}
	id, err := c.operationPath(response.operation)
	if err != nil {
		return Created{}, err
	}
	result := Created{OperationID: id, ResourceID: spec.Name}
	metadata, err := object(response.metadata)
	if err != nil {
		return result, err
	}
	if raw, exists := metadata["id"]; exists {
		var returned string
		if json.Unmarshal(raw, &returned) != nil || returned != id {
			return result, ErrResponse
		}
	}
	return result, nil
}

func (c *Client) operationPath(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return "", ErrResponse
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", ErrResponse
	}
	for key, values := range query {
		if key != "project" || len(values) != 1 || values[0] != c.project {
			return "", ErrResponse
		}
	}
	const prefix = "/1.0/operations/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", ErrResponse
	}
	id := strings.TrimPrefix(parsed.Path, prefix)
	if !operationID(id) {
		return "", ErrResponse
	}
	return id, nil
}

func (c *Client) resourcePath(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return "", ErrResponse
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", ErrResponse
	}
	for key, values := range query {
		if key != "project" || len(values) != 1 || values[0] != c.project {
			return "", ErrResponse
		}
	}
	const prefix = "/1.0/instances/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", ErrResponse
	}
	id := strings.TrimPrefix(parsed.Path, prefix)
	if !name(id) {
		return "", ErrResponse
	}
	return id, nil
}

func operationResources(fields map[string]json.RawMessage) (map[string][]string, error) {
	raw, ok := fields["resources"]
	if !ok {
		return nil, ErrResponse
	}
	values, err := object(raw)
	if err != nil {
		return nil, err
	}
	result := map[string][]string{}
	for key, value := range values {
		var resources []string
		if len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '[' || json.Unmarshal(value, &resources) != nil {
			return nil, ErrResponse
		}
		result[key] = resources
	}
	return result, nil
}

func (c *Client) Wait(ctx context.Context, created Created) error {
	if !operationID(created.OperationID) || !name(created.ResourceID) {
		return ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		// Wait is exported and may be called without Create or after topology
		// changes. A previous poll's trusted standalone view is not a lease.
		if err := c.trusted(ctx, false); err != nil {
			return err
		}
		response, err := c.request(ctx, http.MethodGet, "/1.0/operations/"+created.OperationID+"/wait", url.Values{"timeout": {"10"}}, nil)
		if err != nil {
			return err
		}
		if response.kind != "sync" {
			return ErrResponse
		}
		fields, err := object(response.metadata)
		if err != nil {
			return err
		}
		id, err := field[string](fields, "id")
		if err != nil || id != created.OperationID {
			return ErrResponse
		}
		resources, err := operationResources(fields)
		if err != nil || len(resources["instances"]) != 1 {
			return ErrResponse
		}
		instance, err := c.resourcePath(resources["instances"][0])
		if err != nil || instance != created.ResourceID {
			return ErrResponse
		}
		status, err := field[int](fields, "status_code")
		if err != nil {
			return err
		}
		switch status {
		case 200:
			if err := c.trusted(ctx, false); err != nil {
				return err
			}
			state, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+created.ResourceID+"/state", nil, nil)
			if err != nil {
				return err
			}
			data, err := object(state.metadata)
			if err != nil || state.kind != "sync" {
				return ErrResponse
			}
			running, err := field[int](data, "status_code")
			if err != nil || running != 103 {
				return ErrOperation
			}
			return nil
		case 100, 103, 104, 105:
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(ErrOperation, ctx.Err())
			case <-timer.C:
			}
		case 400, 401:
			return ErrOperation
		default:
			return ErrResponse
		}
	}
}

// CreateAndWait is a transport convenience, not a durable execution boundary.
// A gateway must use Create, persist submitted correlation through its one-shot
// permit, then Wait. This helper cannot journal a receipt before waiting.
// Returned errors still retain known correlation and never imply safe release.
func (c *Client) CreateAndWait(ctx context.Context, spec InstanceSpec) (Created, error) {
	created, err := c.Create(ctx, spec)
	if err != nil {
		return created, err
	}
	return created, c.Wait(ctx, created)
}

// Instance is a minimal allocation view, excluding user-data and credentials.
type Instance struct {
	Name           string
	Status         int
	CPUMillis      int64
	RAMMiB         int64
	ControllerID   string
	PoolID         string
	ReservationKey string
}
type Operation struct {
	ID        string
	Status    int
	Instances []string
}

func parseInstance(row json.RawMessage) (Instance, error) {
	fields, err := object(row)
	if err != nil {
		return Instance{}, err
	}
	instance := Instance{}
	instance.Name, err = field[string](fields, "name")
	if err != nil || !name(instance.Name) {
		return Instance{}, ErrResponse
	}
	instance.Status, err = field[int](fields, "status_code")
	if err != nil {
		return Instance{}, err
	}
	configRaw, exists := fields["expanded_config"]
	if !exists {
		return Instance{}, ErrUnbounded
	}
	config, err := object(configRaw)
	if err != nil {
		return Instance{}, err
	}
	cpu, err := field[string](config, "limits.cpu")
	if err != nil {
		return Instance{}, ErrUnbounded
	}
	instance.CPUMillis, err = parseCPU(cpu)
	if err != nil {
		return Instance{}, err
	}
	memory, err := field[string](config, "limits.memory")
	if err != nil {
		return Instance{}, ErrUnbounded
	}
	instance.RAMMiB, err = parseMemory(memory)
	if err != nil {
		return Instance{}, err
	}
	for key, target := range map[string]*string{"user.runner-controller-id": &instance.ControllerID, "user.runner-pool-id": &instance.PoolID, "user.garm-capacity.reservation": &instance.ReservationKey} {
		if raw, ok := config[key]; ok {
			if json.Unmarshal(raw, target) != nil {
				return Instance{}, ErrResponse
			}
		}
	}
	return instance, nil
}

// Instance reads an exact named allocation without exposing bootstrap data.
func (c *Client) Instance(ctx context.Context, instanceName string) (Instance, error) {
	if !name(instanceName) {
		return Instance{}, ErrConfiguration
	}
	if err := c.trusted(ctx, false); err != nil {
		return Instance{}, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances/"+instanceName, nil, nil)
	if err != nil {
		return Instance{}, err
	}
	if response.kind != "sync" {
		return Instance{}, ErrResponse
	}
	instance, err := parseInstance(response.metadata)
	if err != nil {
		return Instance{}, err
	}
	if instance.Name != instanceName {
		return Instance{}, ErrResponse
	}
	return instance, nil
}

func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	if err := c.trusted(ctx, false); err != nil {
		return nil, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/instances", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return nil, err
	}
	if response.kind != "sync" {
		return nil, ErrResponse
	}
	var rows []json.RawMessage
	trimmed := bytes.TrimSpace(response.metadata)
	if len(trimmed) == 0 || trimmed[0] != '[' || json.Unmarshal(response.metadata, &rows) != nil {
		return nil, ErrResponse
	}
	result := make([]Instance, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		instance, err := parseInstance(row)
		if err != nil {
			return nil, err
		}
		if seen[instance.Name] {
			return nil, ErrResponse
		}
		seen[instance.Name] = true
		result = append(result, instance)
	}
	return result, nil
}

func (c *Client) Operations(ctx context.Context) ([]Operation, error) {
	if err := c.trusted(ctx, false); err != nil {
		return nil, err
	}
	response, err := c.request(ctx, http.MethodGet, "/1.0/operations", url.Values{"recursion": {"1"}}, nil)
	if err != nil {
		return nil, err
	}
	if response.kind != "sync" {
		return nil, ErrResponse
	}
	groups, err := object(response.metadata)
	if err != nil {
		return nil, err
	}
	result := []Operation{}
	seen := map[string]bool{}
	for _, raw := range groups {
		var rows []json.RawMessage
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &rows) != nil {
			return nil, ErrResponse
		}
		for _, row := range rows {
			fields, err := object(row)
			if err != nil {
				return nil, err
			}
			operation := Operation{}
			operation.ID, err = field[string](fields, "id")
			if err != nil || !operationID(operation.ID) || seen[operation.ID] {
				return nil, ErrResponse
			}
			seen[operation.ID] = true
			operation.Status, err = field[int](fields, "status_code")
			if err != nil {
				return nil, err
			}
			resources, err := operationResources(fields)
			if err != nil {
				return nil, err
			}
			for _, resource := range resources["instances"] {
				id, err := c.resourcePath(resource)
				if err != nil {
					return nil, err
				}
				operation.Instances = append(operation.Instances, id)
			}
			result = append(result, operation)
		}
	}
	return result, nil
}

func parseCPU(raw string) (int64, error) {
	if len(raw) > 32768 {
		return 0, ErrUnbounded
	}
	if !strings.ContainsAny(raw, ",-") {
		count, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || count <= 0 || count > int64(1<<63-1)/1000 {
			return 0, ErrUnbounded
		}
		return count * 1000, nil
	}
	seen := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return 0, ErrUnbounded
		}
		start, err := strconv.ParseInt(bounds[0], 10, 64)
		if err != nil || start < 0 || start > 4095 {
			return 0, ErrUnbounded
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.ParseInt(bounds[1], 10, 64)
			if err != nil || end < start || end > 4095 {
				return 0, ErrUnbounded
			}
		}
		for id := start; id <= end; id++ {
			seen[id] = true
		}
	}
	if len(seen) == 0 {
		return 0, ErrUnbounded
	}
	return int64(len(seen)) * 1000, nil
}

var memoryPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$`)

func parseMemory(raw string) (int64, error) {
	if len(raw) > 64 {
		return 0, ErrUnbounded
	}
	parts := memoryPattern.FindStringSubmatch(raw)
	if parts == nil {
		return 0, ErrUnbounded
	}
	factors := map[string]int64{"": 1, "B": 1, "kB": 1000, "KB": 1000, "MB": 1000 * 1000, "GB": 1000 * 1000 * 1000, "TB": 1000 * 1000 * 1000 * 1000, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}
	value, ok := new(big.Rat).SetString(parts[1])
	if !ok || value.Sign() <= 0 {
		return 0, ErrUnbounded
	}
	value.Mul(value, new(big.Rat).SetInt64(factors[parts[2]]))
	value.Quo(value, new(big.Rat).SetInt64(1<<20))
	quotient, remainder := new(big.Int).QuoRem(value.Num(), value.Denom(), new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 {
		return 0, ErrUnbounded
	}
	return quotient.Int64(), nil
}
