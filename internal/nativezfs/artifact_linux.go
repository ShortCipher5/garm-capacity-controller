//go:build linux

package nativezfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ArtifactRegistry is a host-global permanent fresh-setup latch. It must never
// be cleared to recover an uncertain installation or reset UID-range history.
const ArtifactRegistry = "/var/lib/garm-capacity/host-artifact-birth"

// Short fixed mount path leaves room for birth-scoped Unix socket addresses.
const ArtifactMount = "/run/garm-ctl"

type ArtifactConfiguration struct {
	Binary, BinarySHA256, Helper, HelperSHA256, ParentDataset, ParentGUID string
	QuotaBytes                                                            int64
	Image, ImageSHA256                                                    string
}
type artifactRecord struct {
	Version                                                                    int
	ID, Dataset, GUID, ParentDataset, ParentGUID, Pool, PoolGUID, PoolLoadGUID string
	QuotaBytes                                                                 int64
	ImageSHA256                                                                string
	ImageBytes                                                                 int64
}
type artifactState struct {
	mu      sync.Mutex
	failed  bool
	root    *os.File
	mountID uint64
	image   unix.Stat_t
}

// HostArtifactRoot cannot be reconstructed from configuration or disk records.
// It bounds one host's shared registries and copied runtime image, not external
// deployment originals, TLS files, bpffs kernel objects or admission authority.
type HostArtifactRoot struct {
	config ArtifactConfiguration
	record artifactRecord
	state  *artifactState
	pools  *nativequota.PoolReader
}
type ArtifactPaths struct{ Image, IDs, State, ZFS, Egress, Devices string }

func (b HostArtifactRoot) Paths(ctx context.Context) (ArtifactPaths, error) {
	if _, e := b.Observe(ctx); e != nil {
		return ArtifactPaths{}, e
	}
	if b.state == nil {
		return ArtifactPaths{}, ErrRefused
	}
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	if b.state.failed {
		return ArtifactPaths{}, ErrUnknown
	}
	root := ArtifactMount
	return ArtifactPaths{filepath.Join(root, "images/runtime.raw"), filepath.Join(root, "ids"), filepath.Join(root, "state"), filepath.Join(root, "zfs"), filepath.Join(root, "egress"), filepath.Join(root, "devices")}, nil
}

// ArtifactRootObservation contains actual local readback only. A host label is
// deliberately absent; enrollment must join the authenticated actual owner.
type ArtifactRootObservation struct {
	record artifactRecord
	at     time.Time
}

func (o ArtifactRootObservation) Fresh() bool {
	age := time.Since(o.at)
	return !o.at.IsZero() && age >= 0 && age <= 5*time.Second
}
func (o ArtifactRootObservation) RootGUID() string      { return o.record.GUID }
func (o ArtifactRootObservation) Dataset() string       { return o.record.Dataset }
func (o ArtifactRootObservation) PoolGUID() string      { return o.record.PoolGUID }
func (o ArtifactRootObservation) PoolLoadGUID() string  { return o.record.PoolLoadGUID }
func (o ArtifactRootObservation) QuotaBytes() int64     { return o.record.QuotaBytes }
func (o ArtifactRootObservation) ObservedAt() time.Time { return o.at.UTC() }
func (o ArtifactRootObservation) ID() string            { return o.record.ID }

// ValidateArtifactConfiguration checks input shape only and mints no live root.
func ValidateArtifactConfiguration(c ArtifactConfiguration) error {
	if !validArtifactConfig(c) || !birthID(c.BinarySHA256) || !birthID(c.HelperSHA256) {
		return ErrRefused
	}
	for _, path := range []string{c.Binary, c.Helper} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return ErrRefused
		}
	}
	return nil
}
func validArtifactConfig(c ArtifactConfiguration) bool {
	return dataset(c.ParentDataset) && number(c.ParentGUID) && c.QuotaBytes > 0 && birthID(c.ImageSHA256) && filepath.IsAbs(c.Image) && filepath.Clean(c.Image) == c.Image
}
func artifactProperties(p map[string]string, r artifactRecord) bool {
	return p["guid"] == r.GUID && p["type"] == "filesystem" && p["quota"] == strconv.FormatInt(r.QuotaBytes, 10) && p["zoned"] == "off" && p["mountpoint"] == "legacy" && p["canmount"] == "noauto"
}

