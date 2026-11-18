//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"io"
	"net/http"
	"time"
)

// RetirementStopBody matches the canonical retirement STOP request. It never
// forces a running job down. Protected PID1 must finish its post-job fence first.
const RetirementStopBody = `{"action":"stop","timeout":30,"force":false,"stateful":false}`

type RetirementOperation struct {
	operation                     Operation
	resource, step, dataset, guid string
	bytes                         int64
}

func (o RetirementOperation) Valid() bool {
	return o.operation.Valid() && uuid(o.resource) && (o.step == "stop" || o.step == "delete") && resourceDataset(o.dataset) && o.guid != "" && o.bytes > 0
}
func (o RetirementOperation) OperationID() string { return o.operation.ID() }
func (o RetirementOperation) Outcome() string     { return o.operation.Outcome() }
func (o RetirementOperation) ResourceID() string  { return o.resource }
func (o RetirementOperation) Step() string        { return o.step }
func (o RetirementOperation) Dataset() string     { return o.dataset }
func (o RetirementOperation) DatasetGUID() string { return o.guid }

// StopForRetirement is a one-submission adapter, not admission authority. The
// authenticated owner must commit a fresh canonical retirement STOP claim first.
// Legacy unconfined domains are forbidden. Capsule CheckBoundary currently
// refuses activation, so this real protocol is dormant until that boundary is
// genuinely completed and qualified; a journal flag cannot bypass the gate.
func (c *Client) StopForRetirement(ctx context.Context, p Prepared, q StoppedQuota, reader *nativequota.Reader) (RetirementOperation, error) {
	if c == nil || c.capsule == nil || !c.matches(p) || c.CheckBoundary(ctx) != nil || !retirementQuotaMatches(c, p, q) || reader == nil {
		return RetirementOperation{}, ErrNative
	}
	journal, e := c.ObserveProtectedJournal(ctx, p, q.resource)
	if e != nil || !journal.Valid() || journal.record.State != "process_tree_exited_unqualified" || !journal.record.GuestNetworkClosed || journal.record.JobCompletionAuthorized || journal.resource != q.resource || time.Since(journal.observed) < 0 || time.Since(journal.observed) > 5*time.Second {
		return RetirementOperation{}, ErrNative
	}
	disk, e := reader.Read(ctx, q.Dataset(), q.Bytes())
	if e != nil || disk.GUID() != q.DatasetGUID() {
		return RetirementOperation{}, ErrNative
	}
	resource, e := c.ObserveResource(ctx, p, true)
	if e != nil || resource != q.resource || c.CheckBoundary(ctx) != nil {
		return RetirementOperation{}, ErrNative
	}
	op, e := c.submit(ctx, http.MethodPut, "/1.0/instances/"+p.name+"/state", []byte(RetirementStopBody), p.name)
	if e != nil {
		return RetirementOperation{}, e
	}
	return RetirementOperation{op, q.resource, "stop", q.Dataset(), q.DatasetGUID(), q.Bytes()}, nil
}
func retirementQuotaMatches(c *Client, p Prepared, q StoppedQuota) bool {
	return q.Valid() && q.domain == c.policy.DomainID() && q.policy == c.policy.Digest() && q.name == p.name && q.Bytes() == p.allocation.Disk*(1<<20)
}

