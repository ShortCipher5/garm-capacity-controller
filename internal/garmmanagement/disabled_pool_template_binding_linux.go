package garmmanagement

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

// Private service-derived binding. Old public CreateDisabledPool uses nil and
// keeps its exact original canonical bytes. No schema/version/phase is added.
type disabledPoolTemplateBinding struct {
	Reference      string `json:"template_ref"`
	TemplateDigest string `json:"template_digest"`
	ReviewDigest   string `json:"review_digest"`
}

func validDisabledPoolTemplateBinding(b *disabledPoolTemplateBinding, request, repo, expected, reason string, spec garmapi.DisabledPoolSpec, management, lineage string) bool {
	if b == nil || !stagingText(b.Reference, 128) || !stagingID(request) || !stagingID(repo) || !stagingText(reason, 256) || !validJournalLineage(expected) || !validJournalLineage(management) || !validJournalLineage(lineage) {
		return false
	}
	t := DisabledPoolTemplate{Reference: b.Reference, RepositoryID: repo, Spec: spec}
	if b.TemplateDigest != disabledPoolTemplateDigest(t) {
		return false
	}
	p := DisabledPoolPlan{Protocol: DisabledPoolPlanProtocol, RequestID: request, TemplateReference: b.Reference, Reason: reason, RepositoryID: repo, TemplateDigest: b.TemplateDigest, RepositoryFingerprint: expected, Spec: spec}
	return b.ReviewDigest == disabledPoolPlanDigest(p, management, lineage)
}

// RecordedDisabledPoolPlan reads ONLY a canonical confirmed original template
// binding in the actual live journal. It performs no network request or native
// command. Legacy nil bindings cannot be relabelled as template-bound history.
// The fixed registrar must independently match its current protected template,
// re-read fresh disabled source facts and apply its own explicit inactive CAS.
func (m *Manager) RecordedDisabledPoolPlan(ctx context.Context, requestID string) (DisabledPoolPlan, error) {
	if m == nil || m.remote == nil || m.journal == nil || ctx == nil || ctx.Err() != nil || !stagingID(requestID) {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	if e := m.enter(ctx); e != nil {
		return DisabledPoolPlan{}, e
	}
	defer m.leave()
	record, e := m.confirmedStage(ctx, requestID)
	if e != nil {
		return DisabledPoolPlan{}, e
	}
	b := record.binding
	if b == nil {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	p := DisabledPoolPlan{Protocol: DisabledPoolPlanProtocol, RequestID: requestID, TemplateReference: b.Reference, Reason: record.reason, RepositoryID: record.stage.RepositoryID, TemplateDigest: b.TemplateDigest, RepositoryFingerprint: record.expected, Spec: record.spec, PlanDigest: b.ReviewDigest}
	if p.Validate() != nil || ctx.Err() != nil {
		return DisabledPoolPlan{}, ErrDisabledPoolSelection
	}
	return p.Clone(), nil
}
