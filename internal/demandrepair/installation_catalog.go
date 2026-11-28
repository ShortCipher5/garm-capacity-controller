package demandrepair

import (
	"context"
	"crypto/tls"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InstallationCatalog is a separate read-only capability for operator-approved
// candidate repository IDs that need not exist in GARM. It does not return a
// forge registration, a credential reference, or a webhook secret.
type InstallationCatalog struct {
	client *GitHubClient
	scope  map[int64]bool
}
type InstallationRepository struct {
	ID          int64
	Owner, Name string
}

func NewInstallationCatalog(endpoint, authority, ref string, resolve CredentialResolver, trust *tls.Config, ids []int64) (*InstallationCatalog, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, ErrObservation
	}
	scope := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || scope[id] {
			return nil, ErrObservation
		}
		scope[id] = true
	}
	c, e := newGitHubGET(endpoint, authority, ref, resolve, trust)
	if e != nil {
		return nil, e
	}
	return &InstallationCatalog{c, scope}, nil
}
func (c *InstallationCatalog) Close() {
	if c != nil {
		c.client.Close()
	}
}

type installationPage struct {
	Total        *int `json:"total_count"`
	Repositories *[]struct {
		ID    int64 `json:"id"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

// List takes one installation token snapshot per enumeration, so rotating the
// file cannot combine pages from different credentials. A repository that was
// not observed is not proof of deletion; never infer registration from a slug.
func (c *InstallationCatalog) List(ctx context.Context) ([]InstallationRepository, error) {
	if c == nil || c.client == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrObservation
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, e := c.client.resolve(ctx, c.client.ref)
	if e != nil || token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n") {
		return nil, ErrObservation
	}
	client := *c.client
	client.resolve = func(context.Context, string) (string, error) { return token, nil }
	total := -1
	seen := map[int64]bool{}
	names := map[string]bool{}
	out := []InstallationRepository{}
	for page := 1; page <= 10; page++ {
		var result installationPage
		if e := client.get(ctx, "/installation/repositories", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, &result); e != nil || result.Total == nil || result.Repositories == nil {
			return nil, ErrObservation
		}
		if total < 0 {
			total = *result.Total
		}
		if total < 0 || total > 1000 || total != *result.Total {
			return nil, ErrObservation
		}
		rows := *result.Repositories
		if len(rows) != min(100, total-len(seen)) {
			return nil, ErrObservation
		}
		for _, r := range rows {
			full := strings.ToLower(r.Owner.Login + "/" + r.Name)
			if r.ID <= 0 || seen[r.ID] || names[full] || !segment.MatchString(r.Owner.Login) || !segment.MatchString(r.Name) || !strings.EqualFold(r.FullName, r.Owner.Login+"/"+r.Name) {
				return nil, ErrObservation
			}
			seen[r.ID] = true
			names[full] = true
			if c.scope[r.ID] {
				out = append(out, InstallationRepository{r.ID, r.Owner.Login, r.Name})
			}
		}
		if len(seen) == total {
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			return out, nil
		}
	}
	return nil, ErrObservation
}
