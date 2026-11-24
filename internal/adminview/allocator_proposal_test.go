package adminview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
)

type allocatorProposalFixture struct {
	proposal *brokerinput.AllocatorProposal
}

func (p *allocatorProposalFixture) Snapshot() brokerinput.LoopStatus { return brokerinput.LoopStatus{} }
func (p *allocatorProposalFixture) ReadPlan(ctx context.Context) (brokerinput.ReadPlan, error) {
	return brokerinput.ReadPlan{Source: "scoped-stock-and-coherent-ledger", Allocator: p.proposal}, nil
}

func TestAllocatorProposalSectionServedOnExistingAuthenticatedRoute(t *testing.T) {
	h, store, token := fixture(t)
	before, _ := store.ReadSnapshot(context.Background())
	proposal := &brokerinput.AllocatorProposal{
		SourceIdentity: "source",
		InputRevision:  "revision",
		Held: brokerinput.AllocatorHeldCapacity{
			Global:       admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1},
			Repositories: map[string]admission.Resources{},
			Pools:        map[string]admission.Resources{},
		},
		Awards: []brokerinput.AllocatorAward{{
			Pool: "pool", Repository: "repository", Host: "host", Resources: admission.Resources{CPU: 1000, RAM: 1000, Disk: 1000, Runners: 1},
			Reason: "queued_demand",
		}},
		Decisions: []brokerinput.AllocatorPoolDecision{{
			Pool: "pool", Repository: "repository", Reason: "proposed", RepositoryWeight: 2,
		}},
		Risks: []string{"destination_unestablished"},
	}
	source := &allocatorProposalFixture{proposal: proposal}
	h.SetAllocatorPlanSource(source)
	path := "/admin/api/v1/allocator-plan"
	if unauth := request(t, h, "GET", path, ""); unauth.Code != 401 {
		t.Fatal("unauthorized allocator proposal read", unauth.Code)
	}
	response := request(t, h, "GET", path, token)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "allocator") {
		t.Fatal(response.Code, response.Body.String())
	}
	var served brokerinput.ReadPlan
	if json.Unmarshal(response.Body.Bytes(), &served) != nil || served.Allocator == nil ||
		len(served.Allocator.Awards) != 1 || served.Allocator.Awards[0].Reason != "queued_demand" ||
		served.Allocator.Decisions[0].RepositoryWeight != 2 || served.Allocator.Held.Global.Runners != 1 ||
		served.Allocator.Risks[0] != "destination_unestablished" {
		t.Fatalf("allocator section not inspectable: %s", response.Body.String())
	}
	after, _ := store.ReadSnapshot(context.Background())
	if JSONSnapshotEqual(t, before, after) != true {
		t.Fatal("allocator proposal read changed admission state")
	}
}

func JSONSnapshotEqual(t *testing.T, before, after any) bool {
	t.Helper()
	rawBefore, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	rawAfter, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	return string(rawBefore) == string(rawAfter)
}
