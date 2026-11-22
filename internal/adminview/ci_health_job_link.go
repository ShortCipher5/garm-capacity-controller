package adminview

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const ciJobHandleHeader = "X-GARM-CI-Job-Handle"

// The selector is private; production callers use only the concrete observed
// source projection. Handles are not authorization or persistent identifiers.
type ciJobLinkSelector func(cihealth.JobKey) (func() (string, bool), bool)

type ciJobLinkTarget struct {
	key cihealth.JobKey
	url func() (string, bool)
}

type ciJobLinkResponse struct {
	URL          string `json:"url"`
	Source       string `json:"source"`
	RepositoryID string `json:"repository_id"`
	WorkflowID   string `json:"workflow_id"`
	RunID        string `json:"run_id"`
	Attempt      string `json:"attempt"`
	JobID        string `json:"job_id"`
}

func attachCIJobHandles(view *ciTimingView, facts []cihealth.AttemptFacts, selectLink ciJobLinkSelector) (map[string]ciJobLinkTarget, error) {
	if selectLink == nil {
		return nil, nil
	}
	out := make(map[string]ciJobLinkTarget)
	for i, attempt := range facts {
		for j, job := range attempt.Jobs {
			resolver, ok := selectLink(job.Key)
			if !ok {
				continue
			}
			// Measure preserves order, but never attach a handle to an
			// incompatible shape if that projection changes later.
			if resolver == nil || i >= len(view.Attempts) || j >= len(view.Attempts[i].Jobs) ||
				view.Attempts[i].Jobs[j].ID != ciInt(job.Key.JobID) || len(out) >= maxCIJobs {
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
			out[handle] = ciJobLinkTarget{job.Key, resolver}
			view.Attempts[i].Jobs[j].LinkHandle = handle
		}
	}
	return out, nil
}

func validCIJobHandle(handle string) bool {
	if len(handle) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(handle)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == handle
}

func serveCIJobLink(w http.ResponseWriter, r *http.Request, s *CIHealthSnapshot, handles []string) {
	code := http.StatusBadRequest
	if len(handles) == 1 && validCIJobHandle(handles[0]) && r.ContentLength == 0 &&
		len(r.TransferEncoding) == 0 && !r.URL.ForceQuery && sameOrigin(r) {
		if body := s.resolveCIJobLink(r.Context(), handles[0]); body != nil {
			_, _ = w.Write(body) // authorization is already linearized; no lock over the socket
			return
		}
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte("{\"error\":\"CI job destination unavailable\"}\n"))
}

// Authorization is serialized with publication, Withdraw and terminal Stop.
// A result already authorized before revocation cannot be recalled.
func (s *CIHealthSnapshot) resolveCIJobLink(ctx context.Context, handle string) []byte {
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
	target, present := s.jobLinks[handle]
	if !present {
		return nil
	}
	href, ok := target.url()
	if !ok || href == "" {
		return nil
	}
	k := target.key
	body, err := json.Marshal(ciJobLinkResponse{href, k.Attempt.Source, ciInt(k.Attempt.RepositoryID),
		ciInt(k.Attempt.WorkflowID), ciInt(k.Attempt.RunID), ciInt(k.Attempt.Attempt), ciInt(k.JobID)})
	if err != nil || len(body) > 1024 || !current() {
		return nil
	}
	if latest, valid := target.url(); !valid || latest != href || !current() {
		return nil
	}
	return body
}
