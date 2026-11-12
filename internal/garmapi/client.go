// Package garmapi supplies a bounded read-only inventory client for Garm v0.2.1.
package garmapi

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
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
)

var ErrInventory = errors.New("complete Garm inventory unavailable")

type Client struct {
	endpoint  string
	token     string
	providers map[string]bool
	scalesets map[uint64]string
	http      *http.Client
}

// New requires explicit HTTPS CA trust and a nonempty exact provider-name scope.
// Token comes from trusted gateway secret storage, never a browser/job payload.
// No environment proxy, redirect, login flow or write method is available.
func New(endpoint, token string, trust *tls.Config, providers []string, scalesets map[uint64]string) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || len(token) == 0 || len(token) > 16384 || strings.ContainsAny(token, "\r\n") || trust == nil || trust.InsecureSkipVerify || trust.RootCAs == nil || len(providers) == 0 {
		return nil, ErrInventory
	}
	tlsConfig := trust.Clone()
	tlsConfig.RootCAs = trust.RootCAs.Clone()
	if tlsConfig.ServerName != "" && tlsConfig.ServerName != parsed.Hostname() {
		return nil, ErrInventory
	}
	tlsConfig.ServerName = parsed.Hostname()
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if tlsConfig.MaxVersion != 0 && tlsConfig.MaxVersion < tlsConfig.MinVersion {
		return nil, ErrInventory
	}
	parsed.Path = "/api/v1/instances"
	c := &Client{endpoint: parsed.String(), token: token, providers: map[string]bool{}, scalesets: map[uint64]string{}, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second}}}
	for _, provider := range providers {
		if provider == "" || c.providers[provider] {
			return nil, ErrInventory
		}
		c.providers[provider] = true
	}
	for id, pool := range scalesets {
		if id == 0 || pool == "" {
			return nil, ErrInventory
		}
		c.scalesets[id] = pool
	}
	return c, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func row(data json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInventory
	}
	result := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrInventory
		}
		if _, exists := result[name]; exists {
			return nil, ErrInventory
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrInventory
		}
		result[name] = value
	}
	if _, err = decoder.Token(); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrInventory
	}
	return result, nil
}
func textField(fields map[string]json.RawMessage, name string, required bool) (string, error) {
	raw, ok := fields[name]
	if !ok && !required {
		return "", nil
	}
	var value string
	if !ok || json.Unmarshal(raw, &value) != nil || string(raw) == "null" || (required && value == "") {
		return "", ErrInventory
	}
	return value, nil
}

// Instances consumes the complete unpaginated v0.2.1 /instances collection,
// filters only exact configured providers, and discards all fault/job payloads.
// Unknown scaleset/pool placement or malformed rows fail closed.
func (c *Client) Instances(ctx context.Context) ([]gateway.GarmInstance, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, ErrInventory
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrInventory
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrInventory
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return nil, ErrInventory
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, ErrInventory
	}
	var rows []json.RawMessage
	if json.Unmarshal(body, &rows) != nil {
		return nil, ErrInventory
	}
	result := []gateway.GarmInstance{}
	seen := map[string]bool{}
	for _, raw := range rows {
		fields, err := row(raw)
		if err != nil {
			return nil, err
		}
		provider, err := textField(fields, "provider_name", true)
		if err != nil {
			return nil, err
		}
		if !c.providers[provider] {
			continue
		}
		instance := gateway.GarmInstance{}
		instance.Name, err = textField(fields, "name", true)
		if err != nil || seen[instance.Name] {
			return nil, ErrInventory
		}
		seen[instance.Name] = true
		instance.ProviderID, err = textField(fields, "provider_id", false)
		if err != nil {
			return nil, err
		}
		instance.PoolID, err = textField(fields, "pool_id", false)
		if err != nil {
			return nil, err
		}
		var scaleSet uint64
		if raw, ok := fields["scale_set_id"]; ok {
			if string(raw) == "null" || json.Unmarshal(raw, &scaleSet) != nil {
				return nil, ErrInventory
			}
		}
		if scaleSet != 0 {
			pool, ok := c.scalesets[scaleSet]
			if !ok || (instance.PoolID != "" && instance.PoolID != pool) {
				return nil, ErrInventory
			}
			instance.PoolID = pool
		}
		if instance.PoolID == "" {
			return nil, ErrInventory
		}
		instance.State, err = textField(fields, "status", true)
		if err != nil {
			return nil, err
		}
		runner, err := textField(fields, "runner_status", false)
		if err != nil {
			return nil, err
		}
		instance.Busy = runner == "active" || runner == "busy"
		instance.Idle = runner == "idle"
		// A job object is conservatively treated as busy, without decoding its private details.
		if job, ok := fields["job"]; ok && !bytes.Equal(bytes.TrimSpace(job), []byte("null")) {
			instance.Busy = true
		}
		result = append(result, instance)
	}
	return result, nil
}
