//go:build linux

package nativeprovision

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeprepare"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
)

type State struct {
	Attempt      *Attempt               `json:"attempt,omitempty"`
	ReviewDigest string                 `json:"review_digest"`
	Status       string                 `json:"status"`
	Receipt      *nativeprepare.Receipt `json:"receipt,omitempty"`
}

// Service owns one immutable deployment target/bundle and a protected local
// journal. Creation of intent is permanent; restarts permit inspection only.
type Service struct {
	busy    atomic.Bool
	closed  atomic.Bool
	life    context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	bundle  *Bundle
	plan    Plan
	dir     string
	uid     uint32
	execute func(context.Context, Plan, string, ssh.Signer, bool) (nativeprepare.Receipt, error)
}

func NewService(b *Bundle, t Target, journalDirectory string) (*Service, error) {
	p, e := b.Plan(t)
	if e != nil || protected(journalDirectory, uint32(os.Geteuid())) != nil {
		return nil, ErrInvalid
	}
	life, cancel := context.WithCancel(context.Background())
	return &Service{bundle: b, plan: p, dir: journalDirectory, uid: uint32(os.Geteuid()), execute: b.Execute, life: life, cancel: cancel}, nil
}
func (s *Service) ReviewDigest() string { return Digest(s.plan) }
func (s *Service) lock() (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(s.dir, "native-preparation.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, ErrInvalid
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		f.Close()
		return nil, ErrInvalid
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || meta.Uid != s.uid || meta.Nlink != 1 {
		f.Close()
		return nil, ErrInvalid
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (s *Service) write(name string, data []byte) error {
	f, e := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = syncDirectory(s.dir)
	}
	return e
}
func (s *Service) read(name string) ([]byte, error) {
	f, e := os.OpenFile(filepath.Join(s.dir, name), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 16384 {
		return nil, ErrInvalid
	}
	meta, ok := st.Sys().(*syscall.Stat_t)
	if !ok || meta.Uid != s.uid || meta.Nlink != 1 {
		return nil, ErrInvalid
	}
	b := make([]byte, st.Size())
	_, e = f.ReadAt(b, 0)
	return b, e
}

// Apply performs one explicit prepare, or explicit inspect-only recovery. The
// signer resolver runs only after durable unknown exists. It is not a retry API.
func (s *Service) Apply(ctx context.Context, review string, inspect bool, resolve func() (ssh.Signer, error)) (State, error) {
	return s.apply(ctx, review, inspect, resolve, nil)
}
func (s *Service) apply(ctx context.Context, review string, inspect bool, resolve func() (ssh.Signer, error), ready chan State) (State, error) {
	signalled := false
	defer func() {
		if ready != nil && !signalled {
			ready <- State{}
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || review != s.ReviewDigest() || resolve == nil {
		return State{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, e := s.lock()
	if e != nil {
		return State{}, e
	}
	defer lock.Close()
	if s.currentRegisteredPlan() != nil {
		return State{}, ErrInvalid
	}
	id := s.plan.Manifest.ID
	intent := id + ".intent.json"
	state := State{ReviewDigest: review, Status: "unknown"}
	raw, _ := json.Marshal(state)
	previous, e := s.read(intent)
	if os.IsNotExist(e) {
		if seq, se := s.latestSequence(); se != nil || seq != 0 {
			return state, ErrInvalid
		}
		if inspect {
			return state, ErrInvalid
		}
		if s.write(intent, raw) != nil {
			return state, ErrUnknown
		}
	} else if e != nil || string(previous) != string(raw) {
		return state, ErrInvalid
	} else if !inspect {
		return state, ErrUnknown
	}
	attempt, e := s.beginAttempt(inspect)
	if e != nil {
		return state, e
	}
	state.Attempt = &attempt
	failed := func() (State, error) {
		if result, e := s.finishAttempt(attempt, nil); e == nil {
			state.Attempt = &result
		}
		return state, ErrUnknown
	}
	if ready != nil {
		ready <- state
		signalled = true
	}
	// Completed state remains historical. Inspection deliberately rechecks remote
	// bytes instead of trusting this receipt as current target attestation.
	key, e := resolve()
	if e != nil {
		return failed()
	}
	receipt, e := s.execute(ctx, s.plan, review, key, inspect)
	if e != nil {
		return failed()
	}
	if nativeprepare.ValidateReceipt(s.plan.Manifest, receipt) != nil {
		return failed()
	}
	complete := State{ReviewDigest: review, Status: "prepared_not_activated", Receipt: &receipt}
	out, _ := json.Marshal(complete)
	name := id + ".receipt.json"
	old, e := s.read(name)
	if os.IsNotExist(e) {
		if s.write(name, out) != nil {
			return failed()
		}
	} else if e != nil || string(old) != string(out) {
		return failed()
	}
	result, e := s.finishAttempt(attempt, &receipt)
	if e != nil {
		return state, ErrUnknown
	}
	complete.Attempt = &result
	return complete, nil
}

// Start returns only once unknown has been synced. Work is bounded independently
// of a dismissed browser; a failed response never authorizes a second upload.
func (s *Service) Start(ctx context.Context, review string, inspect bool, resolve func() (ssh.Signer, error)) (State, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || review != s.ReviewDigest() || resolve == nil || s.closed.Load() || !s.busy.CompareAndSwap(false, true) {
		return State{}, ErrInvalid
	}
	ready := make(chan State, 1)
	done := make(chan struct{})
	go func() {
		defer func() { s.busy.Store(false); close(done) }()
		work, cancel := context.WithCancel(ctx)
		defer cancel()
		if s.life != nil {
			stop := context.AfterFunc(s.life, cancel)
			defer stop()
		}
		s.apply(work, review, inspect, resolve, ready)
	}()
	state := <-ready
	if state.Status != "unknown" {
		<-done
		return State{}, ErrInvalid
	}
	return state, nil
}
func (s *Service) historicalStatus() (State, error) {
	if s == nil {
		return State{}, ErrInvalid
	}
	state := State{ReviewDigest: s.ReviewDigest(), Status: "not_started"}
	raw, e := s.read(s.plan.Manifest.ID + ".intent.json")
	if os.IsNotExist(e) {
		if seq, se := s.latestSequence(); se != nil || seq != 0 {
			return State{}, ErrInvalid
		}
		return state, nil
	}
	if e != nil {
		return State{}, ErrInvalid
	}
	var intent State
	if json.Unmarshal(raw, &intent) != nil || intent.ReviewDigest != state.ReviewDigest || intent.Status != "unknown" || intent.Receipt != nil {
		return State{}, ErrInvalid
	}
	state.Status = "unknown"
	raw, e = s.read(s.plan.Manifest.ID + ".receipt.json")
	if os.IsNotExist(e) {
		return state, nil
	}
	if e != nil {
		return State{}, ErrInvalid
	}
	var completed State
	if json.Unmarshal(raw, &completed) != nil || completed.ReviewDigest != state.ReviewDigest || completed.Status != "prepared_not_activated" || completed.Receipt == nil || nativeprepare.ValidateReceipt(s.plan.Manifest, *completed.Receipt) != nil {
		return State{}, ErrInvalid
	}
	return completed, nil
}

type View struct {
	AdminUser            string `json:"admin_user"`
	HostKeyFingerprint   string `json:"host_key_fingerprint"`
	AdminKeyFingerprint  string `json:"admin_key_fingerprint"`
	ManifestDigest       string `json:"manifest_digest"`
	ScriptSHA256         string `json:"script_sha256"`
	TargetID             string `json:"target_id"`
	Address              string `json:"address"`
	Port                 uint16 `json:"port"`
	PreparationID        string `json:"preparation_id"`
	ReviewDigest         string `json:"review_digest"`
	BundleBytes          int64  `json:"bundle_bytes"`
	State                State  `json:"state"`
	ActivationAuthorized bool   `json:"activation_authorized"`
}

func (s *Service) View(id string) (View, error) {
	state, e := s.Status()
	if e != nil {
		return View{}, e
	}
	var total int64
	for _, f := range s.plan.Files {
		total += f.Size
	}
	host, _ := hostbootstrap.PublicKey(s.plan.Target.HostKey)
	md, _ := nativeprepare.ManifestDigest(s.plan.Manifest)
	return View{TargetID: id, Address: s.plan.Target.Address, Port: s.plan.Target.Port, PreparationID: s.plan.Manifest.ID, ReviewDigest: s.ReviewDigest(), BundleBytes: total, State: state, AdminUser: s.plan.Target.AdminUser, HostKeyFingerprint: ssh.FingerprintSHA256(host), AdminKeyFingerprint: s.plan.Target.AdminKeyFingerprint, ManifestDigest: md, ScriptSHA256: s.plan.ScriptSHA256}, nil
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closed.Store(true)
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundle.Close()
}

func (s *Service) Status() (State, error) {
	if s == nil || s.currentRegisteredPlan() != nil {
		return State{}, ErrInvalid
	}
	state, e := s.historicalStatus()
	if e != nil {
		return State{}, e
	}
	attempt, e := s.latestAttempt()
	if e != nil {
		return State{}, e
	}
	again, e := s.historicalStatus()
	if e != nil {
		return State{}, e
	}
	before, _ := json.Marshal(state)
	after, _ := json.Marshal(again)
	if string(before) != string(after) || (state.Status == "not_started" && attempt != nil) {
		return State{}, ErrInvalid
	}
	state.Attempt = attempt
	return state, nil
}
