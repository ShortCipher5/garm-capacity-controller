package main

import "testing"

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
