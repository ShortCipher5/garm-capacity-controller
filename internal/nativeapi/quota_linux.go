//go:build linux

package nativeapi

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

// StoppedQuota is actual private-native state bracketed around a root ZFS quota
// read. It grants no start permission. The owner still needs the canonical
// one-shot START claim and must preserve sole mutation ownership throughout.
type StoppedQuota struct {
	domain, policy, name, resource string
	disk                           nativequota.Observation
	observed                       time.Time
}

func (q StoppedQuota) Valid() bool {
	return hash(q.domain) && hash(q.policy) && name(q.name) && uuid(q.resource) && q.disk.Valid() && !q.observed.IsZero()
}
func (q StoppedQuota) DomainID() string      { return q.domain }
func (q StoppedQuota) PolicyDigest() string  { return q.policy }
func (q StoppedQuota) Name() string          { return q.name }
func (q StoppedQuota) ResourceID() string    { return q.resource }
func (q StoppedQuota) Dataset() string       { return q.disk.Dataset() }
func (q StoppedQuota) DatasetGUID() string   { return q.disk.GUID() }
func (q StoppedQuota) Bytes() int64          { return q.disk.Bytes() }
func (q StoppedQuota) ObservedAt() time.Time { return q.observed }

// VerifyStoppedQuota deliberately supports only Incus6.0.4 default-project
// filesystem-backed ZFS containers. No snapshot, block-backed or VM inference.
// The caller cannot select the dataset: it comes from the actual private pool
// config and must match the frozen policy's exact ZFS root.
func (c *Client) VerifyStoppedQuota(ctx context.Context, p Prepared, reader *nativequota.Reader) (StoppedQuota, error) {
	fail := func() (StoppedQuota, error) { return StoppedQuota{}, ErrNative }
	if c == nil || reader == nil || p.pool.Type != "container" || p.pool.ZFSDataset == "" || !hash(p.digest) {
		return fail()
	}
	expected, e := c.policy.Compile(p.allocation, preparedUserData(p))
	if e != nil || expected.digest != p.digest {
		return fail()
	}
	if _, e = c.Identity(ctx); e != nil {
		return fail()
	}
	resource, e := c.stopped(ctx, p)
	if e != nil {
		return fail()
	}
	dataset, e := c.dataset(ctx, p)
	if e != nil {
		return fail()
	}
	disk, e := reader.Read(ctx, dataset, p.allocation.Disk*(1<<20))
	if e != nil {
		return fail()
	}
	// A state/config change while reading invalidates the entire observation.
	again, e := c.stopped(ctx, p)
	if e != nil || again != resource {
		return fail()
	}
	after, e := c.dataset(ctx, p)
	if e != nil || after != dataset {
		return fail()
	}
	if _, e = c.Identity(ctx); e != nil {
		return fail()
	}
	return StoppedQuota{c.policy.DomainID(), c.policy.Digest(), p.name, resource, disk, time.Now().UTC()}, nil
}
func preparedUserData(p Prepared) string {
	var b struct {
		Config map[string]string `json:"config"`
	}
	if json.Unmarshal(p.Body(), &b) != nil {
		return ""
	}
	return b.Config["user.user-data"]
}
func syncMetadata(raw []byte) (map[string]json.RawMessage, error) {
	m, e := object(raw)
	if e != nil {
		return nil, e
	}
	t, e := field[string](m, "type")
	if e != nil || t != "sync" {
		return nil, ErrNative
	}
	s, e := field[int](m, "status_code")
	if e != nil || s != 200 {
		return nil, ErrNative
	}
	return object(m["metadata"])
}
func stringMap(raw []byte) (map[string]string, error) {
	m, e := object(raw)
	if e != nil {
		return nil, e
	}
	out := map[string]string{}
	for k := range m {
		v, e := field[string](m, k)
		if e != nil {
			return nil, e
		}
		out[k] = v
	}
	return out, nil
}
func devices(raw []byte) (map[string]map[string]string, error) {
	m, e := object(raw)
	if e != nil {
		return nil, e
	}
	out := map[string]map[string]string{}
	for k, v := range m {
		d, e := stringMap(v)
		if e != nil {
			return nil, e
		}
		out[k] = d
	}
	return out, nil
}
func (c *Client) stopped(ctx context.Context, p Prepared) (string, error) {
	raw, e := c.read(ctx, "/1.0/instances/"+p.name)
	if e != nil {
		return "", e
	}
	return parseStopped(raw, p)
}
func parseStopped(raw []byte, p Prepared) (string, error) { return parseInstance(raw, p, false) }
func parseInstance(raw []byte, p Prepared, running bool) (string, error) {
	state, codeWant := "Stopped", 102
	if running {
		state, codeWant = "Running", 103
	}
	m, e := syncMetadata(raw)
	if e != nil {
		return "", e
	}
	for k, want := range map[string]string{"name": p.name, "type": "container", "status": state} {
		v, e := field[string](m, k)
		if e != nil || v != want {
			return "", ErrNative
		}
	}
	code, e := field[int](m, "status_code")
	if e != nil || code != codeWant {
		return "", ErrNative
	}
	profiles, e := field[[]string](m, "profiles")
	if e != nil || len(profiles) != 0 {
		return "", ErrNative
	}
	var body struct {
		Config  map[string]string            `json:"config"`
		Devices map[string]map[string]string `json:"devices"`
	}
	if json.Unmarshal(p.Body(), &body) != nil {
		return "", ErrNative
	}
	config, e := stringMap(m["config"])
	if e != nil {
		return "", e
	}
	expanded, e := stringMap(m["expanded_config"])
	if e != nil || !reflect.DeepEqual(config, expanded) {
		return "", ErrNative
	}
	for k, v := range body.Config {
		if config[k] != v {
			return "", ErrNative
		}
	}
	for k := range config {
		if _, ok := body.Config[k]; !ok && !strings.HasPrefix(k, "volatile.") && !strings.HasPrefix(k, "image.") {
			return "", ErrNative
		}
	}
	for _, k := range []string{"devices", "expanded_devices"} {
		actual, e := devices(m[k])
		if e != nil || !reflect.DeepEqual(actual, body.Devices) {
			return "", ErrNative
		}
	}
	if config["volatile.base_image"] != p.allocation.Fingerprint {
		return "", ErrNative
	}
	id := config["volatile.uuid"]
	if !uuid(id) {
		return "", ErrNative
	}
	return id, nil
}
func (c *Client) dataset(ctx context.Context, p Prepared) (string, error) {
	raw, e := c.read(ctx, "/1.0/storage-pools/"+p.pool.StoragePool)
	if e != nil {
		return "", e
	}
	m, e := syncMetadata(raw)
	if e != nil {
		return "", e
	}
	for k, want := range map[string]string{"name": p.pool.StoragePool, "driver": "zfs", "status": "Created"} {
		v, e := field[string](m, k)
		if e != nil || v != want {
			return "", ErrNative
		}
	}
	config, e := stringMap(m["config"])
	if e != nil || config["zfs.pool_name"] != p.pool.ZFSDataset || (config["volume.zfs.block_mode"] != "" && config["volume.zfs.block_mode"] != "false") {
		return "", ErrNative
	}
	raw, e = c.read(ctx, "/1.0/storage-pools/"+p.pool.StoragePool+"/volumes/container/"+p.name)
	if e != nil {
		return "", e
	}
	m, e = syncMetadata(raw)
	if e != nil {
		return "", e
	}
	for k, want := range map[string]string{"name": p.name, "type": "container", "content_type": "filesystem"} {
		v, e := field[string](m, k)
		if e != nil || v != want {
			return "", ErrNative
		}
	}
	config, e = stringMap(m["config"])
	if e != nil || (config["zfs.block_mode"] != "" && config["zfs.block_mode"] != "false") {
		return "", ErrNative
	}
	// Incus6.0.4 driver_zfs_utils.dataset uses zfs.pool_name/containers/name;
	// the default project has no instance-name prefix (project.Instance).
	return p.pool.ZFSDataset + "/containers/" + p.name, nil
}
func uuid(s string) bool {
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
	return s != "00000000-0000-0000-0000-000000000000"
}

// ObserveResource validates the exact compiled instance state through the private domain.
func (c *Client) ObserveResource(ctx context.Context, p Prepared, running bool) (string, error) {
	if !c.matches(p) {
		return "", ErrNative
	}
	raw, e := c.read(ctx, "/1.0/instances/"+p.name)
	if e != nil {
		return "", e
	}
	return parseInstance(raw, p, running)
}
