//go:build linux

package nativeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedinit"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ProtectedJournal is exact root-file readback from one private native instance.
// Its state remains unqualified: it proves neither containment nor job safety.
type ProtectedJournal struct {
	record   protectedinit.Record
	resource string
	observed time.Time
	valid    bool
}

func (j ProtectedJournal) Valid() bool                  { return j.valid }
func (j ProtectedJournal) Record() protectedinit.Record { return j.record }
func (j ProtectedJournal) ObservedAt() time.Time        { return j.observed }
func parseProtectedJournal(raw []byte, domain, resource, initHash string, now time.Time) (protectedinit.Record, error) {
	var r protectedinit.Record
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return r, ErrNative
	}
	canonical, e := json.Marshal(r)
	if e != nil || !bytes.Equal(append(canonical, '\n'), raw) || r.Domain != domain || r.Incarnation != resource || r.LauncherSHA256 != initHash || r.At.IsZero() || r.At.After(now) || r.JobCompletionAuthorized {
		return r, ErrNative
	}
	switch r.State {
	case "launch_unknown":
		if r.ExitCode != -1 || r.GuestNetworkClosed {
			return r, ErrNative
		}
	case "process_tree_exited_unqualified":
		if r.ExitCode < 0 || r.ExitCode > 255 {
			return r, ErrNative
		}
	default:
		return r, ErrNative
	}
	return r, nil
}
func (c *Client) journalRequest(ctx context.Context, name, resource, path string) (*http.Response, error) {
	if c == nil || c.http == nil || !uuid(resource) {
		return nil, ErrNative
	}
	dir := "/var/lib/garm-capacity/" + resource
	method := http.MethodHead
	switch path {
	case "/", "/var", "/var/lib", "/var/lib/garm-capacity", dir:
	case dir + "/state.json":
		method = http.MethodGet
	default:
		return nil, ErrNative
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://native.invalid/1.0/instances/"+url.PathEscape(name)+"/files?"+url.Values{"path": []string{path}}.Encode(), nil)
	if e != nil {
		return nil, ErrNative
	}
	return c.http.Do(req)
}
func (c *Client) readProtectedJournal(ctx context.Context, name, domain, resource, initHash string) (protectedinit.Record, error) {
	dir := "/var/lib/garm-capacity/" + resource
	for _, p := range []string{"/", "/var", "/var/lib", "/var/lib/garm-capacity", dir} {
		r, e := c.journalRequest(ctx, name, resource, p)
		if e != nil {
			return protectedinit.Record{}, ErrNative
		}
		ok := r.StatusCode == 200 && rootDirectory(r.Header)
		r.Body.Close()
		if !ok {
			return protectedinit.Record{}, ErrNative
		}
	}
	r, e := c.journalRequest(ctx, name, resource, dir+"/state.json")
	if e != nil {
		return protectedinit.Record{}, ErrNative
	}
	defer r.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	if e != nil || len(raw) > 8192 || r.StatusCode != 200 || !rootFileHeaders(r.Header) {
		return protectedinit.Record{}, ErrNative
	}
	return parseProtectedJournal(raw, domain, resource, initHash, time.Now().UTC())
}
func (c *Client) ObserveProtectedJournal(ctx context.Context, p Prepared, resource string) (ProtectedJournal, error) {
	if !c.matches(p) || !c.policy.Protected() || !uuid(resource) {
		return ProtectedJournal{}, ErrNative
	}
	if _, e := c.Identity(ctx); e != nil {
		return ProtectedJournal{}, ErrNative
	}
	image, ok := c.policy.ProtectedImage(p.allocation.Pool)
	if !ok {
		return ProtectedJournal{}, ErrNative
	}
	before, e := c.ObserveResource(ctx, p, true)
	if e != nil || before != resource {
		return ProtectedJournal{}, ErrNative
	}
	record, e := c.readProtectedJournal(ctx, p.name, c.policy.DomainID(), resource, image.InitSHA256)
	if e != nil {
		return ProtectedJournal{}, ErrNative
	}
	after, e := c.ObserveResource(ctx, p, true)
	if e != nil || after != resource {
		return ProtectedJournal{}, ErrNative
	}
	return ProtectedJournal{record, resource, time.Now().UTC(), true}, nil
}
