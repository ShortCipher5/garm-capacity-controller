//go:build linux

package nativeapi

import (
	"bytes"
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/protectedboot"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// BootstrapFiles is actual byte/metadata readback, not fsync/boot/job proof.
type BootstrapFiles struct {
	resource string
	files    [2]protectedboot.File
}

func (b BootstrapFiles) Valid() bool {
	return uuid(b.resource) && b.files[0].Path == protectedboot.ManifestPath && b.files[1].Path == protectedboot.JITPath && hash(b.files[0].SHA256) && hash(b.files[1].SHA256) && b.files[0].Bytes > 0 && b.files[1].Bytes > 0
}
func (b BootstrapFiles) ResourceID() string { return b.resource }

// Digests contains no raw credential bytes.
func (b BootstrapFiles) Digests() [2]protectedboot.File {
	out := b.files
	for i := range out {
		out[i].Data = nil
	}
	return out
}

// DeliverBootstrap performs an ordered single POST per fixed, initially absent
// file. The owner must have consumed a canonical bootstrap batch claim first.
// Partial/lost ACK is unknown: no retry, overwrite, compensating delete or start.
func (c *Client) DeliverBootstrap(ctx context.Context, p Prepared, resource string) (BootstrapFiles, error) {
	fail := func() (BootstrapFiles, error) { return BootstrapFiles{}, ErrNative }
	if !c.matches(p) || !c.policy.Protected() || !uuid(resource) || c.CheckBoundary(ctx) != nil {
		return fail()
	}
	payload, e := protectedboot.Decode([]byte(preparedUserData(p)))
	if e != nil {
		return fail()
	}
	files, e := protectedboot.Materialize(payload, protectedboot.Binding{DomainID: c.policy.DomainID(), PolicyDigest: c.policy.Digest(), ResourceID: resource, ImageFingerprint: p.allocation.Fingerprint})
	if e != nil {
		return fail()
	}
	for _, f := range files {
		id, e := c.stopped(ctx, p)
		if e != nil || id != resource {
			return fail()
		}
		// Paths exist only in a qualified protected image; refuse writable/symlink
		// ancestors rather than follow a caller-controlled directory to another path.
		for _, dir := range []string{"/etc", "/etc/garm-capacity"} {
			r, e := c.fileRequest(ctx, p.name, http.MethodHead, dir, nil)
			if e != nil {
				return fail()
			}
			valid := r.StatusCode == 200 && rootDirectory(r.Header)
			r.Body.Close()
			if !valid {
				return fail()
			}
		}
		r, e := c.fileRequest(ctx, p.name, http.MethodHead, f.Path, nil)
		if e != nil {
			return fail()
		}
		absent := r.StatusCode == 404
		r.Body.Close()
		if !absent {
			return fail()
		}
		r, e = c.fileRequest(ctx, p.name, http.MethodPost, f.Path, f.Data)
		if e != nil {
			return fail()
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 65537))
		r.Body.Close()
		if e != nil || len(raw) > 65536 || r.StatusCode != 200 {
			return fail()
		}
		m, e := object(raw)
		if e != nil {
			return fail()
		}
		kind, e := field[string](m, "type")
		if e != nil || kind != "sync" {
			return fail()
		}
		status, e := field[int](m, "status_code")
		if e != nil || status != 200 {
			return fail()
		}
		r, e = c.fileRequest(ctx, p.name, http.MethodGet, f.Path, nil)
		if e != nil {
			return fail()
		}
		raw, e = io.ReadAll(io.LimitReader(r.Body, f.Bytes+1))
		r.Body.Close()
		if e != nil || r.StatusCode != 200 || !rootFileHeaders(r.Header) || int64(len(raw)) != f.Bytes || protectedboot.Hash(raw) != f.SHA256 {
			return fail()
		}
		id, e = c.stopped(ctx, p)
		if e != nil || id != resource {
			return fail()
		}
	}
	for i := range files {
		files[i].Data = nil
	}
	return BootstrapFiles{resource, files}, nil
}
func (c *Client) fileRequest(ctx context.Context, instance, method, path string, body []byte) (*http.Response, error) {
	if c == nil || c.http == nil || !name(instance) {
		return nil, ErrNative
	}
	switch path {
	case "/etc", "/etc/garm-capacity":
		if method != http.MethodHead {
			return nil, ErrNative
		}
	case protectedboot.ManifestPath, protectedboot.JITPath:
	default:
		return nil, ErrNative
	}
	if method != http.MethodHead && method != http.MethodGet && method != http.MethodPost {
		return nil, ErrNative
	}
	u := "http://native.invalid/1.0/instances/" + instance + "/files?" + url.Values{"path": []string{path}}.Encode()
	r, e := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if e != nil {
		return nil, ErrNative
	}
	if method == http.MethodPost {
		if len(body) == 0 || len(body) > 1<<20 {
			return nil, ErrNative
		}
		r.Header.Set("Content-Type", "application/octet-stream")
		for k, v := range map[string]string{"X-Incus-uid": "0", "X-Incus-gid": "0", "X-Incus-mode": "0600", "X-Incus-type": "file", "X-Incus-write": "overwrite"} {
			r.Header.Set(k, v)
		}
	}
	response, e := c.http.Do(r)
	if e != nil {
		return nil, ErrNative
	}
	return response, nil
}
func headerExact(h http.Header, key, want string) bool {
	v := h.Values(key)
	return len(v) == 1 && v[0] == want
}
func rootFileHeaders(h http.Header) bool {
	return headerExact(h, "X-Incus-uid", "0") && headerExact(h, "X-Incus-gid", "0") && headerExact(h, "X-Incus-mode", "0600") && headerExact(h, "X-Incus-type", "file")
}
func rootDirectory(h http.Header) bool {
	if !headerExact(h, "X-Incus-uid", "0") || !headerExact(h, "X-Incus-gid", "0") || !headerExact(h, "X-Incus-type", "directory") {
		return false
	}
	values := h.Values("X-Incus-mode")
	if len(values) != 1 || len(values[0]) != 4 {
		return false
	}
	mode, e := strconv.ParseUint(values[0], 8, 12)
	return e == nil && mode&0022 == 0 && mode&0700 == 0700
}
