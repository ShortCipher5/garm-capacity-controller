package demandrepair

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CredentialResolver resolves an operator-provided reference, never job/browser data.
// This package does not implement secret storage, token minting or new permissions.
type CredentialResolver func(context.Context, string) (string, error)
type GitHubClient struct {
	base, ref string
	resolve   CredentialResolver
	scope     map[string]Repository
	http      *http.Client
}

var segment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// NewGitHub permits one exact HTTPS API origin/path and an explicit repository
// allowlist. No proxy, redirects, caller-supplied URLs or write methods are used.
func NewGitHub(endpoint, authority, ref string, resolve CredentialResolver, trust *tls.Config, repos []Repository) (*GitHubClient, error) {
	u, e := url.Parse(endpoint)
	if authority == "" || len(authority) > 253 || strings.ToLower(authority) != authority || strings.ContainsAny(authority, "/\\:@?# \r\n\x00") || e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/api/v3") || u.RawPath != "" || ref == "" || len(ref) > 256 || resolve == nil || trust == nil || trust.RootCAs == nil || trust.InsecureSkipVerify || len(repos) == 0 || len(repos) > 100 {
		return nil, ErrObservation
	}
	t := trust.Clone()
	t.RootCAs = trust.RootCAs.Clone()
	if t.ServerName != "" && t.ServerName != u.Hostname() {
		return nil, ErrObservation
	}
	t.ServerName = u.Hostname()
	if t.MinVersion < tls.VersionTLS12 {
		t.MinVersion = tls.VersionTLS12
	}
	if t.MaxVersion != 0 && t.MaxVersion < t.MinVersion {
		return nil, ErrObservation
	}
	c := &GitHubClient{base: u.String(), ref: ref, resolve: resolve, scope: map[string]Repository{}, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, TLSClientConfig: t, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 1 << 20}}}
	seen := map[string]bool{}
	for _, r := range repos {
		identity := strings.ToLower(r.Host + "/" + r.Owner + "/" + r.Name)
		if r.GarmID == "" || r.Host != authority || !segment.MatchString(r.Owner) || !segment.MatchString(r.Name) || seen[identity] {
			return nil, ErrObservation
		}
		if _, ok := c.scope[r.GarmID]; ok {
			return nil, ErrObservation
		}
		seen[identity] = true
		c.scope[r.GarmID] = r
	}
	return c, nil
}
func (c *GitHubClient) Close() {
	if c != nil {
		c.http.CloseIdleConnections()
	}
}
func (c *GitHubClient) get(ctx context.Context, path string, q url.Values, out any) error {
	token, e := c.resolve(ctx, c.ref)
	if e != nil || token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n") {
		return ErrObservation
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if e != nil {
		return ErrObservation
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, e := c.http.Do(req)
	if e != nil {
		return ErrObservation
	}
	defer res.Body.Close()
	// No automatic retry: 403/429, exhausted quota or Retry-After all abort the
	// observation. Scheduling/backoff belongs to a durable outer reconciler.
	if res.StatusCode != 200 || res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0" {
		return ErrObservation
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if e != nil || len(data) > 4<<20 || json.Unmarshal(data, out) != nil || ctx.Err() != nil {
		return ErrObservation
	}
	return nil
}

// Read enumerates recent runs plus every supported active run status regardless
// of creation age, then all-attempt job pages. Search ceilings and non-atomic
// status transitions still prevent an authoritative completeness claim.
func (c *GitHubClient) Read(ctx context.Context, repo Repository, from, to time.Time) ([]Job, error) {
	if c == nil || ctx == nil || ctx.Err() != nil || from.IsZero() || !from.Before(to) || to.After(time.Now()) {
		return nil, ErrObservation
	}
	if mapped, ok := c.scope[repo.GarmID]; !ok || mapped != repo {
		return nil, ErrObservation
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	root := "/repos/" + repo.Owner + "/" + repo.Name
	runs := []int64{}
	seenRuns := map[int64]bool{}
	// Recent history catches terminal transitions; status scans recover active
	// jobs in old runs, including re-runs older than the polling window.
	for _, status := range []string{"", "queued", "in_progress", "requested", "waiting", "pending"} {
		found, e := c.runIDs(ctx, root, repo, status, from, to)
		if e != nil {
			return nil, e
		}
		for _, id := range found {
			if !seenRuns[id] {
				if len(runs) >= 999 {
					return nil, ErrObservation
				}
				seenRuns[id] = true
				runs = append(runs, id)
			}
		}
	}
	result := []Job{}
	seenJobs := map[int64]bool{}
	requests := 0
	for _, run := range runs {
		count := 0
		totalJobs := -1
		for page := 1; page <= 100; page++ {
			requests++
			if requests > 1000 {
				return nil, ErrObservation
			}
			var response struct {
				Total *int `json:"total_count"`
				Jobs  []struct {
					ID     int64    `json:"id"`
					RunID  int64    `json:"run_id"`
					Status string   `json:"status"`
					Labels []string `json:"labels"`
				} `json:"jobs"`
			}
			q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "filter": {"all"}}
			if c.get(ctx, root+"/actions/runs/"+strconv.FormatInt(run, 10)+"/jobs", q, &response) != nil || response.Total == nil || response.Jobs == nil || *response.Total < 0 || *response.Total > 10000 || (totalJobs >= 0 && totalJobs != *response.Total) || len(response.Jobs) > 100 {
				return nil, ErrObservation
			}
			totalJobs = *response.Total
			for _, j := range response.Jobs {
				job := Job{ID: j.ID, Status: j.Status, Labels: j.Labels}
				if j.RunID != run || seenJobs[j.ID] || !validJob(job) || len(result) >= 10000 {
					return nil, ErrObservation
				}
				seenJobs[j.ID] = true
				result = append(result, job)
				count++
			}
			if count > totalJobs {
				return nil, ErrObservation
			}
			if count == totalJobs {
				break
			}
			if len(response.Jobs) != 100 {
				return nil, ErrObservation
			}
		}
		if count != totalJobs {
			return nil, ErrObservation
		}
	}
	return result, nil
}

// ObserveGitHub joins a bounded live-capable read transport to the conservative
// reconciliation adapter. It does not persist checkpoints or establish coverage
// outside the run-created interval. No runtime caller enables it in this PR.
func ObserveGitHub(ctx context.Context, c *GitHubClient, repos []Repository, baseline []Baseline, from, to time.Time) (Result, error) {
	if ctx == nil || c == nil || len(repos) == 0 || len(repos) > 100 {
		return Result{}, ErrObservation
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot := collectedJobs{}
	count := 0
	for _, repo := range repos {
		if _, exists := snapshot[repo.GarmID]; exists {
			return Result{}, ErrObservation
		}
		jobs, e := c.Read(ctx, repo, from, to)
		if e != nil {
			return Result{}, ErrObservation
		}
		count += len(jobs)
		if count > 10000 {
			return Result{}, ErrObservation
		}
		snapshot[repo.GarmID] = jobs
	}
	result, e := Observe(ctx, snapshot, repos, baseline, from, to)
	if e != nil {
		return Result{}, e
	}
	result.StartedAt = started
	return result, nil
}

type collectedJobs map[string][]Job

func (s collectedJobs) ListJobs(_ context.Context, r Repository, _, _ time.Time, cursor string) (Page, error) {
	jobs, ok := s[r.GarmID]
	if !ok {
		return Page{}, ErrObservation
	}
	offset := 0
	if cursor != "" {
		n, e := strconv.Atoi(cursor)
		if e != nil || n < 0 || n > len(jobs) {
			return Page{}, ErrObservation
		}
		offset = n
	}
	end := offset + 100
	if end > len(jobs) {
		end = len(jobs)
	}
	p := Page{Jobs: jobs[offset:end], Terminal: end == len(jobs)}
	if !p.Terminal {
		p.Next = strconv.Itoa(end)
	}
	return p, nil
}

func (c *GitHubClient) runIDs(ctx context.Context, root string, repo Repository, status string, from, to time.Time) ([]int64, error) {
	ids := []int64{}
	seen := map[int64]bool{}
	total := -1
	for page := 1; page <= 10; page++ {
		var response struct {
			Total *int `json:"total_count"`
			Runs  []struct {
				ID         int64 `json:"id"`
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			} `json:"workflow_runs"`
		}
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if status == "" {
			q.Set("created", from.UTC().Format(time.RFC3339)+".."+to.UTC().Format(time.RFC3339))
		} else {
			q.Set("status", status)
		}
		if c.get(ctx, root+"/actions/runs", q, &response) != nil || response.Total == nil || response.Runs == nil || *response.Total < 0 || *response.Total >= 1000 || (total >= 0 && total != *response.Total) || len(response.Runs) > 100 {
			return nil, ErrObservation
		}
		total = *response.Total
		for _, r := range response.Runs {
			if r.ID <= 0 || seen[r.ID] || !strings.EqualFold(r.Repository.FullName, repo.Owner+"/"+repo.Name) {
				return nil, ErrObservation
			}
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
		if len(ids) > total {
			return nil, ErrObservation
		}
		if len(ids) == total {
			break
		}
		if len(response.Runs) != 100 {
			return nil, ErrObservation
		}
	}
	if len(ids) != total {
		return nil, ErrObservation
	}
	return ids, nil
}