// DeleteForRetirement requires the actual same STOP operation to have succeeded,
// the exact native UUID to remain stopped, and the owned dataset GUID to remain
// unchanged. The owner must first consume a fresh canonical DELETE claim. No
// alternate-name deletion, force fallback or retry on uncertainty exists.
func (c *Client) DeleteForRetirement(ctx context.Context, p Prepared, stop RetirementOperation, reader *nativequota.Reader) (RetirementOperation, error) {
	if c == nil || c.capsule == nil || !c.matches(p) || c.CheckBoundary(ctx) != nil || !stop.Valid() || stop.step != "stop" || stop.Outcome() != "succeeded" || stop.operation.domain != c.policy.DomainID() || stop.operation.policy != c.policy.Digest() || stop.operation.name != p.name || reader == nil {
		return RetirementOperation{}, ErrNative
	}
	current, e := c.ObserveOperation(ctx, stop.operation)
	if e != nil || current.Outcome() != "succeeded" {
		return RetirementOperation{}, ErrNative
	}
	resource, e := c.stopped(ctx, p)
	if e != nil || resource != stop.resource {
		return RetirementOperation{}, ErrNative
	}
	disk, e := reader.Read(ctx, stop.dataset, stop.bytes)
	if e != nil || disk.GUID() != stop.guid {
		return RetirementOperation{}, ErrNative
	}
	resource, e = c.stopped(ctx, p)
	if e != nil || resource != stop.resource || c.CheckBoundary(ctx) != nil {
		return RetirementOperation{}, ErrNative
	}
	op, e := c.submit(ctx, http.MethodDelete, "/1.0/instances/"+p.name, nil, p.name)
	if e != nil {
		return RetirementOperation{}, e
	}
	return RetirementOperation{op, stop.resource, "delete", stop.dataset, stop.guid, stop.bytes}, nil
}
func (c *Client) ObserveRetirementOperation(ctx context.Context, p Prepared, prior RetirementOperation) (RetirementOperation, error) {
	if c == nil || c.capsule == nil || !c.matches(p) || !prior.Valid() || prior.operation.name != p.name || c.CheckBoundary(ctx) != nil {
		return RetirementOperation{}, ErrNative
	}
	op, e := c.ObserveOperation(ctx, prior.operation)
	if e != nil {
		return RetirementOperation{}, e
	}
	if prior.step == "stop" && op.Outcome() == "succeeded" {
		id, e := c.stopped(ctx, p)
		if e != nil || id != prior.resource {
			return RetirementOperation{}, ErrNative
		}
	}
	prior.operation = op
	return prior, nil
}

// DeletedInstanceObservation is native API absence ONLY. It is intentionally
// incapable of releasing capacity, proving ZFS destruction or guest cgroup exit.
type DeletedInstanceObservation struct {
	operation RetirementOperation
	observed  time.Time
	valid     bool
}

func (o DeletedInstanceObservation) Valid() bool             { return o.valid }
func (o DeletedInstanceObservation) ResourceID() string      { return o.operation.resource }
func (o DeletedInstanceObservation) ReleaseAuthorized() bool { return false }
func (c *Client) ObserveDeletedInstance(ctx context.Context, p Prepared, deleted RetirementOperation) (DeletedInstanceObservation, error) {
	if c == nil || c.capsule == nil || !c.matches(p) || !deleted.Valid() || deleted.step != "delete" || deleted.Outcome() != "succeeded" || deleted.operation.name != p.name || c.CheckBoundary(ctx) != nil {
		return DeletedInstanceObservation{}, ErrNative
	}
	op, e := c.ObserveOperation(ctx, deleted.operation)
	if e != nil || op.Outcome() != "succeeded" {
		return DeletedInstanceObservation{}, ErrNative
	}
	if _, e = c.Identity(ctx); e != nil {
		return DeletedInstanceObservation{}, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://native.invalid/1.0/instances/"+p.name, nil)
	if e != nil {
		return DeletedInstanceObservation{}, ErrNative
	}
	response, e := c.http.Do(req)
	if e != nil {
		return DeletedInstanceObservation{}, ErrNative
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 || !exactNativeAbsent(response.StatusCode, raw) {
		return DeletedInstanceObservation{}, ErrNative
	}
	if _, e = c.Identity(ctx); e != nil || c.CheckBoundary(ctx) != nil {
		return DeletedInstanceObservation{}, ErrNative
	}
	return DeletedInstanceObservation{deleted, time.Now().UTC(), true}, nil
}
func exactNativeAbsent(status int, raw []byte) bool {
	if status != 404 {
		return false
	}
	m, e := object(raw)
	if e != nil {
		return false
	}
	kind, e := field[string](m, "type")
	code, ce := field[int](m, "error_code")
	return e == nil && ce == nil && kind == "error" && code == 404
}
