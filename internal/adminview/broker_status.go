package adminview

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"sort"
	"time"
)

type brokerPoolStatus struct {
	Pool       string                      `json:"pool"`
	Repository string                      `json:"repository,omitempty"`
	Desired    *admission.BrokerPoolPolicy `json:"desired,omitempty"`
	Budget     *admission.Resources        `json:"budget,omitempty"`
	Charged    *admission.Resources        `json:"charged,omitempty"`
	Mapping    string                      `json:"mapping"`
}
type brokerPolicyStatus struct {
	Source         string             `json:"source"`
	Sequence       int64              `json:"sequence"`
	PolicyRevision int64              `json:"policy_revision"`
	ObservedAt     time.Time          `json:"observed_at"`
	LedgerReady    bool               `json:"ledger_ready"`
	Pools          []brokerPoolStatus `json:"pools"`
}

// This is one coherent ledger read, NOT a demand observation or live plan.
// Charged includes unused grants/unknown resources; never label it running.
func projectBrokerStatus(s admission.BrokerGrantState, now time.Time) brokerPolicyStatus {
	rows := map[string]brokerPoolStatus{}
	for id, pool := range s.Config.Pools {
		budget, charged := pool.Budget, s.Allocated.Pools[id]
		rows[id] = brokerPoolStatus{Pool: id, Budget: &budget, Charged: &charged, Mapping: "not-recorded"}
	}
	for _, repo := range s.Policy.Repositories {
		for _, pool := range repo.Pools {
			row := rows[pool.ID]
			row.Pool = pool.ID
			row.Repository = repo.ID
			desired := pool
			row.Desired = &desired
			row.Mapping = "not-recorded"
			rows[pool.ID] = row
		}
	}
	out := brokerPolicyStatus{Source: "saved-policy-and-charged-ledger", Sequence: s.Sequence, PolicyRevision: s.Policy.Revision, ObservedAt: now.UTC(), LedgerReady: s.Ready, Pools: []brokerPoolStatus{}}
	for id, row := range rows {
		if profile, ok := s.PoolProfiles[id]; ok {
			row.Mapping = "recorded"
			if row.Repository != "" && profile.Repository != row.Repository {
				row.Mapping = "repository-mismatch"
			}
		}
		out.Pools = append(out.Pools, row)
	}
	sort.Slice(out.Pools, func(i, j int) bool { return out.Pools[i].Pool < out.Pools[j].Pool })
	return out
}
