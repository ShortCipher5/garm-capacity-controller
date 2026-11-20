package cihealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

func (s cycleSourceInstance) prepareWorkflowRunLink(ctx context.Context, attempt demandrepair.CIAttempt) demandrepair.WorkflowRunLink {
	if s.observer == nil {
		return demandrepair.WorkflowRunLink{}
	}
	link, err := s.observer.PrepareObservedWorkflowRunLink(ctx, attempt)
	if err != nil {
		return demandrepair.WorkflowRunLink{}
	}
	return link
}

// ObservedWorkflowRunLink is an opaque, local, short-lived projection of a
// logical workflow run observed by the exact live reader. It is not a link
// to the exact retry or job. Neither fresh observation nor local reader
// lifetime proves current viewer entitlement or upstream authorization.
type ObservedWorkflowRunLink struct{ resolver *observedWorkflowRunLink }

type observedWorkflowRunLink struct {
	proof WorkflowHistoryProof
	link  demandrepair.WorkflowRunLink
}

func (ObservedWorkflowRunLink) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[observed workflow-run link]"))
}

// URL returns the logical workflow-run destination only while the original
// observation proof remains locally current. Clients must not cache this
// string as a fresh, revocable observation after extracting it.
func (l ObservedWorkflowRunLink) URL() (string, bool) {
	if l.resolver == nil || !l.resolver.proof.current(time.Now()) {
		return "", false
	}
	href := l.resolver.link.URL()
	if href == "" || !l.resolver.proof.current(time.Now()) {
		return "", false
	}
	return href, true
}

// ObservedWorkflowRunLink selects one exact positive attempt from this
// concrete reader's bounded cycle. A partial cycle can establish a link for
// an independently observed attempt, never one it omitted. No fetch or
// browser action is performed.
func (c Cycle) ObservedWorkflowRunLink(source *demandobserver.CIHealthSource, key cihealth.AttemptKey) (ObservedWorkflowRunLink, bool) {
	var none ObservedWorkflowRunLink
	if source == nil || c.source.observer != source || key.Source != source.SourceID() ||
		c.sealedAt == c.sealedAt.Round(0) {
		return none, false
	}
	proof, ok := c.HistoryProof(key)
	if !ok {
		return none, false
	}
	link := c.runLinks[key]
	if !link.Available() || !proof.current(time.Now()) {
		return none, false
	}
	return ObservedWorkflowRunLink{resolver: &observedWorkflowRunLink{proof: proof, link: link}}, true
}
