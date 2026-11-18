//go:build linux

// Package nativeassembly converts an explicitly selected saved configuration
// into a fresh private bundle. It neither uploads nor reloads nor activates it.
package nativeassembly

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"golang.org/x/sys/unix"
)

var ErrAssembly = errors.New("native assembly unavailable; inspect the exact saved selection and preserve partial output")

const version = "native-assembly/v1"

type Recipe struct {
	Binding      nativeplanning.Binding
	Build        nativebundle.Configuration
	OutputParent string
}
type Report struct {
	TargetID             string `json:"target_id"`
	PreparationID        string `json:"preparation_id"`
	SelectionDigest      string `json:"selection_digest"`
	ManifestDigest       string `json:"manifest_digest"`
	PlanDigest           string `json:"plan_digest"`
	AssemblyDigest       string `json:"assembly_digest"`
	State                string `json:"state"`
	ActivationAuthorized bool   `json:"activation_authorized"`
}
type Completed struct {
	report Report
	path   string
	target nativeprovision.Target
}

func (c Completed) Report() Report                 { return c.report }
func (c Completed) BundlePath() string             { return c.path }
func (c Completed) Target() nativeprovision.Target { return c.target }

type record struct {
	Version, RecipeDigest string
	Report                Report
}
type Service struct {
	mu       sync.Mutex
	planning *nativeplanning.Service
	recipes  map[string]Recipe
}

func hash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func NewService(planning *nativeplanning.Service, recipes map[string]Recipe) (*Service, error) {
	if planning == nil || len(recipes) < 1 || len(recipes) > 8 {
		return nil, ErrAssembly
	}
	bindings := planning.Bindings()
	owned := map[string]Recipe{}
	for id, r := range recipes {
		b, ok := bindings[id]
		if !ok || nativeplanning.Digest(b) != nativeplanning.Digest(r.Binding) || nativeplanning.ValidateBinding(b) != nil ||
			r.Build.ID != b.PreparationID || r.Build.SelectionFile != "" || r.Build.ExpectedSelectionDigest != "" || r.Build.ExpectedBindingFile == "" || r.Build.OutputDirectory != "" ||
			privateDirectory(r.OutputParent) != nil {
			return nil, ErrAssembly
		}
		owned[id] = r
	}
	return &Service{planning: planning, recipes: owned}, nil
}
func privateDirectory(p string) error {
	if nativeplanning.ValidateDirectory(p) != nil {
		return ErrAssembly
	}
	st, e := os.Lstat(p)
	if e != nil || st.Mode() != os.ModeDir|0700 {
		return ErrAssembly
	}
	return nil
}
func (r Recipe) stem(selection string) string { return r.Binding.PreparationID + "-" + selection }
func (r Recipe) output(selection string) string {
	return filepath.Join(r.OutputParent, r.stem(selection)+".bundle")
}
func assemblyDigest(r record) string { r.Report.AssemblyDigest = ""; return nativeplanning.Digest(r) }
func lock(r Recipe, write bool) (*os.File, error) {
	if privateDirectory(r.OutputParent) != nil {
		return nil, ErrAssembly
	}
	flags := os.O_RDWR | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if write {
		flags |= os.O_CREATE
	}
	f, e := os.OpenFile(filepath.Join(r.OutputParent, "native-assembly.lock"), flags, 0600)
	if e != nil {
		return nil, ErrAssembly
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, ErrAssembly
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || meta.Uid != uint32(os.Geteuid()) || meta.Nlink != 1 {
		f.Close()
		return nil, ErrAssembly
	}
	op := unix.LOCK_SH
	if write {
		op = unix.LOCK_EX
	}
	if unix.Flock(int(f.Fd()), op|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrAssembly
	}
	return f, nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return ErrAssembly
	}
	defer f.Close()
	return f.Sync()
}
func writeRecord(path string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > 8192 {
		return ErrAssembly
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return ErrAssembly
	}
	e = f.Chmod(0600)
	if e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil || syncDir(filepath.Dir(path)) != nil {
		return ErrAssembly
	}
	return nil
}
func readRecord(path string) (record, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return record{}, ErrAssembly
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return record{}, ErrAssembly
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() < 1 || st.Size() > 8192 || meta.Uid != uint32(os.Geteuid()) || meta.Nlink != 1 {
		return record{}, ErrAssembly
	}
	raw, e := io.ReadAll(io.LimitReader(f, 8193))
	if e != nil || len(raw) > 8192 {
		return record{}, ErrAssembly
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var r record
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return record{}, ErrAssembly
	}
	canonical, e := json.Marshal(r)
	if e != nil || !bytes.Equal(raw, canonical) {
		return record{}, ErrAssembly
	}
	return r, nil
}

