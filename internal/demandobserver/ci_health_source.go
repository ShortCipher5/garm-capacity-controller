package demandobserver

import (
	"context"
	"strings"
	"sync"

	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
)

// CIHealthRepositoryPin joins a configured repository locator to its separately
// reviewed numeric GitHub ID. It cannot provide a different owner or endpoint.
type CIHealthRepositoryPin struct {
	GarmID   string `json:"garm_id"`
	GitHubID int64  `json:"github_id"`
}

// CIHealthInput is opt-in even when the demand observer itself is enabled.
type CIHealthInput struct {
	Enabled      bool                    `json:"enabled"`
	Repositories []CIHealthRepositoryPin `json:"repositories"`
}

// CIHealthSource owns a dedicated read-only transport with the existing demand
// observer's approved GitHub trust and credential. It never opens a GARM client.
type CIHealthSource struct {
	life   context.Context
	cancel context.CancelFunc
	client *demandrepair.GitHubClient
	source *demandrepair.CIHealthSource
	close  sync.Once
}

func NewCIHealthSource(c Config, in CIHealthInput) (*CIHealthSource, error) {
	if !in.Enabled {
		return nil, nil
	}
	if c.validate() != nil || len(in.Repositories) == 0 || len(in.Repositories) > len(c.Repositories) || c.GitHubTokenFile == "" {
		return nil, ErrObserver
	}
	configured := make(map[string]demandrepair.Repository, len(c.Repositories))
	names := make(map[string]bool, len(c.Repositories))
	for _, r := range c.Repositories {
		name := strings.ToLower(r.Host + "/" + r.Owner + "/" + r.Name)
		if _, ok := configured[r.GarmID]; ok || r.GarmID == "" || names[name] || r.Host != c.GitHubAuthority {
			return nil, ErrObserver
		}
		configured[r.GarmID] = r
		names[name] = true
	}
	allowed := make([]demandrepair.Repository, 0, len(in.Repositories))
	enrolled := make([]demandrepair.CIRepository, 0, len(in.Repositories))
	seenIDs, seenRepos := make(map[int64]bool, len(in.Repositories)), make(map[string]bool, len(in.Repositories))
	for _, pin := range in.Repositories {
		r, ok := configured[pin.GarmID]
		if !ok || pin.GitHubID <= 0 || seenIDs[pin.GitHubID] || seenRepos[pin.GarmID] {
			return nil, ErrObserver
		}
		allowed = append(allowed, r)
		enrolled = append(enrolled, demandrepair.CIRepository{Repository: r, GitHubID: pin.GitHubID})
		seenIDs[pin.GitHubID], seenRepos[pin.GarmID] = true, true
	}

	roots, err := trust(c.GitHubCAFile)
	if err != nil {
		return nil, ErrObserver
	}
	life, cancel := context.WithCancel(context.Background())
	failed := true
	defer func() {
		if failed {
			cancel()
		}
	}()
	const ref = "ci-health-existing-demand-observer"
	path := c.GitHubTokenFile // Frozen, not a caller-supplied per-query operand.
	client, err := demandrepair.NewGitHub(c.GitHubEndpoint, c.GitHubAuthority, ref, func(ctx context.Context, received string) (string, error) {
		if received != ref || ctx == nil || ctx.Err() != nil || life.Err() != nil {
			return "", ErrObserver
		}
		secret, err := token(path)
		if err != nil || ctx.Err() != nil || life.Err() != nil {
			return "", ErrObserver
		}
		return secret, nil
	}, roots, allowed)
	if err != nil {
		return nil, ErrObserver
	}
	source, err := demandrepair.NewCIHealthSource(client, enrolled)
	if err != nil {
		client.Close()
		return nil, ErrObserver
	}
	failed = false
	return &CIHealthSource{life: life, cancel: cancel, client: client, source: source}, nil
}

func (s *CIHealthSource) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.close.Do(func() {
		s.cancel()
		s.client.Close()
	})
}

func (s *CIHealthSource) SourceID() string {
	if s == nil || s.source == nil {
		return ""
	}
	return s.source.SourceID()
}

// LifetimeActive reports only whether this exact opted-in reader has been
// stopped locally. An exact fresh observation is still required for any view;
// this method does not check or imply current upstream access by itself.
func (s *CIHealthSource) LifetimeActive() bool {
	return s != nil && s.life != nil && s.life.Err() == nil
}

func (s *CIHealthSource) Repositories() []demandrepair.CIRepository {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Repositories()
}

func ciHealthRead[T any](s *CIHealthSource, caller context.Context, read func(context.Context, *demandrepair.CIHealthSource) (T, error)) (T, error) {
	var empty T
	if s == nil || s.source == nil || s.life.Err() != nil || caller == nil || caller.Err() != nil {
		return empty, ErrObserver
	}
	// Parent these requests to the transport lifetime so Close synchronously
	// cancels outstanding request contexts before releasing idle connections.
	var ctx context.Context
	var cancel context.CancelFunc
	if deadline, ok := caller.Deadline(); ok {
		ctx, cancel = context.WithDeadline(s.life, deadline)
	} else {
		ctx, cancel = context.WithCancel(s.life)
	}
	defer cancel()
	detach := context.AfterFunc(caller, cancel)
	defer detach()
	if caller.Err() != nil || ctx.Err() != nil {
		return empty, ErrObserver
	}
	out, err := read(ctx, s.source)
	if err != nil || ctx.Err() != nil || caller.Err() != nil {
		return empty, ErrObserver
	}
	return out, nil
}

func (s *CIHealthSource) ListCIRuns(ctx context.Context, garmID string, q demandrepair.CIRunQuery) (demandrepair.CIRunPage, error) {
	return ciHealthRead(s, ctx, func(ctx context.Context, source *demandrepair.CIHealthSource) (demandrepair.CIRunPage, error) {
		return source.ListCIRuns(ctx, garmID, q)
	})
}

func (s *CIHealthSource) ReadCIAttempt(ctx context.Context, garmID string, runID, attempt int64) (demandrepair.CIAttempt, error) {
	return ciHealthRead(s, ctx, func(ctx context.Context, source *demandrepair.CIHealthSource) (demandrepair.CIAttempt, error) {
		return source.ReadCIAttempt(ctx, garmID, runID, attempt)
	})
}

func (s *CIHealthSource) ListCIAttemptJobs(ctx context.Context, attempt demandrepair.CIAttempt, page int) (demandrepair.CIJobPage, error) {
	return ciHealthRead(s, ctx, func(ctx context.Context, source *demandrepair.CIHealthSource) (demandrepair.CIJobPage, error) {
		return source.ListCIAttemptJobs(ctx, attempt, page)
	})
}
