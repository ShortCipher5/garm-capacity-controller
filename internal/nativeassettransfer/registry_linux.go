//go:build linux

package nativeassettransfer

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassembly"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeassets"
)

// Registry assigns deployment-approved, unique asset IDs to completed owner
// assemblies. Registration is local configuration only, never an SSH operation.
// An interrupted reservation consumes its slot permanently; there is no reset.
type Registry struct {
	mu       sync.Mutex
	journal  *Service
	slots    map[string]*Bundle
	services map[string]*Service
	revision string
}
type RegistrationReview struct {
	Slot            string `json:"slot"`
	TargetID        string `json:"target_id"`
	PreparationID   string `json:"preparation_id"`
	CatalogDigest   string `json:"catalog_digest"`
	AssemblyDigest  string `json:"assembly_digest"`
	SelectionDigest string `json:"selection_digest"`
	PlanDigest      string `json:"plan_digest"`
	AssetID         string `json:"asset_id"`
	ManifestDigest  string `json:"manifest_digest"`
	TransferDigest  string `json:"transfer_digest"`
	ReviewDigest    string `json:"review_digest"`
	Target          Target `json:"target"`
}

func NewRegistry(directory string, slots map[string]*Bundle) (*Registry, error) {
	if len(slots) < 1 || len(slots) > 8 || protected(directory, uint32(os.Geteuid())) != nil {
		return nil, ErrInvalid
	}
	st, e := os.Lstat(directory)
	if e != nil || st.Mode() != os.ModeDir|0700 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, ErrInvalid
	}
	r := &Registry{journal: &Service{dir: directory, uid: uint32(os.Geteuid())}, slots: map[string]*Bundle{}, services: map[string]*Service{}}
	identities := map[string]string{}
	seen := map[string]bool{}
	for name, b := range slots {
		if !admission.ValidOnboardingProfileID(name) || b == nil {
			return nil, ErrInvalid
		}
		b.mu.Lock()
		raw, e := json.Marshal(struct {
			Manifest nativeassets.Manifest
			Files    []File
		}{b.manifest, b.files})
		id := b.manifest.ID
		closed := b.closed
		b.mu.Unlock()
		if e != nil || closed || seen[id] || !validHash(id) {
			return nil, ErrInvalid
		}
		seen[id] = true
		identities[name] = digest(raw)
		r.slots[name] = b
	}
	raw, _ := json.Marshal(identities)
	r.revision = digest(raw)
	if r.validateRecords() != nil {
		return nil, ErrUnknown
	}
	return r, nil
}
func (r *Registry) Slots() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := []string{}
	for n := range r.slots {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) review(slot string, c nativeassembly.Completed) (RegistrationReview, error) {
	b := r.slots[slot]
	report := c.Report()
	p, e := b.Plan(c.Target())
	if e != nil || !admission.ValidOnboardingProfileID(report.TargetID) || !validHash(report.PreparationID) || !validHash(report.AssemblyDigest) || !validHash(report.PlanDigest) || !validHash(report.SelectionDigest) {
		return RegistrationReview{}, ErrInvalid
	}
	md, e := nativeassets.ManifestDigest(p.Manifest)
	if e != nil || report.RuntimeAssetID != p.Manifest.ID || report.RuntimeAssetManifestDigest != md {
		return RegistrationReview{}, ErrInvalid
	}
	v := RegistrationReview{Slot: slot, TargetID: report.TargetID, PreparationID: report.PreparationID, CatalogDigest: r.revision, AssemblyDigest: report.AssemblyDigest, SelectionDigest: report.SelectionDigest, PlanDigest: report.PlanDigest, AssetID: p.Manifest.ID, ManifestDigest: md, TransferDigest: Digest(p), Target: c.Target()}
	raw, _ := json.Marshal(v)
	v.ReviewDigest = digest(raw)
	return v, nil
}
func (r *Registry) Review(slot string, c nativeassembly.Completed) (RegistrationReview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.review(slot, c)
}
func (r *Registry) read(slot, suffix string) ([]byte, error) {
	return r.journal.read("asset-slot-" + slot + suffix + ".json")
}

