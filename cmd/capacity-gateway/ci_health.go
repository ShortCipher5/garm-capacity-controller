package main

import (
	"context"
	"errors"
	"sync"
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
	// Optional owner-supplied history; buildCIHealth never opens or attaches it.
	historyMu       sync.Mutex
	history         *cihealthsource.BackgroundCycleHistory
	historyDisabled bool
	historyTrends   bool
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
	c.disableHistory()
}

// attachOwnedHistory is a construction-time injection, never a settings or
// startup action. Storage custody and its exact workflow/revision review are
// independently provided by the caller, not derived from the GARM scope.
func (c *ciHealthRuntime) attachOwnedHistory(owned *cihealthsource.OwnedCycleHistory, reviewed []cihealthsource.HistoryExpectation) error {
	return c.attachHistory(owned, reviewed, false)
}

// attachOwnedHistoryWithTrends is explicit consent to publish scoped
// aggregates through the same loopback owner endpoint. Startup never calls
// it from the ordinary timing configuration or opens history by default.
func (c *ciHealthRuntime) attachOwnedHistoryWithTrends(owned *cihealthsource.OwnedCycleHistory, reviewed []cihealthsource.HistoryExpectation) error {
	return c.attachHistory(owned, reviewed, true)
}

func (c *ciHealthRuntime) attachHistory(owned *cihealthsource.OwnedCycleHistory, reviewed []cihealthsource.HistoryExpectation, trends bool) error {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if c.historyDisabled || c.history != nil {
		return cihealthsource.ErrCycleHistory
	}
	bind := owned.BindBackground
	if trends {
		bind = owned.BindBackgroundWithTrends
	}
	history, err := bind(c.source, reviewed)
	if err != nil {
		return err
	}
	c.history, c.historyTrends = history, trends
	return nil
}

func (c *ciHealthRuntime) hasHistoryTrends() bool {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	return !c.historyDisabled && c.historyTrends
}

func (c *ciHealthRuntime) activeHistory() *cihealthsource.BackgroundCycleHistory {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	if c.historyDisabled {
		return nil
	}
	return c.history
}

func (c *ciHealthRuntime) disableHistory() {
	c.historyMu.Lock()
	c.historyDisabled = true
	history := c.history
	c.historyMu.Unlock()
	if history != nil {
		_ = history.Close()
	}
}

const (
	ciHealthCycleDelay   = 4 * time.Minute
	ciHealthCycleBackoff = 5 * time.Minute
	ciHealthRecentWindow = 24 * time.Hour
)

func (c *ciHealthRuntime) run(ctx context.Context) {
	defer c.disableHistory()
	defer c.snapshot.Withdraw()
	delay := ciHealthCycleDelay
	for ctx.Err() == nil {
		if c.runCycle(ctx) {
			delay = ciHealthCycleDelay
		} else {
			delay = ciHealthCycleBackoff
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

func (c *ciHealthRuntime) runCycle(ctx context.Context) bool {
	to := time.Now().UTC().Truncate(time.Second)
	cycle, err := cihealthsource.ReadCycle(ctx, c.source, c.repos, to.Add(-ciHealthRecentWindow), to)
	if err != nil || ctx.Err() != nil || cycle.State() == cihealthsource.CycleUnavailable ||
		c.snapshot.PublishTiming(cycle.ObservedAt(), cycle.Facts(), cycle.State() == cihealthsource.CyclePartial) != nil {
		c.snapshot.Withdraw()
		c.disableHistory()
		return false
	}
	if history := c.activeHistory(); history != nil && cycle.State() == cihealthsource.CycleObserved {
		if _, err := history.PersistCycle(ctx, cycle); err != nil {
			c.disableHistory()
		} else if c.hasHistoryTrends() {
			result, err := history.SummarizeObservedTrends(ctx, cycle)
			if err != nil || (result.Current() && c.snapshot.PublishRetainedTrends(result) != nil) {
				c.disableHistory()
			}
		}
	}
	// Optional storage is not the now-view source. Its failure does not turn
	// freshly read timing into old disk data or change the read cadence.
	return true
}
