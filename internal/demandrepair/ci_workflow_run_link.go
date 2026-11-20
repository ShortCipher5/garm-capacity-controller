package demandrepair

import (
	"context"
	"fmt"
	"sort"
)

// WorkflowRunLink is a browser destination for a logical GitHub workflow run.
// The same run ID is shared by every retry. This cannot identify an exact
// attempt or job and does not carry credentials or authorize access.
type WorkflowRunLink struct{ resolver *workflowRunResolver }

// Keep only private resolver functions here, not a ready URL or named
// coordinates a recursive formatter could dump from an unexported field.
type workflowRunResolver struct {
	resolve func() string
	allowed func() bool
}

func (WorkflowRunLink) Format(s fmt.State, _ rune) { _, _ = s.Write([]byte("[workflow-run link]")) }

// Available checks the optional local lease without constructing the URL.
func (l WorkflowRunLink) Available() bool {
	return l.resolver != nil && l.resolver.resolve != nil && (l.resolver.allowed == nil || l.resolver.allowed())
}

// URL constructs the logical run destination only when still available.
func (l WorkflowRunLink) URL() string {
	if !l.Available() {
		return ""
	}
	href := l.resolver.resolve()
	if !l.Available() {
		return ""
	}
	return href
}

// WithLocalLifetime additionally restricts the link to a reader-owned
// lifetime. It never extends any existing lease or supplies authorization.
func (l WorkflowRunLink) WithLocalLifetime(life context.Context) WorkflowRunLink {
	if life == nil || life.Err() != nil || !l.Available() {
		return WorkflowRunLink{}
	}
	done := life.Done()
	if done == nil {
		return WorkflowRunLink{}
	}
	return WorkflowRunLink{resolver: &workflowRunResolver{
		resolve: l.resolver.resolve,
		allowed: func() bool {
			select {
			case <-done:
				return false
			default:
				return l.Available()
			}
		},
	}}
}

// WorkflowRunLink derives GitHub's documented logical run URL from this
// source's configured GitHub.com repository enrollment only. The caller must
// separately establish that it may show the run observation. No HTTP request,
// API/response URL, browser action or viewer permission is involved.
//
// https://docs.github.com/en/actions/reference/workflows-and-actions/variables
func (s *CIHealthSource) WorkflowRunLink(sourceID string, repositoryID, runID int64) (WorkflowRunLink, error) {
	var none WorkflowRunLink
	if s == nil || s.client == nil || s.client.base != "https://api.github.com" ||
		sourceID == "" || sourceID != s.id || repositoryID <= 0 || runID <= 0 {
		return none, ErrObservation
	}
	i := sort.Search(len(s.scope), func(i int) bool { return s.scope[i].GitHubID >= repositoryID })
	if i == len(s.scope) {
		return none, ErrObservation
	}
	r := s.scope[i]
	if r.GitHubID != repositoryID || r.Host != "github.com" || !segment.MatchString(r.Owner) ||
		!segment.MatchString(r.Name) || s.byGarm[r.GarmID] != r || s.client.scope[r.GarmID] != r.Repository {
		return none, ErrObservation
	}
	owner, name := r.Owner, r.Name
	return WorkflowRunLink{resolver: &workflowRunResolver{resolve: func() string {
		return "https://github.com/" + owner + "/" + name + ciRunPath(runID)
	}}}, nil
}

// PrepareObservedWorkflowRunLink derives the same logical destination only
// from an exact, still-current attempt minted by this reader. The caller owns
// any shorter enclosing lifecycle. No additional remote read is performed.
func (s *CIHealthSource) PrepareObservedWorkflowRunLink(ctx context.Context, a CIAttempt) (WorkflowRunLink, error) {
	var none WorkflowRunLink
	if s == nil {
		return none, ErrObservation
	}
	r, ok := s.enrolled(ctx, a.repository.GarmID)
	if !ok || a.source != s || a.repository != r || a.run.RunID <= 0 || a.run.Attempt <= 0 ||
		a.run.WorkflowID <= 0 || !ciArtifactLease(a, ciArtifactNow()) {
		return none, ErrObservation
	}
	link, err := s.WorkflowRunLink(s.SourceID(), r.GitHubID, a.run.RunID)
	if err != nil || ctx.Err() != nil || !ciArtifactLease(a, ciArtifactNow()) {
		return none, ErrObservation
	}
	// Retain only the lease components, never the original attempt payload.
	lease := CIAttempt{at: a.at, minted: a.minted}
	return WorkflowRunLink{resolver: &workflowRunResolver{
		resolve: link.resolver.resolve,
		allowed: func() bool { return ciArtifactLease(lease, ciArtifactNow()) },
	}}, nil
}
