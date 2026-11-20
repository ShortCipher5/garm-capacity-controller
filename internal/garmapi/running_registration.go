package garmapi

import (
	"context"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedsource"
)

const runningRegistrationMaxAge = 30 * time.Second

// ManagedRunningRegistrationScope is the caller's separately approved exact
// enrollment, not derived from the observation it constrains. This package
// checks equality; constructing these fields is not enrollment or approval.
type ManagedRunningRegistrationScope struct {
	SourceIdentity, ControllerID                             string
	RepositoryID, PoolID, ProviderName, OwnershipFingerprint string
}

// ManagedRunningRegistrationReport is a historical sample of what the
// authenticated managed GARM source reported for one exact running row.
// Stock can replace the agent ID. This proves neither current identity,
// remote revocation, JIT origin, GitHub assignment, native incarnation,
// physical execution, nor authority to act. It is not a roster.
// The zero value and JSON round trips carry no report.
type ManagedRunningRegistrationReport struct {
	// Keep sensitive fields behind a closure. Unexported struct fields alone
	// would still appear in fmt (including when nested in other values).
	read func(ManagedRunningRegistrationScope, string, time.Time) (int64, time.Time, bool)
}

type runningRegistrationOwner struct{ closed atomic.Bool }

func (o *runningRegistrationOwner) active() bool { return o != nil && !o.closed.Load() }

// ReportedAgentID returns the historical value only while this local report
// owner is open and the sample is fresh, with the expected enrollment and row
// UUID. The time is local receipt of the instance list, not the registration
// time or heartbeat. No current upstream read or revocation check is implied.
func (r ManagedRunningRegistrationReport) ReportedAgentID(scope ManagedRunningRegistrationScope, rowID string) (int64, time.Time, bool) {
	return r.agentIDAt(scope, rowID, time.Now())
}

func (r ManagedRunningRegistrationReport) agentIDAt(scope ManagedRunningRegistrationScope, rowID string, now time.Time) (int64, time.Time, bool) {
	if r.read == nil {
		return 0, time.Time{}, false
	}
	return r.read(scope, rowID, now)
}

func sealRunningRegistration(owner *runningRegistrationOwner, source *managedsource.Client, enrolled ManagedRunningRegistrationScope, rowID string, agentID int64,
	started, observed, finished time.Time) ManagedRunningRegistrationReport {
	return ManagedRunningRegistrationReport{read: func(expected ManagedRunningRegistrationScope, id string, now time.Time) (int64, time.Time, bool) {
		if !owner.active() || source == nil || enrolled != expected || rowID != id || agentID <= 0 || started.IsZero() ||
			observed.Before(started) || observed.UTC().Before(started.UTC()) ||
			finished.Before(observed) || finished.UTC().Before(observed.UTC()) ||
			now.Before(finished) || now.UTC().Before(finished.UTC()) ||
			!now.Before(started.Add(runningRegistrationMaxAge)) || !now.UTC().Before(started.UTC().Add(runningRegistrationMaxAge)) ||
			source.Identity() != enrolled.SourceIdentity || source.ControllerID() != enrolled.ControllerID || !owner.active() {
			return 0, time.Time{}, false
		}
		return agentID, observed.UTC(), true
	}}
}

// ObserveRunningRegistration is an explicit read-only, single-row query of the
// existing demand capture. It uses exactly the same collection GETs; no extra
// GARM request, credentials, permissions or public observation fields. Ordinary
// token-backed demand never mints a report. The caller must already hold exact
// approved scope and a row UUID; the runner name is not a lookup key.
func (c *DemandClient) ObserveRunningRegistration(ctx context.Context, enrolled ManagedRunningRegistrationScope, rowID string) (ManagedRunningRegistrationReport, error) {
	if c == nil || c.discovery == nil || c.discovery.management == nil || ctx == nil || ctx.Err() != nil ||
		!preparationUUID(rowID) || !preparationUUID(enrolled.PoolID) || !preparationUUID(enrolled.RepositoryID) ||
		!preparationUUID(enrolled.ControllerID) || !runningRegistrationDigest(enrolled.SourceIdentity) ||
		!runningRegistrationDigest(enrolled.OwnershipFingerprint) || !boundedSettingText(enrolled.ProviderName, 256) ||
		strings.TrimSpace(enrolled.ProviderName) != enrolled.ProviderName || !c.repositories[enrolled.RepositoryID] ||
		!c.discovery.management.client.providers[enrolled.ProviderName] {
		return ManagedRunningRegistrationReport{}, ErrManagement
	}
	source, owner := c.discovery.management.managedSource, c.registrationOwner
	if !owner.active() || source == nil || source.ControllerID() != enrolled.ControllerID {
		return ManagedRunningRegistrationReport{}, ErrManagement
	}
	if observed := source.Identity(); observed != "" && observed != enrolled.SourceIdentity {
		return ManagedRunningRegistrationReport{}, ErrManagement
	}
	capture := runningRegistrationCapture{scope: enrolled, source: source, owner: owner, rowID: rowID}
	if _, err := c.observeScoped(ctx, false, &capture); err != nil {
		return ManagedRunningRegistrationReport{}, ErrManagement
	}
	if _, _, ok := capture.report.ReportedAgentID(enrolled, rowID); !ok || ctx.Err() != nil || !owner.active() {
		return ManagedRunningRegistrationReport{}, ErrManagement
	}
	return capture.report, nil
}

