//go:build linux

package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// resourceBootstrap is reachable only from the concrete capsule constructor.
// The functions are internal seams for fault tests, not caller-supplied authority.
// Every retry is refused by the durable consume function. Partial success is
// never rolled back, adopted from readback, or converted into capacity authority.
func resourceBootstrap(ctx context.Context, p ResourcePlan, client *http.Client, guard func(context.Context) error, consume, complete func(string) error) error {
	if ctx == nil || ctx.Err() != nil || !hash(p.digest) || client == nil || guard == nil || consume == nil || complete == nil || guard(ctx) != nil {
		return ErrNative
	}
	if consume(p.digest) != nil {
		return ErrNative
	}
	// Birth is the freshness proof. These reads only reject contradictory state.
	for _, path := range []string{"/1.0/storage-pools", "/1.0/instances"} {
		raw, _, e := resourceExchange(ctx, client, "GET", path, "")
		if e != nil || !emptyResourceList(raw) {
			return ErrNative
		}
	}
	raw, _, e := resourceExchange(ctx, client, "GET", "/1.0/networks", "")
	if e != nil || !initialNetworks(raw, p.uplink) {
		return ErrNative
	}
	for _, r := range p.requests {
		var requested struct {
			Name   string            `json:"name"`
			Config map[string]string `json:"config"`
		}
		if json.Unmarshal([]byte(r.Body), &requested) != nil || !name(requested.Name) {
			return ErrNative
		}
		target := r.Path + "/" + requested.Name
		// Never adopt a named object, even when its properties match our plan.
		_, res, e := resourceExchange(ctx, client, "GET", target, "")
		if e != nil || res.StatusCode != 404 {
			return ErrNative
		}
		if guard(ctx) != nil {
			return ErrNative
		}
		raw, res, e = resourceExchange(ctx, client, "POST", r.Path, r.Body)
		if e != nil || res.StatusCode != 201 || len(res.Header.Values("Location")) != 1 || res.Header.Get("Location") != target || !syncSuccess(raw) {
			return ErrNative
		}
		raw, res, e = resourceExchange(ctx, client, "GET", target, "")
		if e != nil || res.StatusCode != 200 || !resourceMatches(raw, r) {
			return ErrNative
		}
	}
	// All objects are re-read before marking completion so an earlier object's
	// drift cannot be hidden by a later successful creation.
	for _, r := range p.requests {
		var v struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(r.Body), &v) != nil {
			return ErrNative
		}
		raw, res, e := resourceExchange(ctx, client, "GET", r.Path+"/"+v.Name, "")
		if e != nil || res.StatusCode != 200 || !resourceMatches(raw, r) {
			return ErrNative
		}
	}
	if guard(ctx) != nil || complete(p.digest) != nil {
		return ErrNative
	}
	return nil
}
func resourceExchange(ctx context.Context, c *http.Client, method, path, body string) ([]byte, *http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, "http://native.invalid"+path, bytes.NewBufferString(body))
	if e != nil {
		return nil, nil, ErrNative
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := c.Do(req)
	if e != nil {
		return nil, nil, ErrNative
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return nil, nil, ErrNative
	}
	if res.StatusCode == 404 && method == "GET" {
		m, e := object(raw)
		if e != nil {
			return nil, nil, ErrNative
		}
		kind, e := field[string](m, "type")
		code, ce := field[int](m, "error_code")
		if e != nil || ce != nil || kind != "error" || code != 404 {
			return nil, nil, ErrNative
		}
		return raw, res, nil
	}
	if (res.StatusCode != 200 && res.StatusCode != 201) || !syncSuccess(raw) {
		return nil, nil, ErrNative
	}
	return raw, res, nil
}
func syncSuccess(raw []byte) bool {
	m, e := object(raw)
	if e != nil {
		return false
	}
	kind, e := field[string](m, "type")
	code, ce := field[int](m, "status_code")
	_, present := m["metadata"]
	return e == nil && ce == nil && kind == "sync" && code == 200 && present
}
func resourceList(raw []byte) ([]string, error) {
	if !syncSuccess(raw) {
		return nil, ErrNative
	}
	m, _ := object(raw)
	var v []string
	if json.Unmarshal(m["metadata"], &v) != nil || len(v) > 256 {
		return nil, ErrNative
	}
	return v, nil
}
func emptyResourceList(raw []byte) bool { v, e := resourceList(raw); return e == nil && len(v) == 0 }
func initialNetworks(raw []byte, uplink bool) bool {
	v, e := resourceList(raw)
	if uplink {
		if e != nil || len(v) != 2 {
			return false
		}
		return (v[0] == "/1.0/networks/lo" && v[1] == "/1.0/networks/garm0") || (v[1] == "/1.0/networks/lo" && v[0] == "/1.0/networks/garm0")
	}
	return e == nil && (len(v) == 0 || len(v) == 1 && v[0] == "/1.0/networks/lo")
}
func resourceMatches(raw []byte, r ResourceRequest) bool {
	m, e := syncMetadata(raw)
	if e != nil {
		return false
	}
	var want struct {
		Name, Type, Driver string
		Config             map[string]string
	}
	if json.Unmarshal([]byte(r.Body), &want) != nil {
		return false
	}
	n, e := field[string](m, "name")
	status, se := field[string](m, "status")
	if e != nil || se != nil || n != want.Name || status != "Created" {
		return false
	}
	used, ok := m["used_by"]
	if !ok {
		return false
	}
	var users []string
	if json.Unmarshal(used, &users) != nil || len(users) != 0 {
		return false
	}
	cfg, e := stringMap(m["config"])
	if e != nil {
		return false
	}
	for k, v := range want.Config {
		if cfg[k] != v {
			return false
		}
	}
	for k, v := range cfg {
		if _, ok := want.Config[k]; ok {
			continue
		}
		if r.Path == "/1.0/storage-pools" && k == "volatile.initial_source" && v == want.Config["source"] {
			continue
		}
		return false
	}
	if r.Path == "/1.0/storage-pools" {
		driver, e := field[string](m, "driver")
		return e == nil && driver == "zfs"
	}
	if r.Path != "/1.0/networks" {
		return false
	}
	kind, e := field[string](m, "type")
	managed, me := field[bool](m, "managed")
	return e == nil && me == nil && kind == "bridge" && managed
}
