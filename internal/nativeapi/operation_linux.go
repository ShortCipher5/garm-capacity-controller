//go:build linux

package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// StartBody is fixed, never caller options. This low-level adapter is not the
// authorization boundary: owner control must consume a fresh canonical step
// before each call and must never retry a mutation on transport uncertainty.
const StartBody = `{"action":"start","timeout":-1,"force":false,"stateful":false}`

type Operation struct {
	domain, policy, name, id, outcome string
	observed                          time.Time
}

func (o Operation) Valid() bool {
	return hash(o.domain) && hash(o.policy) && name(o.name) && uuid(o.id) && !o.observed.IsZero() && (o.outcome == "accepted" || o.outcome == "succeeded" || o.outcome == "failed")
}
func (o Operation) ID() string           { return o.id }
func (o Operation) Name() string         { return o.name }
func (o Operation) DomainID() string     { return o.domain }
func (o Operation) PolicyDigest() string { return o.policy }
func (o Operation) Outcome() string      { return o.outcome }

// CreateStopped submits exactly once. Unknown/error returns no retry permission.
// Calling this method requires a canonical create_stopped claim in the owner.
func (c *Client) CreateStopped(ctx context.Context, p Prepared) (Operation, error) {
	if !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return Operation{}, ErrNative
	}
	if _, e := c.Identity(ctx); e != nil {
		return Operation{}, ErrNative
	}
	return c.submit(ctx, http.MethodPost, "/1.0/instances", p.Body(), p.name)
}

// Start submits exactly once after a fresh local quota observation. The caller
// must also have the canonical START claim; this proof alone is not authority.
func (c *Client) Start(ctx context.Context, p Prepared, q StoppedQuota) (Operation, error) {
	if !c.matches(p) || c.CheckBoundary(ctx) != nil || !q.Valid() || q.domain != c.policy.DomainID() || q.policy != c.policy.Digest() || q.name != p.name || q.disk.Bytes() != p.allocation.Disk*(1<<20) || time.Since(q.observed) < 0 || time.Since(q.observed) > 5*time.Second {
		return Operation{}, ErrNative
	}
	id, e := c.stopped(ctx, p)
	if e != nil || id != q.resource {
		return Operation{}, ErrNative
	}
	if _, e := c.Identity(ctx); e != nil {
		return Operation{}, ErrNative
	}
	return c.submit(ctx, http.MethodPut, "/1.0/instances/"+p.name+"/state", []byte(StartBody), p.name)
}
func (c *Client) matches(p Prepared) bool {
	if c == nil || c.domain == nil || c.http == nil || p.pool.Type != "container" {
		return false
	}
	again, e := c.policy.Compile(p.allocation, preparedUserData(p))
	return e == nil && again.digest == p.digest
}
func (c *Client) submit(ctx context.Context, method, path string, body []byte, name string) (Operation, error) {
	r, e := http.NewRequestWithContext(ctx, method, "http://native.invalid"+path, bytes.NewReader(body))
	if e != nil {
		return Operation{}, ErrNative
	}
	r.Header.Set("Content-Type", "application/json")
	response, e := c.http.Do(r)
	if e != nil {
		return Operation{}, ErrNative
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if e != nil || len(raw) > 1<<20 || response.StatusCode != http.StatusAccepted {
		return Operation{}, ErrNative
	}
	envelope, e := object(raw)
	if e != nil {
		return Operation{}, ErrNative
	}
	kind, e := field[string](envelope, "type")
	if e != nil || kind != "async" {
		return Operation{}, ErrNative
	}
	status, e := field[int](envelope, "status_code")
	if e != nil || status != 100 {
		return Operation{}, ErrNative
	}
	op, e := parseOperation(envelope["metadata"], c.policy.DomainID(), c.policy.Digest(), name)
	if e != nil {
		return Operation{}, ErrNative
	}
	uri, e := field[string](envelope, "operation")
	if e != nil || uri != "/1.0/operations/"+op.id {
		return Operation{}, ErrNative
	}
	return op, nil
}

// ObserveOperation is read-only and cannot adopt arbitrary caller IDs: Operation
// has private fields and originates in a response from this exact private domain.
func (c *Client) ObserveOperation(ctx context.Context, op Operation) (Operation, error) {
	if c == nil || !op.Valid() || op.domain != c.policy.DomainID() || op.policy != c.policy.Digest() {
		return Operation{}, ErrNative
	}
	raw, e := c.read(ctx, "/1.0/operations/"+op.id)
	if e != nil {
		return Operation{}, ErrNative
	}
	m, e := syncMetadata(raw)
	if e != nil {
		return Operation{}, ErrNative
	}
	b, _ := json.Marshal(m)
	next, e := parseOperation(b, op.domain, op.policy, op.name)
	if e != nil || next.id != op.id {
		return Operation{}, ErrNative
	}
	if op.outcome != "accepted" && next.outcome != op.outcome {
		return Operation{}, ErrNative
	}
	return next, nil
}
func parseOperation(raw []byte, domain, policy, instance string) (Operation, error) {
	m, e := object(raw)
	if e != nil {
		return Operation{}, ErrNative
	}
	id, e := field[string](m, "id")
	if e != nil || !uuid(id) {
		return Operation{}, ErrNative
	}
	class, e := field[string](m, "class")
	if e != nil || class != "task" {
		return Operation{}, ErrNative
	}
	resource, e := object(m["resources"])
	if e != nil || len(resource) != 1 {
		return Operation{}, ErrNative
	}
	instances, e := field[[]string](resource, "instances")
	if e != nil || len(instances) != 1 || instances[0] != "/1.0/instances/"+instance {
		return Operation{}, ErrNative
	}
	status, e := field[int](m, "status_code")
	if e != nil {
		return Operation{}, ErrNative
	}
	outcome := ""
	switch status {
	case 100, 103, 105:
		outcome = "accepted"
	case 200:
		outcome = "succeeded"
	case 400, 401:
		outcome = "failed"
	default:
		return Operation{}, ErrNative
	}
	errText, e := field[string](m, "err")
	if e != nil || (outcome != "failed" && errText != "") {
		return Operation{}, ErrNative
	}
	op := Operation{domain, policy, instance, id, outcome, time.Now().UTC()}
	if !op.Valid() {
		return Operation{}, ErrNative
	}
	return op, nil
}