func runningRegistrationDigest(v string) bool {
	if len(v) != 64 || v == strings.Repeat("0", 64) {
		return false
	}
	b, err := hex.DecodeString(v)
	return err == nil && hex.EncodeToString(b) == v
}

type runningRegistrationCapture struct {
	scope  ManagedRunningRegistrationScope
	source *managedsource.Client
	owner  *runningRegistrationOwner
	rowID  string
	report ManagedRunningRegistrationReport
}

func (c *runningRegistrationCapture) matches(pools PoolDiscovery) bool {
	if !c.owner.active() {
		return false
	}
	for _, p := range pools.Pools {
		if p.ID == c.scope.PoolID {
			return p.OwnerKind == "repository" && p.OwnerID == c.scope.RepositoryID &&
				p.RepositoryID == c.scope.RepositoryID && p.ProviderName == c.scope.ProviderName &&
				p.OwnershipFingerprint == c.scope.OwnershipFingerprint && c.source.Identity() == c.scope.SourceIdentity
		}
	}
	return false
}

func (c *runningRegistrationCapture) retain(ctx context.Context, first, last PoolDiscovery, runners []gateway.GarmInstance, repos map[string]bool, started, inventoryAt time.Time) bool {
	if !c.matches(last) || len(runners) > 10000 {
		return false
	}
	var selected gateway.GarmInstance
	for _, r := range runners {
		if r.RowID != c.rowID {
			continue
		}
		if selected.RowID != "" || r.PoolID != c.scope.PoolID || r.ProviderName != c.scope.ProviderName ||
			r.State != "running" || !r.IdentityFieldsKnown || r.ScaleSetID != 0 || r.AgentID <= 0 ||
			!boundedSettingText(r.ProviderID, 256) || strings.TrimSpace(r.ProviderID) != r.ProviderID {
			return false
		}
		selected = r
	}
	if selected.RowID == "" {
		return false
	}

	// Only a positive foreign repository in BOTH reads exempts a potentially
	// overlapping agent. Unknown, conflicting or allowed ownership stays
	// ambiguous. Duplicate reported IDs within one provider always veto an
	// exact row report, but that identifier is never returned or used as a key.
	firstByID := registrationPoolsByID(first)
	lastByID := registrationPoolsByID(last)
	for _, r := range runners {
		if r.RowID == selected.RowID {
			continue
		}
		if r.ProviderName == selected.ProviderName && r.ProviderID == selected.ProviderID {
			return false
		}
		if r.AgentID != selected.AgentID {
			continue
		}
		a, b := firstByID[r.PoolID], lastByID[r.PoolID]
		if a.OwnerKind != "repository" || a.OwnerID == "" || repos[a.OwnerID] ||
			a.OwnerKind != b.OwnerKind || a.OwnerID != b.OwnerID ||
			a.ProviderName != r.ProviderName || b.ProviderName != r.ProviderName {
			return false
		}
	}
	out := sealRunningRegistration(c.owner, c.source, c.scope, c.rowID, selected.AgentID, started, inventoryAt, time.Now())
	if _, _, valid := out.ReportedAgentID(c.scope, c.rowID); !valid || ctx.Err() != nil || !c.owner.active() {
		return false
	}
	c.report = out
	return true
}

func registrationPoolsByID(in PoolDiscovery) map[string]DiscoveredPool {
	m := make(map[string]DiscoveredPool, len(in.Pools))
	for _, p := range in.Pools {
		m[p.ID] = p
	}
	return m
}
