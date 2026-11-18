package firstboot

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"regexp"
	"sort"
	"sync"
	"time"
)

var targetName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var requestName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var ErrState = errors.New("bootstrap review or durable state unavailable")

type Operation struct {
	TargetID  string   `json:"target_id"`
	Digest    string   `json:"review_digest"`
	RequestID string   `json:"request_id"`
	State     string   `json:"state"`
	Attempts  int      `json:"attempts"`
	Receipt   *Receipt `json:"receipt,omitempty"`
}
type Store interface {
	FirstbootOperations(context.Context) (map[string]Operation, error)
	BeginFirstboot(context.Context, string, string, string, bool) (Operation, bool, error)
	CompleteFirstboot(context.Context, string, string, string, Receipt) error
}
type DeploymentTarget struct {
	Request         Request
	CredentialAlias string
	PreflightSHA256 string
}
type Target struct {
	ID              string           `json:"id"`
	CredentialAlias string           `json:"credential_alias"`
	Plan            Plan             `json:"plan"`
	Digest          string           `json:"review_digest"`
	ArtifactBytes   int              `json:"artifact_bytes"`
	Operation       *Operation       `json:"operation,omitempty"`
	Running         bool             `json:"running"`
	Preflight       *PreflightRecipe `json:"preflight,omitempty"`
}
type Resolver func(context.Context, string) (ssh.Signer, error)
type Service struct {
	targets map[string]DeploymentTarget
	store   Store
	resolve Resolver
	execute func(context.Context, Request, ssh.Signer) (Receipt, error)
	mu      sync.Mutex
	running map[string]bool
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewService(targets map[string]DeploymentTarget, store Store, resolve Resolver) (*Service, error) {
	return newService(targets, store, resolve, Execute)
}
func newService(targets map[string]DeploymentTarget, store Store, resolve Resolver, run func(context.Context, Request, ssh.Signer) (Receipt, error)) (*Service, error) {
	if len(targets) == 0 || len(targets) > 8 || store == nil || resolve == nil || run == nil {
		return nil, ErrState
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{targets: map[string]DeploymentTarget{}, store: store, resolve: resolve, execute: run, running: map[string]bool{}, ctx: ctx, cancel: cancel}
	for id, t := range targets {
		if !targetName.MatchString(id) || !targetName.MatchString(t.CredentialAlias) || Validate(t.Request) != nil || t.PreflightSHA256 != "" && !preflightHash(t.PreflightSHA256) {
			cancel()
			return nil, ErrState
		}
		t.Request.Artifact = append([]byte(nil), t.Request.Artifact...)
		s.targets[id] = t
	}
	return s, nil
}
func (s *Service) Close() { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock(); s.wg.Wait() }
func (s *Service) Targets(ctx context.Context) ([]Target, error) {
	ops, e := s.store.FirstbootOperations(ctx)
	if e != nil {
		return nil, ErrState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Target{}
	for id, t := range s.targets {
		v := Target{ID: id, CredentialAlias: t.CredentialAlias, Plan: t.Request.Plan, Digest: t.Request.ReviewDigest, ArtifactBytes: len(t.Request.Artifact), Running: s.running[id]}
		if t.PreflightSHA256 != "" {
			recipe := preflightRecipe(t)
			v.Preflight = &recipe
		}
		if op, ok := ops[id]; ok {
			if op.Receipt != nil && op.Digest == t.Request.ReviewDigest && ValidateReceipt(t.Request.Plan, op.Digest, *op.Receipt) != nil {
				return nil, ErrState
			}
			copy := op
			v.Operation = &copy
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Start durably records unknown BEFORE resolving credentials or contacting SSH.
// Duplicate approval never retries an uncertain operation automatically.
func (s *Service) Start(ctx context.Context, id, digest, request string, retry bool) (Operation, error) {
	if !requestName.MatchString(request) {
		return Operation{}, ErrState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok || s.closed || digest != t.Request.ReviewDigest {
		return Operation{}, ErrState
	}
	if s.running[id] {
		return Operation{}, ErrState
	}
	op, run, e := s.store.BeginFirstboot(ctx, id, digest, request, retry)
	if e != nil {
		return Operation{}, ErrState
	}
	if !run {
		return op, nil
	}
	s.running[id] = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); delete(s.running, id); s.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(s.ctx, 190*time.Second)
		defer cancel()
		key, e := s.resolve(ctx, t.CredentialAlias)
		if e != nil || key == nil || ssh.FingerprintSHA256(key.PublicKey()) != t.Request.Plan.Base.AdminKeyFingerprint {
			return
		}
		receipt, err := s.execute(ctx, t.Request, key)
		if err != nil || ValidateReceipt(t.Request.Plan, t.Request.ReviewDigest, receipt) != nil {
			return
		}
		finish, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.store.CompleteFirstboot(finish, id, digest, request, receipt)
	}()
	return op, nil
}
