package firstboot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"sort"
	"strings"
	"sync"
	"time"
)

type ManagedRotationPlan struct {
	Remote     RotationPlan        `json:"remote"`
	AdminAlias string              `json:"admin_credential_alias"`
	OldID      string              `json:"old_target_id"`
	OldConfig  sshinventory.Config `json:"old_config"`
	NewID      string              `json:"new_target_id"`
	NewConfig  sshinventory.Config `json:"new_config"`
}
type ManagedRotationTarget struct {
	Plan     ManagedRotationPlan
	Artifact []byte
}
type ManagedRotationOperation struct {
	TargetID  string           `json:"target_id"`
	Digest    string           `json:"review_digest"`
	RequestID string           `json:"request_id"`
	State     string           `json:"state"`
	Phase     string           `json:"phase"`
	Attempts  int              `json:"attempts"`
	Receipt   *RotationReceipt `json:"receipt,omitempty"`
}
type ManagedRotationView struct {
	ID        string                    `json:"id"`
	Plan      ManagedRotationPlan       `json:"plan"`
	Digest    string                    `json:"review_digest"`
	Operation *ManagedRotationOperation `json:"operation,omitempty"`
	Running   bool                      `json:"running"`
}
type ManagedRotationStore interface {
	ManagedRotations(context.Context) (map[string]ManagedRotationOperation, error)
	BeginManagedRotation(context.Context, string, string, string, bool) (ManagedRotationOperation, bool, error)
	MarkManagedRotation(context.Context, string, string, string, string) error
	CompleteManagedRotation(context.Context, string, string, string, RotationReceipt) error
}
type RotationRegistry interface {
	CheckRotationReplacement(context.Context, string, string, string, string, sshinventory.Config, string) error
	Review(string, sshinventory.Config) (sshinventory.Target, error)
	ReplaceForRotation(context.Context, string, string, string, string, sshinventory.Config, string) (sshinventory.Target, error)
}

func ManagedRotationDigest(p ManagedRotationPlan) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidateManagedRotation(t ManagedRotationTarget) error {
	p := t.Plan

	r := RotationRequest{Plan: p.Remote, ReviewDigest: RotationDigest(p.Remote), Artifact: t.Artifact, Phase: "stage"}
	if ValidateRotation(r) != nil || !targetName.MatchString(p.AdminAlias) || !targetName.MatchString(p.OldID) || !targetName.MatchString(p.NewID) || p.OldID == p.NewID || p.OldConfig.CredentialRef == p.NewConfig.CredentialRef {
		return ErrInvalid
	}
	for _, c := range []sshinventory.Config{p.OldConfig, p.NewConfig} {
		if !targetName.MatchString(c.CredentialRef) || c.Address != p.Remote.Installation.InventoryAddress || c.Port != p.Remote.Installation.InventoryPort || c.User != "garm-inventory-listener" || strings.TrimSpace(c.HostKey) != strings.TrimSpace(p.Remote.ListenerHostKey) {
			return ErrInvalid
		}
	}
	return nil
}

