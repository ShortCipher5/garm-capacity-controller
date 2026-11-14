package garmmanagement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const DisabledPoolPlanProtocol = "disabled-pool-staging-review/v1"

var ErrDisabledPoolSelection = errors.New("fixed disabled pool selection unavailable or changed")

// Protected startup configuration only. The browser selects a reference, never
// a repository/spec/URL/credential supplied as an arbitrary creation payload.
type DisabledPoolTemplate struct {
	Reference    string                   `json:"template_ref"`
	RepositoryID string                   `json:"repository_id"`
	Spec         garmapi.DisabledPoolSpec `json:"spec"`
}

func (t DisabledPoolTemplate) Clone() DisabledPoolTemplate {
	t.Spec.Tags = append([]string(nil), t.Spec.Tags...)
	return t
}

type DisabledPoolSelection struct {
	RequestID          string `json:"request_id"`
	TemplateReference  string `json:"template_ref"`
	Reason             string `json:"reason"`
	ExpectedPlanDigest string `json:"expected_plan_digest,omitempty"`
}
type DisabledPoolPlan struct {
	Protocol              string                   `json:"protocol"`
	RequestID             string                   `json:"request_id"`
	TemplateReference     string                   `json:"template_ref"`
	Reason                string                   `json:"reason"`
	RepositoryID          string                   `json:"repository_id"`
	TemplateDigest        string                   `json:"template_digest"`
	RepositoryFingerprint string                   `json:"repository_fingerprint"`
	Spec                  garmapi.DisabledPoolSpec `json:"spec"`
	PlanDigest            string                   `json:"plan_digest"`
}

