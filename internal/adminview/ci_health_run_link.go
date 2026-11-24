package adminview

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const ciRunHandleHeader = "X-GARM-CI-Run-Handle"

// The selector is private; production callers use only the concrete observed
// source projection. Handles are not authorization or persistent identifiers.
type ciRunLinkSelector func(cihealth.AttemptKey) (func() (string, bool), bool)

type ciRunLinkTarget struct {
	key cihealth.AttemptKey
	url func() (string, bool)
}

type ciRunLinkResponse struct {
	URL          string `json:"url"`
	Source       string `json:"source"`
	RepositoryID string `json:"repository_id"`
	WorkflowID   string `json:"workflow_id"`
	RunID        string `json:"run_id"`
	Attempt      string `json:"attempt"`
}

// attachCIRunHandles mints one opaque handle per exact attempt whose observed
// reader supplied a locator. Attempts without a current, supported locator
// get no handle: exact run navigation is then unavailable, never inferred.
func attachCIRunHandles(view *ciTimingView, facts []cihealth.AttemptFacts, selectLink ciRunLinkSelector) (map[string]ciRunLinkTarget, error) {
	if selectLink == nil {
		return nil, nil
	}
	out := make(map[string]ciRunLinkTarget)
	for i, attempt := range facts {
		k := attempt.Key
		resolver, ok := selectLink(k)
		if !ok {
			continue
		}
		// Measure preserves order, but never attach a handle to an
		// incompatible shape if that projection changes later.
		if resolver == nil || i >= len(view.Attempts) ||
			view.Attempts[i].Source != k.Source || view.Attempts[i].RepoID != ciInt(k.RepositoryID) ||
			view.Attempts[i].Workflow != ciInt(k.WorkflowID) || view.Attempts[i].Run != ciInt(k.RunID) ||
			view.Attempts[i].Attempt != ciInt(k.Attempt) || len(out) >= maxCIAttempts {
			return nil, errCIProjection
		}
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, errCIProjection
		}
		handle := base64.RawURLEncoding.EncodeToString(random[:])
		if _, duplicate := out[handle]; duplicate {
			return nil, errCIProjection
		}
		out[handle] = ciRunLinkTarget{k, resolver}
		view.Attempts[i].RunLinkHandle = handle
	}
	return out, nil
}

func serveCIRunLink(w http.ResponseWriter, r *http.Request, s *CIHealthSnapshot, handles []string) {
	code := http.StatusBadRequest
	if len(handles) == 1 && validCIJobHandle(handles[0]) && r.ContentLength == 0 &&
		len(r.TransferEncoding) == 0 && !r.URL.ForceQuery && sameOrigin(r) {
		if body := s.resolveCIRunLink(r.Context(), handles[0]); body != nil {
			_, _ = w.Write(body) // authorization is already linearized; no lock over the socket
			return
		}
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte("{\"error\":\"CI run destination unavailable\"}\n"))
}

// Authorization is serialized with publication, Withdraw and terminal Stop.
// A result already authorized before revocation cannot be recalled.
func (s *CIHealthSnapshot) resolveCIRunLink(ctx context.Context, handle string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := func() bool {
		now, stable := s.wallLocked()
		return stable && !s.stopped && ctx.Err() == nil && !now.Before(s.latest) &&
			now.Before(s.latest.Add(ciDisplayLifetime))
	}
	if !current() || len(s.body) == 0 {
		return nil
	}
	target, present := s.runLinks[handle]
	if !present {
		return nil
	}
	href, ok := target.url()
	if !ok || href == "" {
		return nil
	}
	k := target.key
	body, err := json.Marshal(ciRunLinkResponse{href, k.Source, ciInt(k.RepositoryID),
		ciInt(k.WorkflowID), ciInt(k.RunID), ciInt(k.Attempt)})
	if err != nil || len(body) > 1024 || !current() {
		return nil
	}
	if latest, valid := target.url(); !valid || latest != href || !current() {
		return nil
	}
	return body
}
