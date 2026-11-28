package main

import (
	"strings"
	"testing"
)

func TestIncusObservationModeCannotMixProvisioning(t *testing.T) {
	observer := hostConfiguration{ObservationServerKeyPin: strings.Repeat("1", 64)}
	legacy := hostConfiguration{}
	for _, tt := range []struct {
		name             string
		hosts            map[string]hostConfiguration
		provision        bool
		externalProvider bool
		wantError        bool
	}{
		{"legacy", map[string]hostConfiguration{"a": legacy}, true, false, false},
		{"read only", map[string]hostConfiguration{"a": observer}, false, false, false},
		{"no provisioning", map[string]hostConfiguration{"a": observer}, true, false, true},
		{"no mixing", map[string]hostConfiguration{"a": observer, "b": legacy}, false, false, true},
		{"no stock callback", map[string]hostConfiguration{"a": observer}, false, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIncusObservationMode(configuration{Hosts: tt.hosts, EnableStockProvider: tt.externalProvider}, tt.provision)
			if (err != nil) != tt.wantError {
				t.Fatalf("got %v, want error %v", err, tt.wantError)
			}
		})
	}
}
