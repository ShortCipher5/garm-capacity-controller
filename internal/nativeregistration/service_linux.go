//go:build linux

// Package nativeregistration persists explicit, fresh server configuration.
// Registration never uploads, activates, enrolls capacity, or retries unknown work.
package nativeregistration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprovision"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/sys/unix"
)

var ErrRegistration = errors.New("server configuration registration unavailable; preserve consumed identity and inspect its history")
var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const version = "native-target-registration/v1"
const maxRegistrations = 8

type Request struct {
	Revision              string                 `json:"revision"`
	Template              string                 `json:"template"`
	Identity              string                 `json:"identity"`
	Administrator         string                 `json:"administrator"`
	ArtifactReference     string                 `json:"artifact_reference"`
	StorageReference      string                 `json:"storage_reference"`
	NetworkReference      string                 `json:"network_reference"`
	PreparationID         string                 `json:"preparation_id"`
	Host                  string                 `json:"host"`
	InventoryTargetID     string                 `json:"inventory_target_id"`
	InventoryTargetDigest string                 `json:"inventory_target_digest"`
	NativeTarget          nativeprovision.Target `json:"native_target"`
}
type Report struct {
	PreparationID        string                 `json:"preparation_id"`
	TargetID             string                 `json:"target_id"`
	RegistrationDigest   string                 `json:"registration_digest"`
	Revision             string                 `json:"revision"`
	State                string                 `json:"state"`
	Binding              nativeplanning.Binding `json:"binding"`
	Template             string                 `json:"template"`
	Identity             string                 `json:"identity"`
	Administrator        string                 `json:"administrator"`
	ArtifactReference    string                 `json:"artifact_reference"`
	StorageReference     string                 `json:"storage_reference"`
	NetworkReference     string                 `json:"network_reference"`
	ActivationAuthorized bool                   `json:"activation_authorized"`
}
type intent struct {
	Version  string
	Request  Request
	Metadata nativebundle.RegistrationMetadata
}
type completed struct{ Version, RegistrationDigest string }

// Configured can only be obtained from a completed durable registration and
// exact compiler readback. It is configuration, not runtime authority.
type Configured struct {
	metadata nativebundle.RegistrationMetadata
}

func (c Configured) Metadata() nativebundle.RegistrationMetadata { return c.metadata }

type Service struct {
	catalogs     map[string]*nativebundle.RegistrationCompiler
	catalogHead  string
	catalogCount int
	mu           sync.Mutex
	compiler     *nativebundle.RegistrationCompiler
	inventory    *sshinventory.Service
	directory    string
}

