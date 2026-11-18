//go:build linux

package nativezfs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// CategoryBudgets assigns independent maxima, including retained deleted images.
// Runtime includes Incus metadata, logs and temporary files; it is not a
// separate log limit. Every fixed stock category must have a positive limit.
type CategoryBudgets struct {
	Runtime    int64
	Categories map[string]int64
}

var sealedCategories = []string{"buckets", "containers", "custom", "images", "virtual-machines", "deleted/buckets", "deleted/containers", "deleted/custom", "deleted/images", "deleted/virtual-machines"}

type sealedRoot struct {
	Name, GUID string
	Bytes      int64
}
type sealRecord struct {
	Version int
	Birth   Record
	Roots   []sealedRoot
}

// SealedBudgets is an actual fresh-domain transition, not allocation authority.
// No JSON constructor or uncertain-operation retry exists.
type SealedBudgets struct {
	birth  Birth
	record sealRecord
	state  *sealState
}
type sealState struct {
	mu     sync.Mutex
	failed bool
}

func (s *sealState) check(fn func() error) error {
	if s == nil {
		return ErrRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return ErrUnknown
	}
	e := fn()
	if e != nil {
		s.failed = true
	}
	return e
}

func (s SealedBudgets) RuntimeBytes() int64 {
	if len(s.record.Roots) == 0 {
		return 0
	}
	return s.record.Roots[0].Bytes
}

// ValidateCategoryBudgets validates configuration shape and arithmetic only.
// It creates no birth, identity, readback or admission capability.
func ValidateCategoryBudgets(total int64, b CategoryBudgets) error {
	if total <= 0 || b.Runtime <= 0 || b.Runtime > total || len(b.Categories) != len(sealedCategories) {
		return ErrRefused
	}
	left := total - b.Runtime
	for _, name := range sealedCategories {
		q := b.Categories[name]
		if q <= 0 || q > left {
			return ErrRefused
		}
		left -= q
	}
	return nil
}
func budgetRoots(r Record, b CategoryBudgets) ([]sealedRoot, error) {
	if e := ValidateCategoryBudgets(r.QuotaBytes, b); e != nil {
		return nil, e
	}
	roots := []sealedRoot{{Name: r.Dataset + "/runtime", GUID: r.RuntimeGUID, Bytes: b.Runtime}}
	for _, name := range sealedCategories {
		roots = append(roots, sealedRoot{Name: r.Dataset + "/pool/" + name, Bytes: b.Categories[name]})
	}
	return roots, nil
}

