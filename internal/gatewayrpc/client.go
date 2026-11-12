package gatewayrpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	common "github.com/cloudbase/garm-provider-common/params"
)

var ErrUnavailable = errors.New("authenticated gateway outcome unavailable")

// Client holds gateway-only credentials. It has no Incus/Garm API credentials,
// database access, administrative method or automatic request retry.
type Client struct {
	base string
	http *http.Client
}

func NewClient(endpoint string, credentials *tls.Config) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || credentials == nil || credentials.InsecureSkipVerify || credentials.RootCAs == nil || len(credentials.Certificates) == 0 {
		return nil, ErrConfiguration
	}
	for _, cert := range credentials.Certificates {
		if len(cert.Certificate) == 0 || cert.PrivateKey == nil {
			return nil, ErrConfiguration
		}
	}
	config := credentials.Clone()
	config.RootCAs = credentials.RootCAs.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	if config.MaxVersion != 0 && config.MaxVersion < config.MinVersion {
		return nil, ErrConfiguration
	}
	if config.ServerName != "" && config.ServerName != parsed.Hostname() {
		return nil, ErrConfiguration
	}
	config.ServerName = parsed.Hostname()
	parsed.Path = ""
	return &Client{base: parsed.String(), http: &http.Client{Timeout: 11 * time.Minute, Transport: &http.Transport{TLSClientConfig: config, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 11 * time.Minute}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil || len(body) > MaximumRequestBytes {
		return ErrConfiguration
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return ErrConfiguration
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaximumResponseBytes))
		return ErrUnavailable
	}
	if Decode(response.Body, output) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) Admit(ctx context.Context, request garmprotocol.Request) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	if request.Validate() != nil {
		return 0, nil, ErrConfiguration
	}
	var result AdmissionResult
	if err := c.call(ctx, "/v1/admit", request, &result); err != nil {
		return 0, nil, err
	}
	if _, err := garmprotocol.Parse(result.ExitCode, result.Response, request); err != nil {
		return 0, nil, ErrUnavailable
	}
	return result.ExitCode, append(result.Response, '\n'), nil
}
func (c *Client) Create(ctx context.Context, request common.BootstrapInstance) (common.ProviderInstance, error) {
	var result common.ProviderInstance
	if err := c.call(ctx, "/v1/create", request, &result); err != nil {
		return common.ProviderInstance{}, err
	}
	if result.Name != request.Name || result.ProviderID != request.Name || result.Status != common.InstanceRunning || len(result.ProviderFault) != 0 {
		return common.ProviderInstance{}, ErrUnavailable
	}
	return result, nil
}
func (c *Client) Get(ctx context.Context, request Identity) (common.ProviderInstance, error) {
	var result common.ProviderInstance
	if err := c.call(ctx, "/v1/get", request, &result); err != nil {
		return common.ProviderInstance{}, err
	}
	if result.Name != request.Name || result.ProviderID != request.Name || len(result.ProviderFault) != 0 {
		return common.ProviderInstance{}, ErrUnavailable
	}
	return result, nil
}
func (c *Client) List(ctx context.Context, pool string) ([]common.ProviderInstance, error) {
	var result []common.ProviderInstance
	if err := c.call(ctx, "/v1/list", Identity{PoolID: pool}, &result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, instance := range result {
		if instance.Name == "" || instance.ProviderID != instance.Name || len(instance.ProviderFault) != 0 || seen[instance.Name] {
			return nil, ErrUnavailable
		}
		seen[instance.Name] = true
	}
	return result, nil
}

// CancelAdmission only returns the gateway's strict tombstone receipt. A lost
// response is not proof of release; retrying the exact identity is idempotent.
func (c *Client) CancelAdmission(ctx context.Context, request garmprotocol.Request) (garmprotocol.CancellationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	if request.Validate() != nil {
		return garmprotocol.CancellationResponse{}, ErrConfiguration
	}
	var response garmprotocol.CancellationResponse
	if err := c.call(ctx, "/v1/cancel-admission", request, &response); err != nil {
		return garmprotocol.CancellationResponse{}, err
	}
	data, err := json.Marshal(response)
	if err != nil {
		return garmprotocol.CancellationResponse{}, ErrUnavailable
	}
	result, err := garmprotocol.ParseCancellation(0, data, request)
	if err != nil {
		return garmprotocol.CancellationResponse{}, ErrUnavailable
	}
	return result, nil
}

func (c *Client) originalPreparation(ctx context.Context, request garmprotocol.PreparationRequest, validate bool) (garmprotocol.PreparationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	if request.Validate() != nil {
		return garmprotocol.PreparationResponse{}, ErrConfiguration
	}
	path, outcome := "/v1/record-preparation", "recorded"
	if validate {
		path, outcome = "/v1/validate-preparation", "validated"
	}
	// Keep raw response bytes: decoding/re-encoding would invent omitted zero/bool
	// fields and could turn a missing generation0 into an accepted acknowledgement.
	var raw json.RawMessage
	if err := c.call(ctx, path, request, &raw); err != nil {
		return garmprotocol.PreparationResponse{}, err
	}
	result, err := garmprotocol.ParsePreparationResponse(0, raw, request, outcome)
	if err != nil {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	return result, nil
}
func (c *Client) RecordPreparation(ctx context.Context, request garmprotocol.PreparationRequest) (garmprotocol.PreparationResponse, error) {
	return c.originalPreparation(ctx, request, false)
}
func (c *Client) ValidatePreparation(ctx context.Context, request garmprotocol.PreparationRequest) (garmprotocol.PreparationResponse, error) {
	return c.originalPreparation(ctx, request, true)
}
