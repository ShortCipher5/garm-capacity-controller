package brokerinput

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStockIdleDecisionPreviewOptionalWireAndCacheExpiry(t *testing.T) {
	now := time.Now()
	plan := ReadPlan{ExpiresAt: now.Add(time.Minute)}
	if raw, err := json.Marshal(plan); err != nil || strings.Contains(string(raw), "stock_idle_decision_preview") {
		t.Fatalf("unconfirmed decision was not omitted: %s %v", raw, err)
	}
	plan.StockIdleDecisionPreview = &StockIdleDecisionPreview{
		StockIdleProposal: StockIdleProposal{PoolID: pool, RepositoryID: repo, ObservedMinIdle: 0, SuggestedMinIdle: 1, UnchangedMaximum: 2},
		Direction:         "increase", HostEvidence: "configured_only", ExpiresAt: now.Add(5 * time.Second),
	}
	plan.StockIdlePreview = &StockIdlePreview{State: "no_confirmed_proposals", ExpiresAt: plan.ExpiresAt}
	s := &AdvisoryLoop{plan: &plan}
	first, err := s.ReadPlan(context.Background())
	if err != nil || first.StockIdleDecisionPreview == nil || first.StockIdleDecisionPreview.Direction != "increase" ||
		first.StockIdlePreview == nil || first.StockIdlePreview.State != "no_confirmed_proposals" {
		t.Fatalf("optional result broke compatibility: %+v %v", first, err)
	}
	first.StockIdleDecisionPreview.Direction = "decrease"
	if plan.StockIdleDecisionPreview.Direction != "increase" {
		t.Fatal("read changed cached decision")
	}
	plan.StockIdleDecisionPreview.ExpiresAt = now
	next, err := s.ReadPlan(context.Background())
	if err != nil || next.StockIdleDecisionPreview != nil || next.StockIdlePreview == nil {
		t.Fatalf("independent expiry affected ordinary advice or retained stale row: %+v %v", next, err)
	}
}
