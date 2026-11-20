//go:build linux && stock_read_pilot_harness

package managedgarm

import "testing"

const stockPilotHarnessDirectory = "/var/lib/capacity-stock-readonly-pilot"

// Used by the explicit tagged harness before any profile, filesystem, namespace
// or authentication operation. Selection is not permission or runtime evidence.
func stockPilotHarnessSelection(marker, githubActions, runnerEnvironment string) (bool, error) {
	if marker == "" {
		return false, nil
	}
	if marker != "1" || githubActions != "true" || runnerEnvironment != "github-hosted" {
		return false, ErrBoundary
	}
	return true, nil
}

func TestStockPilotHarnessSelectionClosed(t *testing.T) {
	for _, tc := range []struct {
		marker, github, runner string
		want                   bool
		bad                    bool
	}{
		{"", "", "", false, false},
		{"", "true", "github-hosted", false, false},
		{"1", "true", "github-hosted", true, false},
		{"1", "", "", false, true},
		{"true", "true", "github-hosted", false, true},
		{"0", "true", "github-hosted", false, true},
		{"1\n", "true", "github-hosted", false, true},
		{"1", "True", "github-hosted", false, true},
		{"1", "true", "self-hosted", false, true},
		{"1", "true", "github-hosted\n", false, true},
		{"--bootstrap", "true", "github-hosted", false, true},
	} {
		got, err := stockPilotHarnessSelection(tc.marker, tc.github, tc.runner)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatal("unexpected closed harness selection")
		}
	}
	if stockPilotHarnessDirectory != "/var/lib/capacity-stock-readonly-pilot" {
		t.Fatal("pilot layout drift")
	}
}