// CreateHostArtifactRoot is fresh setup only. It never adopts directories,
// imports UID history, delegates datasets, deletes or rolls back partial work.
func CreateHostArtifactRoot(ctx context.Context, c ArtifactConfiguration, pools *nativequota.PoolReader) (HostArtifactRoot, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || ValidateArtifactConfiguration(c) != nil || pools == nil || securePath(ArtifactRegistry, true) != nil {
		return HostArtifactRoot{}, ErrRefused
	}
	st, e := os.Stat(ArtifactRegistry)
	if e != nil || st.Mode().Perm() != 0700 {
		return HostArtifactRoot{}, ErrRefused
	}
	binary, e := openBinary(c.Binary, c.BinarySHA256)
	if e != nil {
		return HostArtifactRoot{}, e
	}
	defer binary.Close()
	helper, e := openBinary(c.Helper, c.HelperSHA256)
	if e != nil {
		return HostArtifactRoot{}, e
	}
	defer helper.Close()
	ns, e := os.Open("/proc/self/ns/user")
	if e != nil {
		return HostArtifactRoot{}, ErrRefused
	}
	defer ns.Close()
	cmd := systemCommands{binary, helper}
	version, e := cmd.run(ctx, []string{"version"}, ns)
	if e != nil || !supportedVersion(version) {
		return HostArtifactRoot{}, ErrRefused
	}
	parent, e := read(ctx, cmd, c.ParentDataset, ns)
	if e != nil || parent["guid"] != c.ParentGUID || parent["type"] != "filesystem" || parent["zoned"] != "off" {
		return HostArtifactRoot{}, ErrRefused
	}
	poolName := strings.Split(c.ParentDataset, "/")[0]
	pool, e := pools.Read(ctx, poolName)
	if e != nil {
		return HostArtifactRoot{}, ErrRefused
	}
	id := make([]byte, 32)
	if _, e = rand.Read(id); e != nil {
		return HostArtifactRoot{}, ErrRefused
	}
	r := artifactRecord{Version: 1, ID: hex.EncodeToString(id), ParentDataset: c.ParentDataset, ParentGUID: c.ParentGUID, Pool: poolName, PoolGUID: pool.GUID(), PoolLoadGUID: pool.LoadGUID(), QuotaBytes: c.QuotaBytes, ImageSHA256: c.ImageSHA256}
	r.Dataset = c.ParentDataset + "/control-" + r.ID
	if !dataset(r.Dataset) || writeArtifact("attempt.json", r) != nil {
		return HostArtifactRoot{}, ErrUnknown
	}
	// The durable global latch precedes every dataset or mount effect.
	if _, e = cmd.run(ctx, []string{"create", "-u", "-o", "quota=" + strconv.FormatInt(c.QuotaBytes, 10), "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "zoned=off", r.Dataset}, ns); e != nil {
		return HostArtifactRoot{}, ErrUnknown
	}
	props, e := read(ctx, cmd, r.Dataset, ns)
	if e != nil || !number(props["guid"]) {
		return HostArtifactRoot{}, ErrUnknown
	}
	r.GUID = props["guid"]
	if r.GUID == r.ParentGUID || !artifactProperties(props, r) {
		return HostArtifactRoot{}, ErrUnknown
	}
	state, size, e := mountArtifact(ctx, c, r)
	if e != nil {
		return HostArtifactRoot{}, ErrUnknown
	}
	r.ImageBytes = size
	b := HostArtifactRoot{c, r, state, pools}
	if _, e = b.Observe(ctx); e != nil {
		return HostArtifactRoot{}, ErrUnknown
	}
	if writeArtifact("complete.json", r) != nil {
		state.failed = true
		return HostArtifactRoot{}, ErrUnknown
	}
	return b, nil
}
func writeArtifact(name string, r artifactRecord) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return ErrUnknown
	}
	f, e := os.OpenFile(filepath.Join(ArtifactRegistry, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrUnknown
	}
	n, e := f.Write(raw)
	se := f.Sync()
	ce := f.Close()
	if e != nil || n != len(raw) || se != nil || ce != nil {
		return ErrUnknown
	}
	return syncDirectory(ArtifactRegistry)
}
func (b HostArtifactRoot) Observe(ctx context.Context) (out ArtifactRootObservation, err error) {
	if b.state == nil {
		return out, ErrRefused
	}
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return out, ErrUnknown
	}
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || s.root == nil || b.pools == nil {
		return out, ErrRefused
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binary, e := openBinary(b.config.Binary, b.config.BinarySHA256)
	if e != nil {
		return out, e
	}
	defer binary.Close()
	helper, e := openBinary(b.config.Helper, b.config.HelperSHA256)
	if e != nil {
		return out, e
	}
	defer helper.Close()
	ns, e := os.Open("/proc/self/ns/user")
	if e != nil {
		return out, ErrRefused
	}
	defer ns.Close()
	cmd := systemCommands{binary, helper}
	parent, e := read(ctx, cmd, b.record.ParentDataset, ns)
	if e != nil || parent["guid"] != b.record.ParentGUID || parent["zoned"] != "off" {
		return out, ErrUnknown
	}
	props, e := read(ctx, cmd, b.record.Dataset, ns)
	if e != nil || !artifactProperties(props, b.record) {
		return out, ErrUnknown
	}
	for _, prop := range []string{"quota", "zoned"} {
		raw, e := cmd.run(ctx, []string{"get", "-H", "-p", "-o", "value,source", prop, b.record.Dataset}, ns)
		want := "off"
		if prop == "quota" {
			want = strconv.FormatInt(b.record.QuotaBytes, 10)
		}
		if e != nil || string(raw) != want+"\tlocal\n" {
			return out, ErrUnknown
		}
	}
	if checkArtifactMount(s, b.record) != nil {
		return out, ErrUnknown
	}
	pool, e := b.pools.Read(ctx, b.record.Pool)
	if e != nil || pool.GUID() != b.record.PoolGUID || pool.LoadGUID() != b.record.PoolLoadGUID || ctx.Err() != nil || time.Since(began) > 5*time.Second {
		return out, ErrUnknown
	}
	return ArtifactRootObservation{b.record, began}, nil
}

func (b HostArtifactRoot) ImageSHA256() string { return b.record.ImageSHA256 }

// Close drops the retained descriptor and permanently invalidates every copy.
// It never unmounts, destroys datasets, resets the global latch or releases debt.
func (b HostArtifactRoot) Close() {
	if b.state == nil {
		return
	}
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	b.state.failed = true
	if b.state.root != nil {
		b.state.root.Close()
		b.state.root = nil
	}
}
