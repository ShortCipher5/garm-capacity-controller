package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAdvisoryStaticConfigurationRefusesUnsupportedMode(t *testing.T) {
	if source, err := allocatorAdvisorySource(configuration{}); err != nil || source != nil {
		t.Fatal(source, err)
	}
	for _, seconds := range []int{0, 14, 61} {
		c := configuration{AllocatorAdvisory: &allocatorAdvisoryConfiguration{IntervalSeconds: seconds, RepositoryIDs: []string{"repo"}}}
		if _, err := allocatorAdvisorySource(c); err == nil {
			t.Fatal("invalid interval accepted", seconds)
		}
	}
	c := configuration{AllocatorAdvisory: &allocatorAdvisoryConfiguration{IntervalSeconds: 30, RepositoryIDs: []string{"repo"}}}
	if _, err := allocatorAdvisorySource(c); err == nil {
		t.Fatal("unsupported transport accepted")
	}
}

func TestStockMaximumPreviewConfigIsExplicitAndRequiresAdvisory(t *testing.T) {
	var c allocatorAdvisoryConfiguration
	if err := json.Unmarshal([]byte(`{"repository_ids":["repo"],"interval_seconds":30}`), &c); err != nil || c.StockMaximumPreview {
		t.Fatal(c, err)
	}
	if err := json.Unmarshal([]byte(`{"stock_maximum_preview":true}`), &c); err != nil || !c.StockMaximumPreview {
		t.Fatal(c, err)
	}
	if source, err := allocatorAdvisorySource(configuration{AllocatorAdvisory: &c}); err == nil || source != nil {
		t.Fatal("preview bypassed managed read-only advisory configuration")
	}
}

func TestStockIdlePreviewConfigurationDefaultOff(t *testing.T) {
	var c allocatorAdvisoryConfiguration
	if err := json.Unmarshal([]byte(`{"interval_seconds":30}`), &c); err != nil || c.StockIdlePreview {
		t.Fatal(c, err)
	}
	if err := json.Unmarshal([]byte(`{"stock_idle_preview":true}`), &c); err != nil || !c.StockIdlePreview {
		t.Fatal(c, err)
	}
	if source, err := allocatorAdvisorySource(configuration{AllocatorAdvisory: &c}); err == nil || source != nil {
		t.Fatal("idle bypassed existing managed scope")
	}
}

func TestAllocatorProposalConfigurationDefaultOffAndSourceBound(t *testing.T) {
	var c allocatorAdvisoryConfiguration
	if err := json.Unmarshal([]byte(`{"repository_ids":["repo"],"interval_seconds":30}`), &c); err != nil || c.AllocatorProposal {
		t.Fatal(c, err)
	}
	if err := json.Unmarshal([]byte(`{"allocator_proposal":true}`), &c); err != nil || !c.AllocatorProposal {
		t.Fatal(c, err)
	}
	// The proposal stays on the read-only advisory transport: the same managed
	// source validation applies and the writer path stays unreachable.
	if source, err := allocatorAdvisorySource(configuration{AllocatorAdvisory: &c}); err == nil || source != nil {
		t.Fatal("allocator proposal bypassed managed read-only advisory configuration")
	}
}

func TestAllocatorProposalNeverImpliesAllocatorLoop(t *testing.T) {
	// Opting the advisory tick into the observe-only allocator proposal must
	// never produce or enable the separate grant writer service, and the
	// writer configuration never enables the advisory reader.
	c := configuration{AllocatorAdvisory: &allocatorAdvisoryConfiguration{
		AllocatorProposal: true, IntervalSeconds: 30, RepositoryIDs: []string{"repo"},
	}}
	service, stop, err := startConfiguredAllocatorLoop(context.Background(), c, nil)
	if err != nil || service != nil {
		t.Fatal("allocator proposal configuration reached the grant writer", service, err)
	}
	stop()
	loop, closeAdvisory, err := configuredAllocatorAdvisory(context.Background(), configuration{
		AllocatorLoop: &allocatorLoopConfiguration{RepositoryIDs: []string{"repo"}, IntervalSeconds: 60, GrantTTLSeconds: 60},
	}, nil)
	if err != nil || loop != nil {
		t.Fatal("allocator loop configuration reached the advisory reader", loop, err)
	}
	closeAdvisory()
}
