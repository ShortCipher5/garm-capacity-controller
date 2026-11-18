package managedgarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

// ControlledRestart performs an explicitly requested control-plane interruption.
// It is not called by startup, discovery or the allocator. It requires an already
// healthy managed bundle and kernel-sealed configuration; it never adopts an
// arbitrary existing GARM installation or enables capacity writes.
//
// An ambiguous transition leaves a durable pending record and cannot be retried
// through this API. No compensating restart/stop is guessed after uncertainty.
func ControlledRestart(ctx context.Context, s *Session, source ProviderProfileConfiguration, pins ProviderPathPins, writer RestartParticipant) (*LoadedStockBinding, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || s == nil || writer == nil {
		return nil, ErrBoundary
	}
	observer, err := NewProviderPathObserver(ctx, s, source, pins)
	if err != nil {
		return nil, err
	}
	topology, err := Observe(ctx, s.spec)
	if err != nil {
		return nil, err
	}
	profiles, err := observer.profiles.read(ctx)
	if err != nil {
		return nil, err
	}
	sealed, err := measureStockConfiguration(s.spec)
	if err != nil {
		return nil, err
	}
	names, err := providerNames(profiles.Payload.Profiles, s.ControllerID())
	if err != nil || validateSealedConfigSet(sealed, names) != nil {
		return nil, ErrBoundary
	}
	mounted, err := measureSealedDirectory(fmt.Sprintf("/proc/%d/root/etc/garm", topology.garmPID))
	if err != nil || !reflect.DeepEqual(mounted, sealed) {
		return nil, ErrBoundary
	}
	files, err := openBootstrapFiles(s.spec.AdminSecretDirectory, 0)
	if err != nil {
		return nil, err
	}
	defer files.root.Close()
	order, err := managedRestartOrder(s.spec)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(profiles)
	if err != nil {
		return nil, ErrBoundary
	}
	record := restartRecord{Version: "managed-restart/v1", Specification: s.spec, ProfileSnapshot: raw, Files: sealed, OriginalPID: topology.garmPID, OriginalStart: topology.processStart, Steps: order, State: "prepared"}
	// Exclusive durable intent is the concurrency gate, written before any stop.
	// It is never deleted by this function, including on pre-dispatch failure.
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > 8<<20 {
		return nil, ErrBoundary
	}
	intentHash := hashBytes(encoded)
	if files.create("controlled-restart-intent.json", record) != nil {
		return nil, ErrBoundary
	}
	daemon, err := openRestartDocker(s.spec.DockerSocket)
	if err != nil {
		return nil, err
	}
	defer daemon.transport.CloseIdleConnections()
	if files.create("controlled-restart-writer-suspend-pending.json", intentHash) != nil {
		return nil, ErrBoundary
	}
	if writer.Suspend(ctx) != nil {
		return nil, ErrBoundary
	}
	if files.create("controlled-restart-writer-suspend-confirmed.json", intentHash) != nil {
		return nil, ErrBoundary
	}
	for i, step := range order {
		if ctx.Err() != nil {
			return nil, ErrBoundary
		}
		current, err := measureStockConfiguration(s.spec)
		if err != nil || !reflect.DeepEqual(current, sealed) {
			return nil, ErrBoundary
		}
		pending, err := advanceRestart(record, true, false)
		if err != nil {
			return nil, err
		}
		if files.create(fmt.Sprintf("controlled-restart-%d-pending.json", i), restartJournalEvent{Intent: intentHash, Index: i, Step: step, Pending: pending.Pending}) != nil {
			return nil, ErrBoundary
		}
		if daemon.transition(ctx, step) != nil {
			return nil, ErrBoundary
		}
		record, err = advanceRestart(record, true, true)
		if err != nil {
			return nil, err
		}
		if files.create(fmt.Sprintf("controlled-restart-%d-confirmed.json", i), restartJournalEvent{Intent: intentHash, Index: i, Step: step, Pending: false}) != nil {
			return nil, ErrBoundary
		}
	}
	if files.create("controlled-restart-writer-resume-pending.json", intentHash) != nil {
		return nil, ErrBoundary
	}
	// Resume is an action hook, never evidence. The actual nonce/TLS descriptor,
	// local session identity and mounted files are independently rechecked below.
	if writer.Resume(ctx) != nil {
		return nil, ErrBoundary
	}
	if files.create("controlled-restart-writer-resume-confirmed.json", intentHash) != nil {
		return nil, ErrBoundary
	}
	// HTTP 204/start alone is not loaded-config proof. Reopen the real new stock
	// session and reverify the entire bundle, exact provider process and profiles.
	fresh, err := waitRestartSession(ctx, s.spec)
	if err != nil {
		return nil, ErrBoundary
	}
	valid := false
	defer func() {
		if !valid {
			fresh.Close()
		}
	}()
	final, err := NewProviderPathObserver(ctx, fresh, source, pins)
	if err != nil {
		return nil, ErrBoundary
	}
	after, err := Observe(ctx, s.spec)
	if err != nil || (after.garmPID == topology.garmPID && after.processStart == topology.processStart) {
		return nil, ErrBoundary
	}
	actual, err := measureStockConfiguration(s.spec)
	if err != nil || !reflect.DeepEqual(actual, sealed) {
		return nil, ErrBoundary
	}
	mounted, err = measureSealedDirectory(fmt.Sprintf("/proc/%d/root/etc/garm", after.garmPID))
	if err != nil || !reflect.DeepEqual(mounted, sealed) {
		return nil, ErrBoundary
	}
	afterProfiles, err := final.profiles.read(ctx)
	if err != nil || afterProfiles.Lineage != profiles.Lineage || afterProfiles.Sequence < profiles.Sequence || afterProfiles.ProfileDigest != profiles.ProfileDigest {
		return nil, ErrBoundary
	}
	record.State = "revalidated"
	if files.create("controlled-restart-complete.json", struct {
		Record restartRecord
		PID    int
		Start  uint64
		At     time.Time
	}{record, after.garmPID, after.processStart, time.Now().UTC()}) != nil {
		return nil, ErrBoundary
	}
	valid = true
	return &LoadedStockBinding{observer: final, session: fresh, files: sealed, pid: after.garmPID, start: after.processStart}, nil
}