type ManagedRotationService struct {
	targets          map[string]ManagedRotationTarget
	store            ManagedRotationStore
	registry         RotationRegistry
	admin, inventory Resolver
	phase            func(context.Context, RotationRequest, ssh.Signer) (RotationReceipt, error)
	probe            func(context.Context, sshinventory.Config, ssh.Signer) error
	mu               sync.Mutex
	running          map[string]bool
	closed           bool
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func NewManagedRotationService(targets map[string]ManagedRotationTarget, store ManagedRotationStore, registry RotationRegistry, admin, inventory Resolver) (*ManagedRotationService, error) {
	return newManagedRotationService(targets, store, registry, admin, inventory, rotationPhase, func(ctx context.Context, c sshinventory.Config, k ssh.Signer) error {
		client, e := sshinventory.New(c, func(context.Context, string) (ssh.Signer, error) { return k, nil })
		if e != nil {
			return e
		}
		_, e = client.Probe(ctx)
		return e
	})
}
func newManagedRotationService(targets map[string]ManagedRotationTarget, store ManagedRotationStore, registry RotationRegistry, admin, inventory Resolver, phase func(context.Context, RotationRequest, ssh.Signer) (RotationReceipt, error), probe func(context.Context, sshinventory.Config, ssh.Signer) error) (*ManagedRotationService, error) {
	if len(targets) == 0 || len(targets) > 8 || store == nil || registry == nil || admin == nil || inventory == nil || phase == nil || probe == nil {
		return nil, ErrState
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &ManagedRotationService{targets: map[string]ManagedRotationTarget{}, store: store, registry: registry, admin: admin, inventory: inventory, phase: phase, probe: probe, running: map[string]bool{}, ctx: ctx, cancel: cancel}
	requests := map[string]bool{}
	for id, t := range targets {
		if !targetName.MatchString(id) || ValidateManagedRotation(t) != nil || requests[t.Plan.Remote.RequestID] {
			cancel()
			return nil, ErrState
		}
		requests[t.Plan.Remote.RequestID] = true
		if _, e := registry.Review(t.Plan.NewID, t.Plan.NewConfig); e != nil {
			cancel()
			return nil, ErrState
		}
		t.Artifact = append([]byte(nil), t.Artifact...)
		s.targets[id] = t
	}
	return s, nil
}
func (s *ManagedRotationService) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *ManagedRotationService) Targets(ctx context.Context) ([]ManagedRotationView, error) {
	ops, e := s.store.ManagedRotations(ctx)
	if e != nil {
		return nil, ErrState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ManagedRotationView{}
	for id, t := range s.targets {
		v := ManagedRotationView{ID: id, Plan: t.Plan, Digest: ManagedRotationDigest(t.Plan), Running: s.running[id]}
		if op, ok := ops[id]; ok {
			if op.Receipt != nil && op.Digest == v.Digest {
				r := RotationRequest{Plan: t.Plan.Remote, ReviewDigest: RotationDigest(t.Plan.Remote), Phase: "commit"}
				if validateRotationReceipt(r, *op.Receipt) != nil {
					return nil, ErrState
				}
			}
			copy := op
			v.Operation = &copy
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *ManagedRotationService) Start(ctx context.Context, id, digest, request string, retry bool) (ManagedRotationOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok || s.closed || s.running[id] || digest != ManagedRotationDigest(t.Plan) || request != t.Plan.Remote.RequestID {
		return ManagedRotationOperation{}, ErrState
	}
	op, run, e := s.store.BeginManagedRotation(ctx, id, digest, request, retry)
	if e != nil {
		return op, ErrState
	}
	if !run {
		if op.Receipt != nil {
			r := RotationRequest{Plan: t.Plan.Remote, ReviewDigest: RotationDigest(t.Plan.Remote), Phase: "commit"}
			if validateRotationReceipt(r, *op.Receipt) != nil {
				return op, ErrState
			}
		}
		return op, nil
	}
	s.running[id] = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); delete(s.running, id); s.mu.Unlock() }()
		c, cancel := context.WithTimeout(s.ctx, 7*time.Minute)
		defer cancel()
		p := t.Plan
		if s.registry.CheckRotationReplacement(c, request, p.OldID, sshinventory.ReviewDigest(p.OldID, p.OldConfig), p.NewID, p.NewConfig, sshinventory.ReviewDigest(p.NewID, p.NewConfig)) != nil {
			return
		}
		admin, e := s.admin(c, p.AdminAlias)
		if e != nil || admin == nil || ssh.FingerprintSHA256(admin.PublicKey()) != p.Remote.Installation.Base.AdminKeyFingerprint {
			return
		}
		next, e := s.inventory(c, p.NewConfig.CredentialRef)
		if e != nil || next == nil || strings.TrimSpace(string(ssh.MarshalAuthorizedKey(next.PublicKey()))) != strings.TrimSpace(p.Remote.NewKey) {
			return
		}
		mark := func(phase string) bool { return s.store.MarkManagedRotation(c, id, digest, request, phase) == nil }
		r := RotationRequest{Plan: p.Remote, ReviewDigest: RotationDigest(p.Remote), Artifact: t.Artifact, Phase: "stage"}
		receipt, e := s.phase(c, r, admin)
		if e != nil || validateRotationReceipt(r, receipt) != nil || !mark("staged") {
			return
		}
		if s.probe(c, p.NewConfig, next) != nil || !mark("proved") {
			return
		}
		replace := func() error {
			_, e := s.registry.ReplaceForRotation(c, request, p.OldID, sshinventory.ReviewDigest(p.OldID, p.OldConfig), p.NewID, p.NewConfig, sshinventory.ReviewDigest(p.NewID, p.NewConfig))
			return e
		}
		if replace() != nil || !mark("registry_switched") {
			return
		}
		r.Phase = "commit"
		receipt, e = s.phase(c, r, admin)
		if e != nil || validateRotationReceipt(r, receipt) != nil || !mark("committed") {
			return
		}
		if s.probe(c, p.NewConfig, next) != nil || replace() != nil {
			return
		}
		finish, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.store.CompleteManagedRotation(finish, id, digest, request, receipt)
	}()
	return op, nil
}
