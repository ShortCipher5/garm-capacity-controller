package adminview

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"testing"
	"time"
)

func TestBrokerStatusPreservesChargesAndMissingConfiguration(t *testing.T) {
	charge := admission.Resources{CPU: 4000, RAM: 8192, Disk: 40000, Runners: 4}
	state := admission.BrokerGrantState{Sequence: 9, Ready: false, Config: admission.Config{Pools: map[string]admission.Pool{"legacy": {Budget: admission.Resources{Runners: 2}}, "mapped": {Budget: admission.Resources{Runners: 2}}}}, Allocated: admission.Allocations{Pools: map[string]admission.Resources{"legacy": charge, "mapped": charge}}, Policy: admission.BrokerPolicy{Revision: 2, Repositories: []admission.BrokerRepositoryPolicy{{ID: "repo", Pools: []admission.BrokerPoolPolicy{{ID: "missing", MaxRunners: 3, WarmIdle: 1}, {ID: "mapped", MaxRunners: 1, WarmIdle: 0}}}}}, PoolProfiles: map[string]admission.BrokerPoolProfile{"mapped": {Repository: "different", SourceIdentity: "private-not-rendered"}}}
	view := projectBrokerStatus(state, time.Now())
	if view.Sequence != 9 || view.PolicyRevision != 2 || view.LedgerReady || len(view.Pools) != 3 {
		t.Fatal(view)
	}
	for _, row := range view.Pools {
		switch row.Pool {
		case "legacy":
			if row.Desired != nil || row.Repository != "" || *row.Charged != charge {
				t.Fatal(row)
			}
		case "missing":
			if row.Budget != nil || row.Charged != nil || row.Desired.WarmIdle != 1 || row.Mapping != "not-recorded" {
				t.Fatal(row)
			}
		case "mapped":
			if row.Mapping != "repository-mismatch" || row.Charged.Runners != 4 || row.Desired.MaxRunners != 1 {
				t.Fatal(row)
			}
		}
	}
}
func TestBrokerPolicyReadUsesCoherentStatusAndNoMutation(t *testing.T) {
	h, read, _ := managementFixture(t)
	before, err := h.store.ReadBrokerGrantState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, "GET", "/admin/api/v1/broker-policy", read)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Policy admission.BrokerPolicy `json:"policy"`
		Status brokerPolicyStatus     `json:"status"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	after, err := h.store.ReadBrokerGrantState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.Sequence != before.Sequence || result.Status.Sequence != after.Sequence || result.Policy.Revision != result.Status.PolicyRevision || result.Status.Source != "saved-policy-and-charged-ledger" {
		t.Fatal(result)
	}
}