// LoadedStockBinding is an in-process checked startup binding, not a native or
// management capacity permit. It cannot be reconstructed from a receipt alone.
type LoadedStockBinding struct {
	observer *ProviderPathObserver
	session  *Session
	files    map[string]sealedFileIdentity
	pid      int
	start    uint64
}

func (b *LoadedStockBinding) Close() {
	if b != nil && b.session != nil {
		b.session.Close()
	}
}
func (b *LoadedStockBinding) Check(ctx context.Context) error {
	if b == nil || b.observer == nil || b.session == nil {
		return ErrBoundary
	}
	if _, err := b.observer.Check(ctx); err != nil {
		return ErrBoundary
	}
	topology, err := Observe(ctx, b.session.spec)
	if err != nil || topology.garmPID != b.pid || topology.processStart != b.start {
		b.Close()
		return ErrBoundary
	}
	mounted, err := measureSealedDirectory(fmt.Sprintf("/proc/%d/root/etc/garm", topology.garmPID))
	if err != nil || !reflect.DeepEqual(mounted, b.files) {
		b.Close()
		return ErrBoundary
	}
	current, err := measureStockConfiguration(b.session.spec)
	if err != nil || !reflect.DeepEqual(current, b.files) {
		b.Close()
		return ErrBoundary
	}
	return nil
}

func measureStockConfiguration(s Specification) (map[string]sealedFileIdentity, error) {
	path := filepath.Join(filepath.Dir(s.AdminSecretDirectory), "garm-config")
	if rootControlledAncestors(filepath.Dir(path)) != nil {
		return nil, ErrBoundary
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !privateDeploymentParent(parent) {
		return nil, ErrBoundary
	}
	return measureSealedDirectory(path)
}
func measureSealedDirectory(path string) (map[string]sealedFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrBoundary
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 65530 {
		return nil, ErrBoundary
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrBoundary
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, ErrBoundary
	}
	defer dir.Close()
	entries, err := dir.ReadDir(2049)
	if err != nil || len(entries) < 3 || len(entries) > 2048 {
		return nil, ErrBoundary
	}
	out := make(map[string]sealedFileIdentity, len(entries)+1)
	out["."] = sealedFileIdentity{Device: uint64(st.Dev), Inode: st.Ino}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Base(name) != name || entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrBoundary
		}
		before, err := providerPathNoLinks(root, name)
		if err != nil {
			return nil, ErrBoundary
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, ErrBoundary
		}
		opened, e := file.Stat()
		if e != nil || !os.SameFile(before, opened) {
			file.Close()
			return nil, ErrBoundary
		}
		identity, e := measuredFileIdentity(file)
		file.Close()
		if e != nil {
			return nil, ErrBoundary
		}
		after, e := providerPathNoLinks(root, name)
		if e != nil || !os.SameFile(before, after) {
			return nil, ErrBoundary
		}
		out[name] = identity
	}
	return out, nil
}

type restartJournalEvent struct {
	Intent  string
	Index   int
	Step    restartStep
	Pending bool
}

func waitRestartSession(ctx context.Context, s Specification) (*Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		session, err := OpenSession(ctx, s)
		if err == nil {
			return session, nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ErrBoundary
		case <-timer.C:
		}
	}
}

func validateSealedConfigSet(files map[string]sealedFileIdentity, names []string) error {
	allowed := map[string]bool{".": true, "garm.toml": true, "api-cert.pem": true, "api-key.pem": true}
	for _, name := range names {
		if !providerName.MatchString(name) {
			return ErrBoundary
		}
		allowed["provider-"+name+".json"] = true
		for _, role := range []string{"stock", "maintenance"} {
			for _, part := range []string{"ca", "cert", "key"} {
				allowed["provider-"+name+"-"+role+"-"+part+".pem"] = true
			}
		}
	}
	if len(files) != len(allowed) {
		return ErrBoundary
	}
	for path := range files {
		if !allowed[path] {
			return ErrBoundary
		}
	}
	return nil
}

// RestartParticipant is the operational listener lifecycle. Implementations do
// not supply proof: final independent kernel/TLS observations remain mandatory.
type RestartParticipant interface {
	Suspend(context.Context) error
	Resume(context.Context) error
}

func privateDeploymentParent(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