func (p DisabledPoolPlan) Clone() DisabledPoolPlan {
	p.Spec.Tags = append([]string(nil), p.Spec.Tags...)
	return p
}
func stagingText(s string, n int) bool {
	if s == "" || len(s) > n || strings.TrimSpace(s) != s || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func stagingID(s string) bool { return validUUID(s) && s != "00000000-0000-0000-0000-000000000000" }
func (s DisabledPoolSelection) Validate(requireDigest bool) error {
	if !stagingID(s.RequestID) || !stagingText(s.TemplateReference, 128) || !stagingText(s.Reason, 256) || (s.ExpectedPlanDigest != "" || requireDigest) && !validJournalLineage(s.ExpectedPlanDigest) {
		return ErrDisabledPoolSelection
	}
	return nil
}

// Value consistency only. The real Create recomputes the digest in the actual
// manager/journal namespace; a plan received as JSON is never permission.
func (p DisabledPoolPlan) Validate() error {
	owned, e := garmapi.CanonicalDisabledPoolSpec(p.Spec)
	if p.Protocol != DisabledPoolPlanProtocol || (DisabledPoolSelection{RequestID: p.RequestID, TemplateReference: p.TemplateReference, Reason: p.Reason}).Validate(false) != nil || !stagingID(p.RepositoryID) || !validJournalLineage(p.RepositoryFingerprint) || !validJournalLineage(p.PlanDigest) || e != nil || !reflect.DeepEqual(p.Spec, owned) || p.TemplateDigest != disabledPoolTemplateDigest(DisabledPoolTemplate{p.TemplateReference, p.RepositoryID, owned}) {
		return ErrDisabledPoolSelection
	}
	return nil
}
func disabledPoolTemplateDigest(t DisabledPoolTemplate) string {
	raw, e := json.Marshal(struct {
		Protocol string               `json:"protocol"`
		Template DisabledPoolTemplate `json:"template"`
	}{"disabled-pool-staging-template/v1", t})
	if e != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func disabledPoolPlanDigest(p DisabledPoolPlan, management, lineage string) string {
	p.PlanDigest = ""
	raw, e := json.Marshal(struct {
		ManagementIdentity string           `json:"management_identity"`
		JournalLineage     string           `json:"journal_lineage"`
		Plan               DisabledPoolPlan `json:"plan"`
	}{management, lineage, p})
	if e != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(DisabledPoolPlanProtocol+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

type DisabledPoolStaging struct {
	self      *DisabledPoolStaging
	manager   *Manager
	remote    *garmapi.ManagementClient
	journal   *Journal
	templates map[string]DisabledPoolTemplate
}

// NewDisabledPoolStaging binds only fixed experimental library wiring. Client
// opt-in is not proof of the independently qualified source8 native profile,
// exclusive writer custody, job emptiness, route installation or admission.
func NewDisabledPoolStaging(m *Manager, templates []DisabledPoolTemplate) (*DisabledPoolStaging, error) {
	if m == nil || m.journal == nil || m.remote == nil || len(templates) == 0 || len(templates) > 128 {
		return nil, ErrDisabledPoolSelection
	}
	remote, ok := m.remote.(*garmapi.ManagementClient)
	if !ok || remote == nil || !remote.DisabledPoolCreationEnabled() || !validJournalLineage(remote.ManagementIdentity()) || !validJournalLineage(m.journal.Lineage()) {
		return nil, ErrDisabledPoolSelection
	}
	s := &DisabledPoolStaging{manager: m, remote: remote, journal: m.journal, templates: map[string]DisabledPoolTemplate{}}
	for _, t := range templates {
		owned, e := garmapi.CanonicalDisabledPoolSpec(t.Spec)
		if e != nil || !stagingText(t.Reference, 128) || !stagingID(t.RepositoryID) {
			return nil, ErrDisabledPoolSelection
		}
		if _, exists := s.templates[t.Reference]; exists {
			return nil, ErrDisabledPoolSelection
		}
		t.Spec = owned
		s.templates[t.Reference] = t
	}
	s.self = s
	return s, nil
}
func (s *DisabledPoolStaging) valid() bool {
	return s != nil && s.self == s && s.manager != nil && s.remote != nil && s.journal != nil && s.manager.remote == s.remote && s.manager.journal == s.journal && s.remote.DisabledPoolCreationEnabled()
}

func (s *DisabledPoolStaging) Templates() []DisabledPoolTemplate {
	if !s.valid() {
		return nil
	}
	out := make([]DisabledPoolTemplate, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reference < out[j].Reference })
	return out
}
func (s *DisabledPoolStaging) journalClear(ctx context.Context) error {
	if !s.valid() {
		return ErrJournal
	}
	return s.journal.transaction(ctx, func(tx *sql.Tx) error {
		var app, version, count, pending int
		var identity, lineage string
		if tx.QueryRow("PRAGMA application_id").Scan(&app) != nil || app != applicationID || tx.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 2 || tx.QueryRow("SELECT count(*) FROM journal_identity").Scan(&count) != nil || count != 1 || tx.QueryRow("SELECT lineage FROM journal_identity WHERE id=1").Scan(&lineage) != nil || lineage != s.journal.Lineage() || tx.QueryRow("SELECT count(*) FROM binding").Scan(&count) != nil || count != 1 || tx.QueryRow("SELECT identity FROM binding WHERE id=1").Scan(&identity) != nil || identity != s.remote.ManagementIdentity() {
			return ErrJournal
		}
		if tx.QueryRow("SELECT count(*) FROM intents WHERE state IN ('pending','unknown')").Scan(&pending) != nil {
			return ErrJournal
		}
		if pending != 0 {
			return ErrBlocked
		}
		return nil
	})
}

// Read-only plan. It neither creates a journal intent nor changes native/core
// scope, budget, route, allowlist, activation or instance/job state.
func (s *DisabledPoolStaging) ReviewCreate(ctx context.Context, selection DisabledPoolSelection) (DisabledPoolPlan, error) {
	if ctx == nil || ctx.Err() != nil || !s.valid() || selection.Validate(false) != nil {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	t, ok := s.templates[selection.TemplateReference]
	if !ok {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e := s.manager.enter(ctx); e != nil {
		return DisabledPoolPlan{}, e
	}
	defer s.manager.leave()
	if e := s.journalClear(ctx); e != nil {
		return DisabledPoolPlan{}, e
	}
	repo, e := s.remote.Repository(ctx, t.RepositoryID)
	if e != nil {
		return DisabledPoolPlan{}, e
	}
	if repo.ID != t.RepositoryID || !validJournalLineage(repo.Fingerprint) {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	if e := s.journalClear(ctx); e != nil {
		return DisabledPoolPlan{}, e
	}
	p := DisabledPoolPlan{Protocol: DisabledPoolPlanProtocol, RequestID: selection.RequestID, TemplateReference: t.Reference, Reason: selection.Reason, RepositoryID: t.RepositoryID, TemplateDigest: disabledPoolTemplateDigest(t), RepositoryFingerprint: repo.Fingerprint, Spec: t.Clone().Spec}
	p.PlanDigest = disabledPoolPlanDigest(p, s.remote.ManagementIdentity(), s.journal.Lineage())
	if p.Validate() != nil || !s.valid() || ctx.Err() != nil {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	return p, nil
}

// Create requires a fresh SAME manager/template/repository review match. The
// existing durable library owns the one POST and its irreversible UNKNOWN stop.
// A replay is historical, never a new mutation/empty-job/capacity/route proof.
func (s *DisabledPoolStaging) Create(ctx context.Context, selection DisabledPoolSelection) (StagedPool, error) {
	if ctx == nil || ctx.Err() != nil || !s.valid() || selection.Validate(true) != nil {
		return StagedPool{}, ErrDisabledPoolSelection
	}
	p, e := s.ReviewCreate(ctx, selection)
	if e != nil {
		return StagedPool{}, e
	}
	if p.PlanDigest != selection.ExpectedPlanDigest {
		return StagedPool{}, ErrDisabledPoolSelection
	}
	binding := &disabledPoolTemplateBinding{Reference: p.TemplateReference, TemplateDigest: p.TemplateDigest, ReviewDigest: p.PlanDigest}
	stage, e := s.manager.createDisabledPool(ctx, p.RequestID, p.RepositoryID, p.RepositoryFingerprint, p.Spec, p.Reason, binding)
	if e != nil {
		return StagedPool{}, e
	}
	if ctx.Err() != nil || !s.valid() {
		return StagedPool{}, ErrOutcomeUnknown
	}
	return stage, nil
}
