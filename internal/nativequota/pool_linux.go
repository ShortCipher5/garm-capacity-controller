//go:build linux

package nativequota

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// PoolReader uses a separately pinned zpool executable, never a caller command.
type PoolReader struct{ config Configuration }

func NewPoolReader(c Configuration) (*PoolReader, error) {
	if _, e := New(c); e != nil {
		return nil, e
	}
	return &PoolReader{c}, nil
}
func validPool(s string) bool { return !strings.Contains(s, "/") && validDataset("pool/"+s) }

const poolProperties = "guid,load_guid,health,readonly,freeing,leaked,allocated,free,size"

type PoolAccounting struct {
	pool, guid, loadGUID                   string
	freeing, leaked, allocated, free, size uint64
	at                                     time.Time
}

func (o PoolAccounting) Pool() string          { return o.pool }
func (o PoolAccounting) GUID() string          { return o.guid }
func (o PoolAccounting) LoadGUID() string      { return o.loadGUID }
func (o PoolAccounting) Freeing() uint64       { return o.freeing }
func (o PoolAccounting) Allocated() uint64     { return o.allocated }
func (o PoolAccounting) Free() uint64          { return o.free }
func (o PoolAccounting) Size() uint64          { return o.size }
func (o PoolAccounting) ObservedAt() time.Time { return o.at }
func parsePool(raw []byte, pool string) (PoolAccounting, error) {
	if !validPool(pool) || len(raw) > 4096 || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return PoolAccounting{}, ErrQuota
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] != pool || values[f[1]] != "" {
			return PoolAccounting{}, ErrQuota
		}
		values[f[1]] = f[2]
	}
	if len(values) != 9 || values["health"] != "ONLINE" || values["readonly"] != "off" {
		return PoolAccounting{}, ErrQuota
	}
	nums := map[string]uint64{}
	for _, k := range []string{"guid", "load_guid", "freeing", "leaked", "allocated", "free", "size"} {
		v, e := strconv.ParseUint(values[k], 10, 64)
		if e != nil || strconv.FormatUint(v, 10) != values[k] {
			return PoolAccounting{}, ErrQuota
		}
		nums[k] = v
	}
	if nums["guid"] == 0 || nums["load_guid"] == 0 || nums["size"] == 0 || nums["allocated"] > nums["size"] || nums["free"] > nums["size"]-nums["allocated"] || nums["leaked"] != 0 {
		return PoolAccounting{}, ErrQuota
	}
	return PoolAccounting{pool, values["guid"], values["load_guid"], nums["freeing"], nums["leaked"], nums["allocated"], nums["free"], nums["size"], time.Now().UTC()}, nil
}
func (r *PoolReader) run(ctx context.Context, pool string, wait bool) ([]byte, error) {
	if r == nil || ctx == nil || os.Geteuid() != 0 || !validPool(pool) {
		return nil, ErrQuota
	}
	binary, e := pinned(r.config.Binary, r.config.BinarySHA256)
	if e != nil {
		return nil, e
	}
	defer binary.Close()
	helper, e := pinned(r.config.Helper, r.config.HelperSHA256)
	if e != nil {
		return nil, e
	}
	defer helper.Close()
	timeout := 10 * time.Second
	mode := "--pool-accounting"
	if wait {
		timeout = 30 * time.Second
		mode = "--pool-wait-free"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", mode, pool)
	cmd.ExtraFiles = []*os.File{binary, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || ctx.Err() != nil {
		return nil, ErrQuota
	}
	if wait && out.Len() != 0 {
		return nil, ErrQuota
	}
	return out.Bytes(), nil
}
func (r *PoolReader) Read(ctx context.Context, pool string) (PoolAccounting, error) {
	raw, e := r.run(ctx, pool, false)
	if e != nil {
		return PoolAccounting{}, e
	}
	return parsePool(raw, pool)
}
func samePool(a, b PoolAccounting) bool {
	return a.pool == b.pool && a.guid != "" && a.guid == b.guid && a.loadGUID == b.loadGUID && a.size == b.size
}

// ReclamationTracker retains original subtree GUIDs and the full-pool reference
// baseline. It never assumes that a missing dataset name means freed capacity.
type ReclamationTracker struct {
	life       DatasetLifetime
	poolReader *PoolReader
	pool       PoolAccounting
	root       FilesystemIdentity
	targets    map[string]bool
	references map[string]inventoryIdentity
}
type ReclamationObservation struct {
	root     RootAccounting
	identity DatasetIdentityAbsence
	pool     PoolAccounting
	at       time.Time
}

func (o ReclamationObservation) Identity() DatasetIdentityAbsence { return o.identity }
func (o ReclamationObservation) PoolAccounting() PoolAccounting   { return o.pool }
func (o ReclamationObservation) ObservedAt() time.Time            { return o.at.UTC() }

// Fresh uses the retained sampling-start clock, including its monotonic value.
// Call again at the evidence join; transport timestamps cannot recreate this.
func (o ReclamationObservation) Fresh() bool {
	age := time.Since(o.at)
	return !o.at.IsZero() && age >= 0 && age <= 5*time.Second
}
func referenceBaseline(items []inventoryIdentity) map[string]inventoryIdentity {
	m := map[string]inventoryIdentity{}
	for _, v := range items {
		if v.kind == "snapshot" || v.kind == "bookmark" || v.origin != "-" {
			m[v.name] = v
		}
	}
	return m
}
func (l DatasetLifetime) CaptureReclamation(ctx context.Context, p *PoolReader) (ReclamationTracker, error) {
	if l.reader == nil || p == nil || ctx == nil || !l.original.Valid() {
		return ReclamationTracker{}, ErrQuota
	}
	poolName := strings.Split(l.original.dataset, "/")[0]
	account, e := p.Read(ctx, poolName)
	if e != nil {
		return ReclamationTracker{}, ErrQuota
	}
	raw, e := l.reader.readInventory(ctx, poolName)
	if e != nil {
		return ReclamationTracker{}, ErrQuota
	}
	items, e := parseIdentityInventory(raw, FilesystemIdentity{dataset: poolName})
	if e != nil {
		return ReclamationTracker{}, ErrQuota
	}
	var root FilesystemIdentity
	targets := map[string]bool{}
	originalFound := false
	for _, v := range items {
		if v.name == poolName {
			root = FilesystemIdentity{dataset: poolName, guid: v.guid}
		}
		if v.name == l.original.dataset {
			if v.guid != l.original.guid {
				return ReclamationTracker{}, ErrQuota
			}
			originalFound = true
		}
		if v.name == l.original.dataset || strings.HasPrefix(v.name, l.original.dataset+"/") || strings.HasPrefix(v.name, l.original.dataset+"@") || strings.HasPrefix(v.name, l.original.dataset+"#") {
			targets[v.guid] = true
		}
	}
	if !originalFound || root.guid == "" {
		return ReclamationTracker{}, ErrQuota
	}
	after, e := p.Read(ctx, poolName)
	if e != nil || !samePool(account, after) {
		return ReclamationTracker{}, ErrQuota
	}
	current, e := l.reader.Read(ctx, l.original.dataset, l.original.bytes)
	if e != nil || current.guid != l.original.guid {
		return ReclamationTracker{}, ErrQuota
	}
	return ReclamationTracker{l, p, account, root, targets, referenceBaseline(items)}, nil
}
func (t ReclamationTracker) referencesGone(items []inventoryIdentity) bool {
	if !identityGone(items, t.life.original) {
		return false
	}
	for _, v := range items {
		if t.targets[v.guid] {
			return false
		}
		if v.deferDestroy == "on" || v.userrefs != "-" && v.userrefs != "0" {
			return false
		}
		if v.kind == "snapshot" || v.kind == "bookmark" || v.origin != "-" {
			old, ok := t.references[v.name]
			if !ok || old != v {
				return false
			}
		}
	}
	return true
}

// ObserveReclamation waits only for actual pool background freeing, then requires
// same loaded pool identity, zero pending/leaked bytes and no retained original
// GUIDs or new/changed reference graph. It reports accounting, NOT a release bool.
// Concurrent reference-changing workloads or oversized inventories fail closed.
func (t ReclamationTracker) ObserveReclamation(ctx context.Context) (ReclamationObservation, error) {
	if t.life.reader == nil || t.poolReader == nil || ctx == nil || len(t.targets) == 0 {
		return ReclamationObservation{}, ErrQuota
	}
	before, e := t.poolReader.Read(ctx, t.pool.pool)
	if e != nil || !samePool(t.pool, before) {
		return ReclamationObservation{}, ErrQuota
	}
	if _, e = t.poolReader.run(ctx, t.pool.pool, true); e != nil {
		return ReclamationObservation{}, ErrQuota
	}
	// Background freeing can take longer; only subsequent evidence belongs
	// to the fresh bounded accounting window. Preserve its START timestamp.
	sampledAt := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, e := t.life.reader.readInventory(ctx, t.pool.pool)
	if e != nil {
		return ReclamationObservation{}, ErrQuota
	}
	items, e := parseIdentityInventory(raw, t.root)
	if e != nil || !t.referencesGone(items) {
		return ReclamationObservation{}, ErrQuota
	}
	absence, e := t.life.ObserveIdentityAbsence(ctx)
	if e != nil {
		return ReclamationObservation{}, ErrQuota
	}
	after, e := t.poolReader.Read(ctx, t.pool.pool)
	if e != nil || !samePool(t.pool, after) || after.freeing != 0 {
		return ReclamationObservation{}, ErrQuota
	}
	root, e := t.life.reader.readRootAccounting(ctx, t.life.root)
	if e != nil {
		return ReclamationObservation{}, ErrQuota
	}
	final, e := t.poolReader.Read(ctx, t.pool.pool)
	if e != nil || !samePool(t.pool, final) || final.freeing != 0 {
		return ReclamationObservation{}, ErrQuota
	}
	if ctx.Err() != nil || time.Since(sampledAt) > 5*time.Second {
		return ReclamationObservation{}, ErrQuota
	}
	return ReclamationObservation{root, absence, final, sampledAt}, nil
}

func (o ReclamationObservation) RootAccounting() RootAccounting { return o.root }
