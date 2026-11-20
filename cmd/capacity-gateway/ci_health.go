package main

import (
	"context"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
)

var errCIHealthConfiguration = errors.New("CI health timing configuration unavailable")

type ciHealthConfiguration struct {
	Repositories []demandobserver.CIHealthRepositoryPin `json:"repositories"`
}

func validateCIHealthConfiguration(c configuration) error {
	if c.CIHealth == nil && !c.EnableCIHealth {
		return nil
	}
	if c.CIHealth == nil || !c.EnableCIHealth || c.Admin == nil || c.DemandObserverConfigFile == "" ||
		len(c.CIHealth.Repositories) == 0 || len(c.CIHealth.Repositories) > 4 {
		return errCIHealthConfiguration
	}
	ids := make(map[int64]bool, len(c.CIHealth.Repositories))
	locators := make(map[string]bool, len(c.CIHealth.Repositories))
	for _, p := range c.CIHealth.Repositories {
		if p.GitHubID <= 0 || p.GarmID == "" || len(p.GarmID) > 256 || ids[p.GitHubID] || locators[p.GarmID] {
			return errCIHealthConfiguration
		}
		ids[p.GitHubID], locators[p.GarmID] = true, true
	}
	return nil
}

// ciHealthRuntime only polls the already-approved GitHub read source. It
// does not inspect GARM, schedule jobs, or use the admission database.
type ciHealthRuntime struct {
	source   *demandobserver.CIHealthSource
	snapshot *adminview.CIHealthSnapshot
	repos    []string
}

func buildCIHealth(c configuration, dc *demandobserver.Config) (*ciHealthRuntime, error) {
	if err := validateCIHealthConfiguration(c); err != nil {
		return nil, err
	}
	if !c.EnableCIHealth {
		return nil, nil
	}
	if dc == nil {
		return nil, errCIHealthConfiguration
	}
	source, err := demandobserver.NewCIHealthSource(*dc, demandobserver.CIHealthInput{Enabled: true, Repositories: c.CIHealth.Repositories})
	if err != nil || source == nil {
		return nil, errCIHealthConfiguration
	}
	approved := make([]adminview.CIHealthRepository, 0, len(c.CIHealth.Repositories))
	repos := make([]string, 0, len(c.CIHealth.Repositories))
	for _, repo := range source.Repositories() {
		approved = append(approved, adminview.CIHealthRepository{Source: source.SourceID(), RepositoryID: repo.GitHubID})
		repos = append(repos, repo.GarmID)
	}
	snapshot, err := adminview.NewCIHealthSnapshot(approved)
	if err != nil || len(repos) != len(c.CIHealth.Repositories) {
		source.Close()
		return nil, errCIHealthConfiguration
	}
	return &ciHealthRuntime{source: source, snapshot: snapshot, repos: repos}, nil
}

func (c *ciHealthRuntime) close() {
	c.snapshot.Withdraw()
	c.source.Close()
}

const (
	ciHealthCycleDelay   = 4 * time.Minute
	ciHealthCycleBackoff = 5 * time.Minute
	ciHealthRecentWindow = 24 * time.Hour
)

func (c *ciHealthRuntime) run(ctx context.Context) {
	defer c.snapshot.Withdraw()
	delay := ciHealthCycleDelay
	for ctx.Err() == nil {
		to := time.Now().UTC().Truncate(time.Second)
		cycle, err := cihealthsource.ReadCycle(ctx, c.source, c.repos, to.Add(-ciHealthRecentWindow), to)
		if err != nil || ctx.Err() != nil || cycle.State() == cihealthsource.CycleUnavailable ||
			c.snapshot.PublishTiming(cycle.ObservedAt(), cycle.Facts(), cycle.State() == cihealthsource.CyclePartial) != nil {
			c.snapshot.Withdraw()
			delay = ciHealthCycleBackoff
		} else {
			delay = ciHealthCycleDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