func privateDirectory(p string) error {
	if nativeplanning.ValidateDirectory(p) != nil {
		return ErrRegistration
	}
	s, e := os.Lstat(p)
	if e != nil || s.Mode() != os.ModeDir|0700 {
		return ErrRegistration
	}
	return nil
}
func NewService(c *nativebundle.RegistrationCompiler, i *sshinventory.Service, directory string) (*Service, error) {
	if c == nil || c.Revision() == "" || i == nil || privateDirectory(directory) != nil {
		return nil, ErrRegistration
	}
	return &Service{compiler: c, inventory: i, directory: directory}, nil
}
func checkedFile(f *os.File) error {
	s, e := f.Stat()
	if e != nil {
		return ErrRegistration
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok || s.Mode() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return ErrRegistration
	}
	return nil
}
func (s *Service) lock() (*os.File, error) {
	if privateDirectory(s.directory) != nil {
		return nil, ErrRegistration
	}
	f, e := os.OpenFile(filepath.Join(s.directory, "registration.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if e != nil {
		return nil, ErrRegistration
	}
	if checkedFile(f) != nil || unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrRegistration
	}
	return f, nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (s *Service) read(name string, v any) error {
	f, e := os.OpenFile(filepath.Join(s.directory, name), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	if checkedFile(f) != nil {
		return ErrRegistration
	}
	b, e := io.ReadAll(io.LimitReader(f, catalogFileLimit(name)+1))
	if e != nil || int64(len(b)) > catalogFileLimit(name) {
		return ErrRegistration
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrRegistration
	}
	canonical, e := json.Marshal(v)
	if e != nil || !bytes.Equal(b, canonical) {
		return ErrRegistration
	}
	return nil
}
func (s *Service) write(name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil || int64(len(b)) > catalogFileLimit(name) {
		return ErrRegistration
	}
	f, e := os.OpenFile(filepath.Join(s.directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return ErrRegistration
	}
	defer f.Close()
	if f.Chmod(0600) != nil {
		return ErrRegistration
	}
	if n, e := f.Write(b); e != nil || n != len(b) {
		return ErrRegistration
	}
	if f.Sync() != nil {
		return ErrRegistration
	}
	return syncDir(s.directory)
}
func (s *Service) compile(ctx context.Context, r Request) (nativebundle.Registration, error) {
	return s.compileAt(ctx, r, false)
}
func (s *Service) compileAt(ctx context.Context, r Request, historical bool) (nativebundle.Registration, error) {
	if ctx == nil || ctx.Err() != nil || !idPattern.MatchString(r.PreparationID) || !idPattern.MatchString(r.InventoryTargetDigest) {
		return nativebundle.Registration{}, ErrRegistration
	}
	targets, e := s.inventory.ListTargets(ctx)
	if e != nil {
		return nativebundle.Registration{}, ErrRegistration
	}
	var t sshinventory.Target
	matches := 0
	for _, x := range targets {
		if x.ID == r.InventoryTargetID {
			t = x
			matches++
		}
	}
	if matches != 1 || nativeplanning.Digest(t) != r.InventoryTargetDigest {
		return nativebundle.Registration{}, ErrRegistration
	}
	if historical {
		return s.compileHistoricalPinned(r, t)
	}
	return s.compilePinned(r, t)
}
func (s *Service) compilePinned(r Request, t sshinventory.Target) (nativebundle.Registration, error) {
	if nativeplanning.Digest(t) != r.InventoryTargetDigest || t.ID != r.InventoryTargetID {
		return nativebundle.Registration{}, ErrRegistration
	}
	return s.compiler.Compile(nativebundle.RegistrationRequest{Revision: r.Revision, Template: r.Template, Identity: r.Identity, Administrator: r.Administrator, ArtifactReference: r.ArtifactReference, StorageReference: r.StorageReference, NetworkReference: r.NetworkReference, PreparationID: r.PreparationID, Host: r.Host, NativeTarget: r.NativeTarget, InventoryTarget: t})
}
func report(m nativebundle.RegistrationMetadata, state string) Report {
	return Report{PreparationID: m.Binding.PreparationID, TargetID: m.TargetID, RegistrationDigest: m.RegistrationDigest, Revision: m.Revision, State: state, Binding: m.Binding, Template: m.Template, Identity: m.Identity, Administrator: m.Administrator, ArtifactReference: m.ArtifactReference, StorageReference: m.StorageReference, NetworkReference: m.NetworkReference}
}

// records rejects orphan receipts and malformed state before any new effect.
func (s *Service) records() ([]intent, map[string]bool, error) {
	f, e := os.Open(s.directory)
	if e != nil {
		return nil, nil, ErrRegistration
	}
	names, e := f.Readdirnames(maxRegistrations*2 + maxCatalogRevisions + 2)
	f.Close()
	if (e != nil && e != io.EOF) || len(names) > maxRegistrations*2+maxCatalogRevisions+1 {
		return nil, nil, ErrRegistration
	}
	if e := s.readCatalogHistory(names); e != nil {
		return nil, nil, e
	}
	ids := map[string]bool{}
	done := map[string]bool{}
	for _, n := range names {
		if n == "registration.lock" || strings.HasPrefix(n, "catalog-") {
			continue
		}
		switch {
		case strings.HasSuffix(n, ".intent.json"):
			id := strings.TrimSuffix(n, ".intent.json")
			if !idPattern.MatchString(id) {
				return nil, nil, ErrRegistration
			}
			ids[id] = true
		case strings.HasSuffix(n, ".complete.json"):
			id := strings.TrimSuffix(n, ".complete.json")
			if !idPattern.MatchString(id) {
				return nil, nil, ErrRegistration
			}
			done[id] = true
		default:
			return nil, nil, ErrRegistration
		}
	}
	if len(ids) > maxRegistrations {
		return nil, nil, ErrRegistration
	}
	for id := range done {
		if !ids[id] {
			return nil, nil, ErrRegistration
		}
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	out := []intent{}
	used := map[string]bool{}
	for _, id := range order {
		var in intent
		if s.read(id+".intent.json", &in) != nil || in.Version != version || in.Request.PreparationID != id || in.Metadata.Binding.PreparationID != id {
			return nil, nil, ErrRegistration
		}
		r, e := s.compileHistoricalPinned(in.Request, in.Metadata.Binding.InventoryTarget)
		if e != nil || nativeplanning.Digest(r.Metadata()) != nativeplanning.Digest(in.Metadata) {
			return nil, nil, ErrRegistration
		}
		for _, key := range reservationKeys(in.Metadata) {
			if used[key] {
				return nil, nil, ErrRegistration
			}
			used[key] = true
		}
		if done[id] {
			var c completed
			if s.read(id+".complete.json", &c) != nil || c.Version != version || c.RegistrationDigest != in.Metadata.RegistrationDigest {
				return nil, nil, ErrRegistration
			}
		}
		out = append(out, in)
	}
	return out, done, nil
}
func reservationKeys(m nativebundle.RegistrationMetadata) []string {
	return []string{"id:" + m.Binding.PreparationID, "target:" + m.TargetID, "host:" + m.Binding.Host, "identity:" + m.Identity, "spki:" + m.OwnerServerSPKI, "spki:" + m.OwnerClientSPKI}
}

// Review resolves the saved inventory endpoint and compiles only. No writes.
func (s *Service) Review(ctx context.Context, r Request) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	compiled, e := s.compile(ctx, r)
	if e != nil {
		return Report{}, ErrRegistration
	}
	return report(compiled.Metadata(), "configuration_review_only"), nil
}

// Register consumes identities durably BEFORE writing private configuration.
// A failure after consumption is permanently unknown; this method never adopts,
// retries, overwrites or frees a reservation. It makes no SSH request.
func (s *Service) Register(ctx context.Context, r Request, reviewDigest string) (Configured, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	compiled, e := s.compile(ctx, r)
	if e != nil || compiled.Metadata().RegistrationDigest != reviewDigest {
		return Configured{}, ErrRegistration
	}
	lock, e := s.lock()
	if e != nil {
		return Configured{}, e
	}
	defer lock.Close()
	records, _, e := s.records()
	if e != nil || len(records) >= maxRegistrations {
		return Configured{}, ErrRegistration
	}
	m := compiled.Metadata()
	used := map[string]bool{}
	for _, in := range records {
		for _, k := range reservationKeys(in.Metadata) {
			used[k] = true
		}
	}
	for _, k := range reservationKeys(m) {
		if used[k] {
			return Configured{}, ErrRegistration
		}
	}
	if s.checkpointCatalog() != nil {
		return Configured{}, ErrRegistration
	}
	if ctx.Err() != nil || s.write(r.PreparationID+".intent.json", intent{version, r, m}) != nil {
		return Configured{}, ErrRegistration
	}
	if compiled.Materialize(ctx) != nil {
		return Configured{}, ErrRegistration
	}
	// The preparation journal is a fresh private child, never an adopted path.
	if privateDirectory(filepath.Dir(m.JournalDirectory)) != nil || os.Mkdir(m.JournalDirectory, 0700) != nil || os.Chmod(m.JournalDirectory, 0700) != nil || syncDir(filepath.Dir(m.JournalDirectory)) != nil {
		return Configured{}, ErrRegistration
	}
	// Re-resolve the inventory pin before recording a completed configuration.
	now, e := s.compile(ctx, r)
	if e != nil || now.Metadata().RegistrationDigest != reviewDigest || compiled.InspectMaterialized(ctx) != nil {
		return Configured{}, ErrRegistration
	}
	if s.write(r.PreparationID+".complete.json", completed{version, reviewDigest}) != nil {
		return Configured{}, ErrRegistration
	}
	return Configured{m}, nil
}

// Restore verifies completed private bytes; unknown entries remain consumed.
// It grants configuration access only and does not renew OS observation age.
func (s *Service) Restore(ctx context.Context) ([]Configured, []Report, error) {
	return s.restore(ctx, false)
}

// AnchorCatalog is an explicit startup metadata migration, not a GET action.
// It validates exact legacy state and completed materialization before writing.
func (s *Service) AnchorCatalog(ctx context.Context) error { _, _, e := s.restore(ctx, true); return e }
func (s *Service) restore(ctx context.Context, anchor bool) ([]Configured, []Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrRegistration
	}
	lock, e := s.lock()
	if e != nil {
		return nil, nil, e
	}
	defer lock.Close()
	records, done, e := s.records()
	if e != nil {
		return nil, nil, e
	}
	out := []Configured{}
	history := []Report{}
	for _, in := range records {
		state := "registration_unknown"
		if done[in.Request.PreparationID] {
			r, e := s.compileAt(ctx, in.Request, true)
			if e != nil || r.InspectMaterialized(ctx) != nil || privateDirectory(in.Metadata.JournalDirectory) != nil {
				return nil, nil, ErrRegistration
			}
			out = append(out, Configured{in.Metadata})
			state = "configuration_registered"
		}
		history = append(history, report(in.Metadata, state))
	}
	// Legacy migration is permitted only after all exact original records and
	// materialized outputs were validated under the unchanged original catalog.
	if anchor && s.checkpointCatalog() != nil {
		return nil, nil, ErrRegistration
	}
	return out, history, nil
}

func (s *Service) CatalogView() nativebundle.RegistrationCatalogView { return s.compiler.CatalogView() }
