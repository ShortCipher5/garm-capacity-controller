package garmprotocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func originalPreparation() PreparationRequest {
	return PreparationRequest{Protocol: PreparationVersion, Name: "runner-fixture", PoolID: "pool-fixture", InstanceID: "11111111-1111-4111-8111-111111111111", AgentID: 42, Generation: 0, ProviderName: "capacity", EntityType: "repository", EntityID: "22222222-2222-4222-8222-222222222222", ForgeEndpointName: "github-fixture", JITEnabled: true}
}
func TestPreparationWireRoundTripPreservesExactZeroGenerationAndOriginalIdentity(t *testing.T) {
	for _, jit := range []bool{true, false} {
		r := originalPreparation()
		r.JITEnabled = jit
		if !jit {
			r.AgentID = 0
		}
		data, err := EncodePreparationRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodePreparationRequest(data)
		if err != nil || got != r {
			t.Fatalf("request %+v %v", got, err)
		}
		for _, outcome := range []string{"recorded", "validated"} {
			replay := outcome == "validated"
			dispatched := false
			response := PreparationResponse{PreparationRequest: r, Outcome: outcome, ReservationKey: strings.Repeat("a", 64), RecordSequence: 1, Replay: &replay, Dispatched: &dispatched}
			raw, err := EncodePreparationResponse(response, r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParsePreparationResponse(0, raw, r, outcome)
			if err != nil || got.PreparationRequest != r || got.Generation != 0 {
				t.Fatal("identity drift")
			}
			if _, err := ParsePreparationResponse(75, raw, r, outcome); !errors.Is(err, ErrPreparationProtocol) {
				t.Fatal("defer mistaken for receipt")
			}
		}
	}
}
func TestPreparationRequestRejectsMissingNullDuplicateCasefoldAndSecrets(t *testing.T) {
	raw, _ := EncodePreparationRequest(originalPreparation())
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range preparationRequestKeys {
		t.Run(key, func(t *testing.T) {
			clone := map[string]json.RawMessage{}
			for k, v := range fields {
				clone[k] = v
			}
			delete(clone, key)
			missing, _ := json.Marshal(clone)
			if _, err := DecodePreparationRequest(missing); err == nil {
				t.Fatal("missing accepted")
			}
			clone[key] = json.RawMessage("null")
			null, _ := json.Marshal(clone)
			if _, err := DecodePreparationRequest(null); err == nil {
				t.Fatal("null accepted")
			}
			duplicate := append([]byte("{\""+key+"\":"+string(fields[key])+","), raw[1:]...)
			if _, err := DecodePreparationRequest(duplicate); err == nil {
				t.Fatal("duplicate accepted")
			}
			upper := append([]byte("{\""+strings.ToUpper(key)+"\":"+string(fields[key])+","), raw[1:]...)
			if _, err := DecodePreparationRequest(upper); err == nil {
				t.Fatal("case alias accepted")
			}
		})
	}
	for _, key := range []string{"controller", "source_scope", "actor_fingerprint", "jit_config", "instance_token", "credentials"} {
		injected := append([]byte("{\""+key+"\":\"private-secret-must-not-echo\","), raw[1:]...)
		_, err := DecodePreparationRequest(injected)
		if !errors.Is(err, ErrPreparationProtocol) || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("untrusted metadata accepted/echoed")
		}
	}
	for _, suffix := range []string{" {}", " true", "\n[]"} {
		if _, err := DecodePreparationRequest(append(append([]byte(nil), raw...), []byte(suffix)...)); err == nil {
			t.Fatal("trailing accepted")
		}
	}
	if _, err := DecodePreparationRequest([]byte(strings.Repeat("x", MaximumResponseBytes+1))); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := DecodePreparationRequest(append([]byte{0xff}, raw...)); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
func TestPreparationRequestRejectsCoercionAndMismatchedMode(t *testing.T) {
	valid := originalPreparation()
	raw, _ := EncodePreparationRequest(valid)
	for _, pair := range [][2]string{{`"agent_id":42`, `"agent_id":"42"`}, {`"agent_id":42`, `"agent_id":42.0`}, {`"agent_id":42`, `"agent_id":0`}, {`"agent_id":42`, `"agent_id":-1`}, {`"generation":0`, `"generation":"0"`}, {`"generation":0`, `"generation":-1`}, {`"generation":0`, `"generation":9223372036854775808`}, {`"jit_enabled":true`, `"jit_enabled":"true"`}, {`"jit_enabled":true`, `"jit_enabled":false`}} {
		data := []byte(strings.Replace(string(raw), pair[0], pair[1], 1))
		if _, err := DecodePreparationRequest(data); err == nil {
			t.Fatalf("accepted %s", pair[1])
		}
	}
	for _, mutate := range []func(*PreparationRequest){func(r *PreparationRequest) { r.InstanceID = "00000000-0000-0000-0000-000000000000" }, func(r *PreparationRequest) { r.InstanceID = "11111111-1111-5111-8111-111111111111" }, func(r *PreparationRequest) { r.EntityID = "not-a-uuid" }, func(r *PreparationRequest) { r.ForgeEndpointName = "" }, func(r *PreparationRequest) { r.EntityType = "scaleset" }, func(r *PreparationRequest) { r.Name = "runner\nsecret" }, func(r *PreparationRequest) { r.PoolID = " pool" }, func(r *PreparationRequest) { r.ProviderName = strings.Repeat("p", 129) }} {
		r := valid
		mutate(&r)
		if r.Validate() == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}
func TestPreparationResponseRejectsForgedIdentityAndPermissionFlags(t *testing.T) {
	req := originalPreparation()
	no, replay := false, false
	r := PreparationResponse{PreparationRequest: req, Outcome: "recorded", ReservationKey: strings.Repeat("a", 64), RecordSequence: 7, Replay: &replay, Dispatched: &no}
	raw, _ := EncodePreparationResponse(r, req)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	keys := append(append([]string(nil), preparationRequestKeys...), "outcome", "reservation_key", "record_sequence", "replay", "dispatched")
	for _, key := range keys {
		clone := map[string]json.RawMessage{}
		for k, v := range fields {
			clone[k] = v
		}
		delete(clone, key)
		data, _ := json.Marshal(clone)
		if _, err := ParsePreparationResponse(0, data, req, "recorded"); err == nil {
			t.Fatalf("missing %s", key)
		}
		clone[key] = json.RawMessage("null")
		data, _ = json.Marshal(clone)
		if _, err := ParsePreparationResponse(0, data, req, "recorded"); err == nil {
			t.Fatalf("null %s", key)
		}
	}
	for _, pair := range [][2]string{{`"agent_id":42`, `"agent_id":43`}, {`"generation":0`, `"generation":1`}, {`"record_sequence":7`, `"record_sequence":0`}, {`"record_sequence":7`, `"record_sequence":7.0`}, {`"dispatched":false`, `"dispatched":true`}, {`"outcome":"recorded"`, `"outcome":"reserved"`}} {
		data := []byte(strings.Replace(string(raw), pair[0], pair[1], 1))
		if _, err := ParsePreparationResponse(0, data, req, "recorded"); err == nil {
			t.Fatalf("forged %s", pair[1])
		}
	}
	if _, err := ParsePreparationResponse(0, raw, req, "validated"); err == nil {
		t.Fatal("record ack treated as validation")
	}
	r.Outcome = "validated"
	r.Replay = &no
	if _, err := EncodePreparationResponse(r, req); err == nil {
		t.Fatal("validation invented original record")
	}
}
