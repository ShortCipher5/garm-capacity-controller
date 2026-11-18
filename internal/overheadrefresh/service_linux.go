package overheadrefresh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

var ErrService = errors.New("owner collection unavailable; reload durable receipts")
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var requestPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Recipe struct {
	ID, TrustDigest string
	Targets         []Target
}
type TargetChoice struct {
	Host            string `json:"host"`
	BindingSequence int64  `json:"binding_sequence"`
	Kind            string `json:"kind"`
}
type Choice struct {
	ID       string         `json:"id"`
	Revision string         `json:"revision"`
	Targets  []TargetChoice `json:"targets"`
}
type Operation struct {
	RequestID  string    `json:"request_id"`
	Recipe     string    `json:"recipe"`
	Revision   string    `json:"recipe_revision"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Results    []Result  `json:"results"`
}

// Service owns a metadata-only journal; canonical charges stay exclusively in
// SQLite. A pending receipt after restart is unknown, never a replay instruction.
type Service struct {
	mu         sync.Mutex
	store      *admission.SQLiteStore
	journal    *operationJournal
	recipes    map[string]Recipe
	choices    []Choice
	operations map[string]Operation
	running    string
	closed     bool
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewService(ctx context.Context, store *admission.SQLiteStore, directory string, recipes []Recipe) (*Service, error) {
	if ctx == nil || ctx.Err() != nil || store == nil || !store.RuntimeLineageBound() || len(recipes) == 0 || len(recipes) > 100 {
		return nil, ErrService
	}
	s := &Service{store: store, recipes: map[string]Recipe{}, operations: map[string]Operation{}, done: make(chan struct{})}
	for _, r := range recipes {
		if !namePattern.MatchString(r.ID) || s.recipes[r.ID].ID != "" || len(r.Targets) == 0 || len(r.Targets) > 10 || !digestString(r.TrustDigest) {
			return nil, ErrService
		}
		r.Targets = append([]Target(nil), r.Targets...)
		choice := Choice{ID: r.ID, Targets: []TargetChoice{}}
		seen := map[int64]bool{}
		for i, t := range r.Targets {
			u, e := url.Parse(t.Endpoint)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || t.BindingSequence <= 0 || seen[t.BindingSequence] || t.TLS == nil || t.TLS.RootCAs == nil || len(t.TLS.Certificates) != 1 || t.TLS.InsecureSkipVerify || t.TLS.VerifyConnection != nil || t.TLS.VerifyPeerCertificate != nil || t.TLS.GetClientCertificate != nil || (t.Kind != "domain" && t.Kind != "artifact" && t.Kind != "both") {
				return nil, ErrService
			}
			b, e := store.LookupNativeDomainBinding(ctx, t.BindingSequence)
			if e != nil {
				return nil, ErrService
			}
			h, e := store.LookupHostBinding(ctx, b.Spec.Host)
			if e != nil || h.Sequence != b.Spec.HostBindingSequence {
				return nil, ErrService
			}
			seen[t.BindingSequence] = true
			trust, e := ownedTLS(t.TLS)
			if e != nil {
				return nil, ErrService
			}
			r.Targets[i].TLS = trust
			choice.Targets = append(choice.Targets, TargetChoice{b.Spec.Host, t.BindingSequence, t.Kind})
		}
		// Only private endpoint and trust hashes enter this review digest, never key material.
		type targetRevision struct {
			Binding        int64
			Endpoint, Kind string
			Certificates   [][]byte
		}
		targets := []targetRevision{}
		for _, t := range r.Targets {
			targets = append(targets, targetRevision{t.BindingSequence, t.Endpoint, t.Kind, t.TLS.Certificates[0].Certificate})
		}
		raw, _ := json.Marshal(struct {
			ID, Trust, Lineage string
			Targets            []targetRevision
		}{r.ID, r.TrustDigest, store.RuntimeLineageFingerprint(), targets})
		sum := sha256.Sum256(raw)
		choice.Revision = hex.EncodeToString(sum[:])
		s.recipes[r.ID] = r
		s.choices = append(s.choices, choice)
	}
	sort.Slice(s.choices, func(i, j int) bool { return s.choices[i].ID < s.choices[j].ID })
	j, ops, e := openOperationJournal(directory, store.RuntimeLineageFingerprint())
	if e != nil {
		return nil, ErrService
	}
	s.journal = j
	s.operations = ops
	return s, nil
}
func digestString(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == s
}
func cloneOperation(v Operation) Operation { v.Results = append([]Result{}, v.Results...); return v }

type View struct {
	Recipes           []Choice    `json:"recipes"`
	Operations        []Operation `json:"operations"`
	Running           bool        `json:"running"`
	CanStart          bool        `json:"can_start"`
	BlockedReason     string      `json:"blocked_reason"`
	CollectionEnabled bool        `json:"collection_enabled"`
	AccountingOnly    bool        `json:"accounting_only"`
}

func (s *Service) Snapshot() (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.journal == nil || s.store == nil {
		return View{}, ErrService
	}
	choices := append([]Choice{}, s.choices...)
	for i := range choices {
		choices[i].Targets = append([]TargetChoice{}, choices[i].Targets...)
	}
	ops := make([]Operation, 0, len(s.operations))
	for _, v := range s.operations {
		if v.State == "pending" && v.RequestID != s.running {
			v.State = "unknown"
		}
		ops = append(ops, cloneOperation(v))
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].StartedAt.After(ops[j].StartedAt) })
	if len(ops) > 100 {
		ops = ops[:100]
	}
	reason := ""
	if s.running != "" {
		reason = "collection_running"
	} else if len(s.operations) >= 10000 {
		reason = "receipt_history_full"
	}
	return View{Recipes: choices, Operations: ops, Running: s.running != "", CanStart: reason == "", BlockedReason: reason, CollectionEnabled: true, AccountingOnly: true}, nil
}
func (s *Service) Start(recipe, revision, request string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.journal == nil || s.store == nil || !requestPattern.MatchString(request) {
		return Operation{}, ErrService
	}
	r, ok := s.recipes[recipe]
	if !ok {
		return Operation{}, ErrService
	}
	found := false
	for _, c := range s.choices {
		if c.ID == recipe && c.Revision == revision {
			found = true
		}
	}
	if !found {
		return Operation{}, ErrService
	}
	if v, ok := s.operations[request]; ok {
		if v.Recipe != recipe || v.Revision != revision {
			return Operation{}, ErrService
		}
		if v.State == "pending" && request != s.running {
			v.State = "unknown"
		}
		return cloneOperation(v), nil
	}
	if s.running != "" || len(s.operations) >= 10000 {
		return Operation{}, ErrService
	}
	op := Operation{RequestID: request, Recipe: recipe, Revision: revision, State: "pending", StartedAt: time.Now().UTC(), Results: []Result{}}
	if s.journal.persist(op) != nil {
		s.closed = true // Rename may have completed before directory-sync failure.
		return Operation{}, ErrService
	}
	s.operations[request] = op
	s.running = request
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, r, op)
	return cloneOperation(op), nil
}
func (s *Service) run(ctx context.Context, r Recipe, op Operation) {
	results, e := Refresh(ctx, s.store, r.Targets)
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(s.done)
	s.cancel()
	s.cancel = nil
	s.running = ""
	op.Results = append([]Result{}, results...)
	op.FinishedAt = time.Now().UTC()
	op.State = "complete"
	if e != nil {
		op.State = "failed"
		if len(results) > 0 {
			op.State = "partial"
		}
	}
	if s.journal.persist(op) != nil { // The last durable receipt stays pending; never claim completion.
		s.closed = true
		return
	}
	s.operations[op.RequestID] = op
}
func (s *Service) Close() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	running := s.running != ""
	done := s.done
	s.closed = true
	s.mu.Unlock()
	if running {
		<-done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journal.close()
}

// NewRequestID is a convenience for trusted local callers; the browser may also
// generate these 16 random bytes. Identity is not authorization.
func NewRequestID() (string, error) {
	var b [16]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}

// Only the deployed roots and one fixed certificate/key are inputs. Do not retain
// caller callbacks, key-log writers, mutable keys or optional TLS metadata.
func ownedTLS(input *tls.Config) (*tls.Config, error) {
	if input == nil || input.RootCAs == nil || len(input.Certificates) != 1 || input.InsecureSkipVerify || input.VerifyConnection != nil || input.VerifyPeerCertificate != nil || input.GetClientCertificate != nil || input.GetConfigForClient != nil || input.EncryptedClientHelloRejectionVerify != nil {
		return nil, ErrService
	}
	cert := input.Certificates[0]
	raw, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if e != nil {
		return nil, ErrService
	}
	key, e := x509.ParsePKCS8PrivateKey(raw)
	for i := range raw {
		raw[i] = 0
	}
	if e != nil {
		return nil, ErrService
	}
	owned := tls.Certificate{PrivateKey: key}
	for _, der := range cert.Certificate {
		owned.Certificate = append(owned.Certificate, append([]byte(nil), der...))
	}
	if len(owned.Certificate) == 0 {
		return nil, ErrService
	}
	return &tls.Config{RootCAs: input.RootCAs.Clone(), Certificates: []tls.Certificate{owned}, MinVersion: tls.VersionTLS13}, nil
}
