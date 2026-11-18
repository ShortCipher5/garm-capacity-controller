package sshinventory

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/ssh"
	"regexp"
	"sort"
	"time"
)

var ErrReview = errors.New("SSH target review changed or unavailable")
var ErrBusy = errors.New("SSH inventory probe already running")
var targetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var requestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Audit must durably record requested before Probe is allowed to contact a host.
type Audit func(context.Context, string, string, string, string, bool) error

type Target struct {
	ID                 string `json:"id"`
	Address            string `json:"address"`
	Port               uint16 `json:"port"`
	User               string `json:"user"`
	CredentialRef      string `json:"credential_ref"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	ReviewDigest       string `json:"review_digest"`
}
type ProbeResult struct {
	TargetID     string `json:"target_id"`
	ReviewDigest string `json:"review_digest"`
	Inventory    Result `json:"inventory"`
}
type Service struct {
	targets  map[string]Target
	clients  map[string]*Client
	audit    Audit
	gate     chan struct{}
	registry *Registry
	resolve  KeyResolver
}

// NewService freezes deployment-authorized address/user/key/credential pairings.
// No browser request can choose a different destination or credential.
func NewService(configs map[string]Config, resolve KeyResolver, audit Audit) (*Service, error) {
	if len(configs) > 32 || audit == nil {
		return nil, ErrInventory
	}
	s := &Service{targets: map[string]Target{}, clients: map[string]*Client{}, audit: audit, gate: make(chan struct{}, 1), resolve: resolve}
	for id, c := range configs {
		if !targetID.MatchString(id) {
			return nil, ErrInventory
		}
		client, e := New(c, resolve)
		if e != nil {
			return nil, e
		}
		s.targets[id] = describeTarget(id, c, client)
		s.clients[id] = client
	}
	return s, nil
}

// ReviewDigest binds a durable approval to the exact target ID and configuration.
func ReviewDigest(id string, c Config) string {
	data, _ := json.Marshal(struct {
		ID     string
		Config Config
	}{id, c})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func describeTarget(id string, c Config, client *Client) Target {
	return Target{ID: id, Address: c.Address, Port: c.Port, User: c.User, CredentialRef: c.CredentialRef, HostKeyFingerprint: ssh.FingerprintSHA256(client.hostKey), ReviewDigest: ReviewDigest(id, c)}
}

// SetRegistry is startup-only. Dynamic targets are revalidated against current scope on every use.
func (s *Service) SetRegistry(r *Registry) { s.registry = r }
func (s *Service) ListTargets(ctx context.Context) ([]Target, error) {
	out := s.Targets()
	if s.registry != nil {
		configs, e := s.registry.targets(ctx)
		if e != nil {
			return nil, e
		}
		for id, c := range configs {
			client, e := New(c, s.resolve)
			if e != nil {
				return nil, e
			}
			out = append(out, describeTarget(id, c, client))
		}
	}
	if len(out) > 32 {
		return nil, ErrInventory
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Service) Targets() []Target {
	out := make([]Target, 0, len(s.targets))
	for _, t := range s.targets {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Service) Probe(ctx context.Context, id, digest, request string) (ProbeResult, error) {
	if s == nil || ctx == nil || ctx.Err() != nil || !requestID.MatchString(request) {
		return ProbeResult{}, ErrReview
	}
	t, ok := s.targets[id]
	client := s.clients[id]
	if !ok && s.registry != nil {
		configs, e := s.registry.targets(ctx)
		if e != nil {
			return ProbeResult{}, ErrInventory
		}
		if c, found := configs[id]; found {
			client, e = New(c, s.resolve)
			if e != nil {
				return ProbeResult{}, ErrInventory
			}
			t = describeTarget(id, c, client)
			ok = true
		}
	}
	if !ok || len(digest) != 64 || subtle.ConstantTimeCompare([]byte(digest), []byte(t.ReviewDigest)) != 1 {
		return ProbeResult{}, ErrReview
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		return ProbeResult{}, ErrBusy
	}
	if s.audit(ctx, request, id, t.ReviewDigest, "requested", false) != nil {
		return ProbeResult{}, ErrInventory
	}
	result, e := client.Probe(ctx)
	finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.audit(finish, request, id, t.ReviewDigest, "completed", e == nil) != nil {
		return ProbeResult{}, ErrInventory
	}
	if e != nil || ctx.Err() != nil {
		return ProbeResult{}, ErrInventory
	}
	return ProbeResult{TargetID: id, ReviewDigest: t.ReviewDigest, Inventory: result}, nil
}