// Build consumes a fresh saved choice and a fresh deterministic output only.
// An interrupted attempt is never replayed or adopted automatically.
func (s *Service) Build(ctx context.Context, id, selectionDigest string) (Completed, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !hash(selectionDigest) {
		return Completed{}, ErrAssembly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recipes[id]
	if !ok {
		return Completed{}, ErrAssembly
	}
	path, selection, e := s.planning.LookupSaved(ctx, id, selectionDigest)
	if e != nil || nativeplanning.Digest(selection.Binding) != nativeplanning.Digest(r.Binding) || nativeplanning.Digest(selection) != selectionDigest {
		return Completed{}, ErrAssembly
	}
	locked, e := lock(r, true)
	if e != nil {
		return Completed{}, e
	}
	defer locked.Close()
	dir, e := os.Open(r.OutputParent)
	if e != nil {
		return Completed{}, ErrAssembly
	}
	names, e := dir.Readdirnames(1025)
	dir.Close()
	if e != nil && e != io.EOF || len(names) >= 1000 {
		return Completed{}, ErrAssembly
	}
	intent := record{Version: version, RecipeDigest: nativeplanning.Digest(r), Report: Report{TargetID: id, PreparationID: r.Binding.PreparationID, SelectionDigest: selectionDigest, State: "assembly_unknown"}}
	if writeRecord(filepath.Join(r.OutputParent, r.stem(selectionDigest)+".intent.json"), intent) != nil {
		return Completed{}, ErrAssembly
	}
	c := r.Build
	c.SelectionFile = path
	c.ExpectedSelectionDigest = selectionDigest
	c.OutputDirectory = r.output(selectionDigest)
	result, e := nativebundle.Build(ctx, c)
	if e != nil || result.SelectionDigest != selectionDigest || result.ID != r.Binding.PreparationID || result.ActivationAuthorized || result.State != "bundle_created_not_prepared" {
		return Completed{}, ErrAssembly
	}
	bundle, e := nativeprovision.LoadBundle(c.OutputDirectory)
	if e != nil {
		return Completed{}, ErrAssembly
	}
	defer bundle.Close()
	plan, e := bundle.Plan(r.Binding.NativeTarget)
	if e != nil || ctx.Err() != nil {
		return Completed{}, ErrAssembly
	}
	actualManifest, e := nativeprepare.ManifestDigest(plan.Manifest)
	if e != nil || actualManifest != result.ManifestDigest {
		return Completed{}, ErrAssembly
	}
	report := Report{TargetID: id, PreparationID: result.ID, SelectionDigest: selectionDigest, ManifestDigest: result.ManifestDigest, PlanDigest: nativeprovision.Digest(plan), State: "bundle_created_not_reloaded"}
	complete := record{version, nativeplanning.Digest(r), report}
	complete.Report.AssemblyDigest = assemblyDigest(complete)
	if writeRecord(filepath.Join(r.OutputParent, r.stem(selectionDigest)+".receipt.json"), complete) != nil {
		return Completed{}, ErrAssembly
	}
	return Completed{complete.Report, c.OutputDirectory, r.Binding.NativeTarget}, nil
}

// InspectCompleted validates historical configuration, not fresh OS capacity.
// It never calls Probe, rebuilds an output, or grants preparation authority.
func (s *Service) InspectCompleted(ctx context.Context, id, selectionDigest string) (Completed, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !hash(selectionDigest) {
		return Completed{}, ErrAssembly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recipes[id]
	if !ok {
		return Completed{}, ErrAssembly
	}
	held, e := lock(r, false)
	if e != nil {
		return Completed{}, e
	}
	defer held.Close()
	intent, e := readRecord(filepath.Join(r.OutputParent, r.stem(selectionDigest)+".intent.json"))
	expected := record{Version: version, RecipeDigest: nativeplanning.Digest(r), Report: Report{TargetID: id, PreparationID: r.Binding.PreparationID, SelectionDigest: selectionDigest, State: "assembly_unknown"}}
	if e != nil || intent != expected {
		return Completed{}, ErrAssembly
	}
	saved, e := readRecord(filepath.Join(r.OutputParent, r.stem(selectionDigest)+".receipt.json"))
	if e != nil || saved.Version != version || saved.RecipeDigest != expected.RecipeDigest || saved.Report.TargetID != id || saved.Report.PreparationID != r.Binding.PreparationID || saved.Report.SelectionDigest != selectionDigest || saved.Report.State != "bundle_created_not_reloaded" || saved.Report.ActivationAuthorized || !hash(saved.Report.ManifestDigest) || !hash(saved.Report.PlanDigest) || saved.Report.AssemblyDigest != assemblyDigest(saved) {
		return Completed{}, ErrAssembly
	}
	bundle, e := nativeprovision.LoadBundle(r.output(selectionDigest))
	if e != nil {
		return Completed{}, ErrAssembly
	}
	defer bundle.Close()
	plan, e := bundle.Plan(r.Binding.NativeTarget)
	if e != nil || nativeprovision.Digest(plan) != saved.Report.PlanDigest || ctx.Err() != nil {
		return Completed{}, ErrAssembly
	}
	// The plan digest binds the actual manifest, exact file sizes/hashes, target
	// and fixed upload script; the manifest digest is separately checked below.
	actualManifest, e := nativeprepare.ManifestDigest(plan.Manifest)
	if e != nil || actualManifest != saved.Report.ManifestDigest {
		return Completed{}, ErrAssembly
	}
	return Completed{saved.Report, r.output(selectionDigest), r.Binding.NativeTarget}, nil
}