// Register requires a fresh exact review and a real opaque completed assembly.
// The reservation and completion are immutable, fsynced, separate records.
func (r *Registry) Register(slot string, c nativeassembly.Completed, review string) (*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, e := r.review(slot, c)
	if e != nil || review != v.ReviewDigest {
		return nil, ErrInvalid
	}
	lock, e := r.journal.lock()
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	if r.validateRecords() != nil {
		return nil, ErrUnknown
	}
	for name := range r.slots {
		raw, e := r.read(name, "-intent")
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, ErrUnknown
		}
		var old RegistrationReview
		if json.Unmarshal(raw, &old) != nil || old.CatalogDigest != r.revision {
			return nil, ErrUnknown
		}
		if name == slot || old.TargetID == v.TargetID || old.AssetID == v.AssetID {
			return nil, ErrUnknown
		}
	}
	raw, _ := json.Marshal(v)
	if r.journal.write("asset-slot-"+slot+"-intent.json", raw) != nil {
		return nil, ErrUnknown
	}
	dir := filepath.Join(r.journal.dir, "asset-slot-"+slot)
	if os.Mkdir(dir, 0700) != nil || syncDirectory(r.journal.dir) != nil {
		return nil, ErrUnknown
	}
	s, e := NewService(r.slots[slot], v.Target, dir)
	if e != nil {
		return nil, ErrUnknown
	}
	if r.journal.write("asset-slot-"+slot+"-complete.json", raw) != nil {
		s.Close()
		return nil, ErrUnknown
	}
	r.services[slot] = s
	return s, nil
}

// Restore only reopens an exact completed registration. Missing or partial
// records are never repaired, and mismatched assemblies cannot reuse a slot.
func (r *Registry) Restore(slot string, c nativeassembly.Completed) (*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, e := r.review(slot, c)
	if e != nil {
		return nil, e
	}
	lock, e := r.journal.lock()
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	if r.validateRecords() != nil {
		return nil, ErrUnknown
	}
	raw, _ := json.Marshal(v)
	a, e := r.read(slot, "-intent")
	if e != nil || !bytes.Equal(a, raw) {
		return nil, ErrUnknown
	}
	b, e := r.read(slot, "-complete")
	if e != nil || !bytes.Equal(b, raw) {
		return nil, ErrUnknown
	}
	if s := r.services[slot]; s != nil {
		return s, nil
	}
	s, e := NewService(r.slots[slot], v.Target, filepath.Join(r.journal.dir, "asset-slot-"+slot))
	if e != nil {
		return nil, e
	}
	r.services[slot] = s
	return s, nil
}
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.services {
		s.Close()
	}
	for _, b := range r.slots {
		b.Close()
	}
}

// validateRecords rejects removed catalog slots and partial/malformed history.
// Unknown state is never treated as free configuration capacity.
func (r *Registry) validateRecords() error {
	dir, e := os.Open(r.journal.dir)
	if e != nil {
		return ErrUnknown
	}
	entries, e := dir.ReadDir(26)
	dir.Close()
	if len(entries) > 25 {
		return ErrUnknown
	}
	if e != nil && e != io.EOF {
		return ErrUnknown
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "native-assets-workflow.lock" {
			continue
		}
		found := false
		for slot := range r.slots {
			base := "asset-slot-" + slot
			if name == base || name == base+"-intent.json" || name == base+"-complete.json" {
				found = true
				break
			}
		}
		if !found {
			return ErrUnknown
		}
	}
	for slot := range r.slots {
		raw, e := r.read(slot, "-intent")
		if os.IsNotExist(e) {
			if _, e := os.Lstat(filepath.Join(r.journal.dir, "asset-slot-"+slot)); !os.IsNotExist(e) {
				return ErrUnknown
			}
			if _, e := r.read(slot, "-complete"); !os.IsNotExist(e) {
				return ErrUnknown
			}
			continue
		}
		if e != nil {
			return ErrUnknown
		}
		var v RegistrationReview
		if json.Unmarshal(raw, &v) != nil {
			return ErrUnknown
		}
		canon, _ := json.Marshal(v)
		if !bytes.Equal(canon, raw) || v.Slot != slot || v.CatalogDigest != r.revision || !validHash(v.ReviewDigest) {
			return ErrUnknown
		}
		want := v.ReviewDigest
		v.ReviewDigest = ""
		check, _ := json.Marshal(v)
		if digest(check) != want {
			return ErrUnknown
		}
		if done, e := r.read(slot, "-complete"); e == nil && !bytes.Equal(done, raw) || e != nil && !os.IsNotExist(e) {
			return ErrUnknown
		}
	}
	return nil
}
func (r *Registry) State(slot string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.slots[slot] == nil || r.validateRecords() != nil {
		return "unavailable"
	}
	if _, e := r.read(slot, "-intent"); os.IsNotExist(e) {
		return "available"
	}
	if _, e := r.read(slot, "-complete"); e == nil {
		return "registered"
	}
	return "consumed_unknown"
}

// AssetIDs permits configuration-time collision checks against legacy recipes.
func (r *Registry) AssetIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := []string{}
	for _, b := range r.slots {
		b.mu.Lock()
		ids = append(ids, b.manifest.ID)
		b.mu.Unlock()
	}
	return ids
}
