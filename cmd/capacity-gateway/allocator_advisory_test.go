package main

import (
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