// SealBudgets removes broad parent delegation only after the trusted stock
// bootstrap has created all fixed category roots. Invoke before any guests.
// Any partial outcome permanently consumes this transition; no rollback or
// adoption is attempted. Namespace membership follows successful fixed kernel
// zone/unzone commands, not a property-only inference.
func (b Birth) SealBudgets(ctx context.Context, budgets CategoryBudgets) (SealedBudgets, error) {
	if b.Recheck(ctx) != nil {
		return SealedBudgets{}, ErrRefused
	}
	roots, e := budgetRoots(b.record, budgets)
	if e != nil {
		return SealedBudgets{}, e
	}
	ns, e := b.peer.UserNamespace()
	if e != nil {
		return SealedBudgets{}, ErrRefused
	}
	defer ns.Close()
	binary, e := openBinary(b.config.Binary, b.config.BinarySHA256)
	if e != nil {
		return SealedBudgets{}, e
	}
	defer binary.Close()
	helper, e := openBinary(b.config.Helper, b.config.HelperSHA256)
	if e != nil {
		return SealedBudgets{}, e
	}
	defer helper.Close()
	directory := filepath.Join(b.config.Registry, b.record.CapsuleID)
	if securePath(directory, true) != nil {
		return SealedBudgets{}, ErrRefused
	}
	record := sealRecord{Version: 1, Birth: b.record, Roots: roots}
	cmd := systemCommands{binary, helper}
	record, e = seal(ctx, record, ns, cmd, func(r sealRecord) error { return writeSeal(directory, "seal-intent.json", r) })
	if e != nil || !b.peer.Alive() {
		return SealedBudgets{}, ErrUnknown
	}
	if writeSeal(directory, "seal-complete.json", record) != nil {
		return SealedBudgets{}, ErrUnknown
	}
	return SealedBudgets{birth: b, record: record, state: &sealState{}}, nil
}
func writeSeal(directory, name string, r sealRecord) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return ErrUnknown
	}
	f, e := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrUnknown
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrUnknown
	}
	return syncDirectory(directory)
}
func seal(ctx context.Context, r sealRecord, ns *os.File, cmd commands, consume func(sealRecord) error) (sealRecord, error) {
	if ctx == nil || ctx.Err() != nil || ns == nil || cmd == nil || consume == nil || len(r.Roots) != 1+len(sealedCategories) {
		return sealRecord{}, ErrRefused
	}
	if checkBirth(ctx, r.Birth, ns, cmd) != nil {
		return sealRecord{}, ErrRefused
	}
	seen := map[string]bool{r.Birth.GUID: true, r.Birth.PoolGUID: true, r.Birth.RootGUID: true}
	for i := range r.Roots {
		root := &r.Roots[i]
		p, e := read(ctx, cmd, root.Name, ns)
		if e != nil || p["type"] != "filesystem" || !number(p["guid"]) || seen[p["guid"]] || root.GUID != "" && root.GUID != p["guid"] {
			return sealRecord{}, ErrRefused
		}
		root.GUID = p["guid"]
		seen[root.GUID] = true
	}
	if consume(r) != nil {
		return sealRecord{}, ErrUnknown
	}
	// Set local zoned roots before quotas. Parent remains bounded throughout.
	for _, root := range r.Roots {
		for _, args := range [][]string{{"set", "zoned=on", root.Name}, {"set", "quota=" + strconv.FormatInt(root.Bytes, 10), root.Name}} {
			if _, e := cmd.run(ctx, args, ns); e != nil {
				return sealRecord{}, ErrUnknown
			}
		}
	}
	// Remove broad authority FIRST. Never attach pool or deleted ancestors.
	if _, e := cmd.run(ctx, []string{"unzone", "/proc/self/fd/4", r.Birth.Dataset}, ns); e != nil {
		return sealRecord{}, ErrUnknown
	}
	for _, root := range r.Roots {
		if _, e := cmd.run(ctx, []string{"zone", "/proc/self/fd/4", root.Name}, ns); e != nil {
			return sealRecord{}, ErrUnknown
		}
	}
	if checkSeal(ctx, r, ns, cmd) != nil {
		return sealRecord{}, ErrUnknown
	}
	return r, nil
}
func checkSeal(ctx context.Context, r sealRecord, ns *os.File, cmd commands) error {
	original, e := read(ctx, cmd, r.Birth.RootDataset, ns)
	if e != nil || original["guid"] != r.Birth.RootGUID || original["type"] != "filesystem" || original["zoned"] != "off" {
		return ErrUnknown
	}
	pool, e := read(ctx, cmd, r.Birth.Dataset+"/pool", ns)
	if e != nil || pool["guid"] != r.Birth.PoolGUID || pool["type"] != "filesystem" {
		return ErrUnknown
	}
	parent, e := read(ctx, cmd, r.Birth.Dataset, ns)
	if e != nil || parent["guid"] != r.Birth.GUID || !parentProperties(parent, r.Birth.QuotaBytes, "on") {
		return ErrUnknown
	}
	for _, root := range r.Roots {
		p, e := read(ctx, cmd, root.Name, ns)
		if e != nil || p["guid"] != root.GUID || p["type"] != "filesystem" || p["quota"] != strconv.FormatInt(root.Bytes, 10) || p["zoned"] != "on" {
			return ErrUnknown
		}
		for _, prop := range []string{"zoned", "quota"} {
			raw, e := cmd.run(ctx, []string{"get", "-H", "-p", "-o", "value,source", prop, root.Name}, ns)
			want := "on"
			if prop == "quota" {
				want = strconv.FormatInt(root.Bytes, 10)
			}
			if e != nil || string(raw) != want+"\tlocal\n" {
				return ErrUnknown
			}
		}
	}
	return nil
}

// Budgets returns a copy of immutable limits, not measured free space.
func (s SealedBudgets) Budgets() CategoryBudgets {
	b := CategoryBudgets{Runtime: s.RuntimeBytes(), Categories: map[string]int64{}}
	if len(s.record.Roots) != 1+len(sealedCategories) {
		return CategoryBudgets{}
	}
	for i, n := range sealedCategories {
		b.Categories[n] = s.record.Roots[i+1].Bytes
	}
	return b
}
func (s SealedBudgets) BirthID() string { return s.record.Birth.CapsuleID }

// Recheck verifies identities and local protected quota setpoints. The original
// held namespace and successful one-way transition are retained privately;
// zoned=on alone is never treated as evidence of namespace membership.
func (s SealedBudgets) Recheck(ctx context.Context) error {
	return s.state.check(func() error { return s.recheck(ctx) })
}
func (s SealedBudgets) recheck(ctx context.Context) error {
	b := s.birth
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || b.peer == nil || !b.peer.Alive() || b.peer.BirthID() != s.BirthID() || len(s.record.Roots) != 1+len(sealedCategories) {
		return ErrRefused
	}
	ns, e := b.peer.UserNamespace()
	if e != nil {
		return ErrRefused
	}
	defer ns.Close()
	binary, e := openBinary(b.config.Binary, b.config.BinarySHA256)
	if e != nil {
		return e
	}
	defer binary.Close()
	helper, e := openBinary(b.config.Helper, b.config.HelperSHA256)
	if e != nil {
		return e
	}
	defer helper.Close()
	if checkSeal(ctx, s.record, ns, systemCommands{binary, helper}) != nil || !b.peer.Alive() {
		return ErrUnknown
	}
	return nil
}
