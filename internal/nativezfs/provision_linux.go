//go:build linux

// Package nativezfs creates disposable datasets below a host-owned quota.
// No dataset is adopted or destroyed. Uncertain births are permanently consumed.
package nativezfs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/capsulepeer"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrRefused = errors.New("fresh native ZFS birth refused")
var ErrUnknown = errors.New("native ZFS birth consumed; outcome unknown; no retry or cleanup")

type Configuration struct {
	Binary, BinarySHA256, Helper, HelperSHA256, Registry, RootDataset, RootGUID string
	QuotaBytes                                                                  int64
}
type Record struct {
	MappedUID     uint32    `json:"mapped_uid"`
	MappedGID     uint32    `json:"mapped_gid"`
	HelperSHA256  string    `json:"helper_sha256"`
	BinarySHA256  string    `json:"binary_sha256"`
	Version       int       `json:"version"`
	CapsuleID     string    `json:"capsule_id"`
	UserNamespace uint64    `json:"user_namespace"`
	RootDataset   string    `json:"root_dataset"`
	RootGUID      string    `json:"root_guid"`
	Dataset       string    `json:"dataset"`
	GUID          string    `json:"guid"`
	RuntimeGUID   string    `json:"runtime_guid"`
	PoolGUID      string    `json:"pool_guid"`
	QuotaBytes    int64     `json:"quota_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// Birth is not physical capacity admission or proof that Incus is safe to run.
// Only the live capsule entrypoint may return it after durable completion.
type Birth struct {
	record Record
	config Configuration
	peer   *capsulepeer.Peer
}

func (b Birth) Record() Record { return b.record }

type commands interface {
	run(context.Context, []string, *os.File) ([]byte, error)
}
type birthJournal interface {
	consume(Record) error
	complete(Record) error
}

func validConfig(c Configuration) bool {
	return dataset(c.RootDataset) && number(c.RootGUID) && c.QuotaBytes > 0
}
func dataset(s string) bool {
	if len(s) == 0 || len(s) > 240 || !strings.Contains(s, "/") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for i, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-' || r == '.')) {
				return false
			}
		}
	}
	return true
}
func number(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == s
}
func birthID(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == s
}

// provision is private: its production caller must hold a genuine capsule peer
// and namespace descriptor for the entire operation, checking liveness before
// and after. A string or namespace inode supplied by a caller is insufficient.
func provision(ctx context.Context, c Configuration, id string, ns uint64, namespace *os.File, cmd commands, journal birthJournal, uid, gid uint32, initialize func(Record) error) (Record, error) {
	if ctx == nil || ctx.Err() != nil || !validConfig(c) || !birthID(id) || ns == 0 || namespace == nil || cmd == nil || journal == nil || uid == 0 || gid == 0 || initialize == nil {
		return Record{}, ErrRefused
	}
	parent := c.RootDataset + "/domain-" + id
	if !dataset(parent) || !dataset(parent+"/runtime") || !dataset(parent+"/pool") {
		return Record{}, ErrRefused
	}
	// Existing trusted root must still be the exact host dataset, not a renamed
	// replacement or a delegation into another namespace.
	root, e := read(ctx, cmd, c.RootDataset, namespace)
	if e != nil || root["guid"] != c.RootGUID || root["type"] != "filesystem" || root["zoned"] != "off" {
		return Record{}, ErrRefused
	}
	r := Record{MappedUID: uid, MappedGID: gid, HelperSHA256: c.HelperSHA256, BinarySHA256: c.BinarySHA256, Version: 1, CapsuleID: id, UserNamespace: ns, RootDataset: c.RootDataset, RootGUID: c.RootGUID, Dataset: parent, QuotaBytes: c.QuotaBytes, CreatedAt: time.Now().UTC()}
	if journal.consume(r) != nil {
		return Record{}, ErrUnknown
	}
	// No -p: the selected parent must exist and the exact target must be fresh.
	if _, e = cmd.run(ctx, []string{"create", "-u", "-o", "quota=" + strconv.FormatInt(c.QuotaBytes, 10), "-o", "mountpoint=none", "-o", "canmount=off", "-o", "zoned=off", parent}, namespace); e != nil {
		return Record{}, ErrUnknown
	}
	props, e := read(ctx, cmd, parent, namespace)
	if e != nil || !parentProperties(props, c.QuotaBytes, "off") {
		return Record{}, ErrUnknown
	}
	r.GUID = props["guid"]
	for _, child := range []string{"runtime", "pool"} {
		target := parent + "/" + child
		if _, e = cmd.run(ctx, []string{"create", "-u", "-o", "mountpoint=legacy", "-o", "canmount=noauto", target}, namespace); e != nil {
			return Record{}, ErrUnknown
		}
		p, e := read(ctx, cmd, target, namespace)
		if e != nil || !number(p["guid"]) || p["type"] != "filesystem" || p["zoned"] != "off" || p["mountpoint"] != "legacy" || p["canmount"] != "noauto" || p["quota"] != "0" || p["guid"] == r.GUID {
			return Record{}, ErrUnknown
		}
		if child == "runtime" {
			r.RuntimeGUID = p["guid"]
		} else {
			r.PoolGUID = p["guid"]
		}
	}
	if r.RuntimeGUID == r.PoolGUID {
		return Record{}, ErrUnknown
	}
	if initialize(r) != nil {
		return Record{}, ErrUnknown
	}
	if _, e = cmd.run(ctx, []string{"set", "zoned=on", parent}, namespace); e != nil {
		return Record{}, ErrUnknown
	}
	// Only the inherited held descriptor is opened by zfs. No PID pathname race.
	if _, e = cmd.run(ctx, []string{"zone", "/proc/self/fd/4", parent}, namespace); e != nil {
		return Record{}, ErrUnknown
	}
	props, e = read(ctx, cmd, parent, namespace)
	if e != nil || props["guid"] != r.GUID || !parentProperties(props, c.QuotaBytes, "on") {
		return Record{}, ErrUnknown
	}
	root, e = read(ctx, cmd, c.RootDataset, namespace)
	if e != nil || root["guid"] != c.RootGUID || root["zoned"] != "off" || root["type"] != "filesystem" {
		return Record{}, ErrUnknown
	}
	return r, nil
}
func parentProperties(p map[string]string, q int64, zoned string) bool {
	return number(p["guid"]) && p["type"] == "filesystem" && p["quota"] == strconv.FormatInt(q, 10) && p["zoned"] == zoned && p["mountpoint"] == "none" && p["canmount"] == "off"
}

var properties = []string{"guid", "type", "quota", "zoned", "mountpoint", "canmount"}

func read(ctx context.Context, c commands, target string, ns *os.File) (map[string]string, error) {
	raw, e := c.run(ctx, []string{"get", "-H", "-p", "-o", "name,property,value", strings.Join(properties, ","), target}, ns)
	if e != nil {
		return nil, e
	}
	return parseProperties(raw, target)
}
func parseProperties(raw []byte, target string) (map[string]string, error) {
	if len(raw) > 8192 || !strings.HasSuffix(string(raw), "\n") {
		return nil, ErrRefused
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != len(properties) {
		return nil, ErrRefused
	}
	out := map[string]string{}
	for _, line := range lines {
		v := strings.Split(line, "\t")
		if len(v) != 3 || v[0] != target || v[2] == "" {
			return nil, ErrRefused
		}
		if _, ok := out[v[1]]; ok {
			return nil, ErrRefused
		}
		out[v[1]] = v[2]
	}
	for _, k := range properties {
		if _, ok := out[k]; !ok {
			return nil, ErrRefused
		}
	}
	return out, nil
}
func recordBytes(r Record) []byte { raw, _ := json.Marshal(r); return raw }
