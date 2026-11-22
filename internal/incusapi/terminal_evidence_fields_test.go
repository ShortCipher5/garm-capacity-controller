package incusapi

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExactInstanceEvidenceFieldSpellings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inConfig  bool
		field     string
		wantError bool
	}{
		{name: "canonical"},
		{name: "unknown server extension", field: "FutureField"},
		{name: "instance name", field: "Name", wantError: true},
		{name: "expanded config", field: "EXPANDED_CONFIG", wantError: true},
		{name: "generation", inConfig: true, field: "Volatile.UUID.generation", wantError: true},
		{name: "memory", inConfig: true, field: "Limits.Memory", wantError: true},
		{name: "optional memory enforcement", inConfig: true, field: "Limits.Memory.Enforce", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := instanceEvidenceBody()
			if tc.field != "" {
				target := body
				if tc.inConfig {
					target = body["expanded_config"].(map[string]any)
				}
				target[tc.field] = "fixture"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			fields, config, err := exactInstanceEvidenceFields(raw)
			if tc.wantError {
				if !errors.Is(err, ErrResponse) || fields != nil || config != nil {
					t.Fatalf("ambiguous identity fields did not fail closed: %v", err)
				}
			} else if err != nil || fields["name"] == nil || config["volatile.uuid.generation"] == nil {
				t.Fatalf("canonical identity fields were lost: %v", err)
			}
		})
	}
}

func TestExactTerminalOperationFieldSpellings(t *testing.T) {
	for _, tc := range []struct {
		field     string
		wantError bool
	}{
		{},
		{field: "FutureField"},
		{field: "ID", wantError: true},
		{field: "Status", wantError: true},
		{field: "Created_At", wantError: true},
		{field: "Resources", wantError: true},
		{field: "ERR", wantError: true},
	} {
		t.Run(tc.field, func(t *testing.T) {
			body := operationEvidenceBody(RecoveryDelete)
			if tc.field != "" {
				body[tc.field] = "fixture"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			fields, err := exactTerminalOperationFields(raw)
			if tc.wantError {
				if !errors.Is(err, ErrResponse) || fields != nil {
					t.Fatalf("ambiguous terminal fields did not fail closed: %v", err)
				}
			} else if err != nil || fields["id"] == nil || fields["resources"] == nil {
				t.Fatalf("canonical terminal fields were lost: %v", err)
			}
		})
	}
}
