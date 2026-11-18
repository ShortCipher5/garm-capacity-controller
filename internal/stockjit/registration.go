// Package stockjit reads the stock GARM JIT registration identity before guest
// boot. It cannot create/delete resources, authorize retirement or free capacity.
package stockjit

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	common "github.com/cloudbase/garm-provider-common/params"
)

var ErrPolicy = errors.New("stock JIT registration policy rejected")
var ErrObservation = errors.New("stock JIT registration identity unavailable or conflicting")

// Policy is deployment-owned, never populated from bootstrap URLs or extraspecs.
// MetadataURL must be the exact stock /api/v1/metadata endpoint. HTTPS reverse
// proxies with a path prefix are deliberately unsupported in this first adapter.
type Policy struct{ MetadataURL, ControllerID, PoolID, GitHubURL string }
type Client struct {
	policy Policy
	http   *http.Client
}

// Record contains only identity, never the instance token or credential files.
// A record is evidence from trusted GARM metadata, not physical/job authority.
type Record struct {
	ControllerID         string    `json:"controller_id"`
	PoolID               string    `json:"pool_id"`
	Name                 string    `json:"name"`
	RunnerID             int64     `json:"runner_id"`
	GitHubURL            string    `json:"github_url"`
	MetadataOriginDigest string    `json:"metadata_origin_digest"`
	ObservedAt           time.Time `json:"observed_at"`
}
type Observation struct {
	record   Record
	verified bool
}

func (o Observation) Valid() bool { return o.verified && o.record.RunnerID > 0 }
func (o Observation) Record() Record {
	if !o.Valid() {
		return Record{}
	}
	return o.record
}

func cleanURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || strings.ContainsAny(raw, "\\\r\n\t ?#") {
		return nil, ErrPolicy
	}
	if u.Hostname() == "" || u.Hostname() != strings.ToLower(u.Hostname()) {
		return nil, ErrPolicy
	}
	return u, nil
}
func label(v string) bool {
	return utf8.ValidString(v) && v != "" && len(v) <= 256 && strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
func New(policy Policy, trust *tls.Config) (*Client, error) {
	metadata, err := cleanURL(policy.MetadataURL)
	if err != nil {
		return nil, ErrPolicy
	}
	github, err := cleanURL(policy.GitHubURL)
	if err != nil {
		return nil, ErrPolicy
	}
	if metadata.Path != "/api/v1/metadata" || github.Path == "" || github.Path == "/" || strings.HasSuffix(github.Path, "/") || !label(policy.ControllerID) || !label(policy.PoolID) || trust == nil || trust.InsecureSkipVerify || trust.ServerName != "" {
		return nil, ErrPolicy
	}
	secure := trust.Clone()
	if secure.RootCAs != nil {
		secure.RootCAs = secure.RootCAs.Clone()
	}
	if secure.MinVersion < tls.VersionTLS12 {
		secure.MinVersion = tls.VersionTLS12
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: secure, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 16384}
	return &Client{policy: policy, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Observe uses only GET credentials/runner from the configured GARM endpoint.
// It does not fetch .credentials, registration tokens or RSA secrets. Incoming
// metadata and repository URLs must match deployment policy before any request.
func (c *Client) Observe(ctx context.Context, controller string, b common.BootstrapInstance) (Observation, error) {
	if controller != c.policy.ControllerID || b.PoolID != c.policy.PoolID || !b.JitConfigEnabled || b.MetadataURL != c.policy.MetadataURL || b.RepoURL != c.policy.GitHubURL || !label(b.Name) || len(b.Name) > 128 || len(b.InstanceToken) < 1 || len(b.InstanceToken) > 16384 || strings.IndexFunc(b.InstanceToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Observation{}, ErrPolicy
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.policy.MetadataURL+"/credentials/runner", nil)
	if err != nil {
		return Observation{}, ErrObservation
	}
	request.Header.Set("Authorization", "Bearer "+b.InstanceToken)
	request.Header.Set("Accept", "application/octet-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return Observation{}, ErrObservation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Observation{}, ErrObservation
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return Observation{}, ErrObservation
	}
	id, name, github, ephemeral, err := parseRunner(body)
	if err != nil || id <= 0 || name != b.Name || github != c.policy.GitHubURL || !ephemeral {
		return Observation{}, ErrObservation
	}
	digest := sha256.Sum256([]byte(c.policy.MetadataURL))
	return Observation{verified: true, record: Record{ControllerID: controller, PoolID: b.PoolID, Name: name, RunnerID: id, GitHubURL: github, MetadataOriginDigest: hex.EncodeToString(digest[:]), ObservedAt: time.Now().UTC()}}, nil
}

// RunnerSettings uses camel-case JSON. Match the known fields case-insensitively
// but reject duplicate aliases so the parser cannot disagree with the runner.
// Unknown version-extension fields are ignored; none are copied to the output.
func parseRunner(body []byte) (int64, string, string, bool, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return 0, "", "", false, ErrObservation
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return 0, "", "", false, ErrObservation
		}
		key = strings.ToLower(key)
		if _, exists := fields[key]; exists {
			return 0, "", "", false, ErrObservation
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return 0, "", "", false, ErrObservation
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return 0, "", "", false, ErrObservation
	}
	if decoder.Decode(new(any)) != io.EOF {
		return 0, "", "", false, ErrObservation
	}
	var id int64
	var name, github string
	var ephemeral bool
	for key, destination := range map[string]any{"agentid": &id, "agentname": &name, "githuburl": &github, "ephemeral": &ephemeral} {
		raw, ok := fields[key]
		if !ok || string(raw) == "null" || json.Unmarshal(raw, destination) != nil {
			return 0, "", "", false, ErrObservation
		}
	}
	return id, name, github, ephemeral, nil
}
