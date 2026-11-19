package main

import (
	"encoding/json"
	"testing"
)

func configFixture(n int) []byte {
	raw, _ := json.Marshal(map[string]any{"created": "2020-01-01T00:00:00Z", "os": "linux", "architecture": "amd64",
		"config": map[string]any{"Cmd": "echo synthetic", "Env": []string{"SYNTHETIC=1"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffs(n)}})
	return raw
}
func TestFinalV28DefaultExpansionExplainsRawConfigMismatch(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		nodes, e := DeriveV28(configFixture(n))
		if e != nil || len(nodes) != n {
			t.Fatal(n, e)
		}
		var got map[string]json.RawMessage
		if json.Unmarshal(nodes[n-1].Bytes, &got) != nil {
			t.Fatal("decode")
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(got["config"], &config) != nil {
			t.Fatal("config")
		}
		if string(config["OnBuild"]) != "null" || string(config["Hostname"]) != `""` || string(config["Cmd"]) != `["echo synthetic"]` {
			t.Fatal("Go default conversion absent")
		}
		if n > 1 {
			var previous neutral
			json.Unmarshal(nodes[n-2].Bytes, &previous)
			var parent string
			json.Unmarshal(got["parent"], &parent)
			if parent != previous.ID {
				t.Fatal("final parent mismatch")
			}
		}
	}
}
func TestFinalV28MalformedAndUnsupportedInput(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"os":"linux","os":"linux"}`), []byte(`{}`), append(configFixture(1), []byte("{}")...),
	} {
		if _, e := DeriveV28(raw); e == nil {
			t.Fatal("malformed accepted")
		}
	}
	var value map[string]any
	json.Unmarshal(configFixture(1), &value)
	value["config"].(map[string]any)["Healthcheck"] = map[string]any{"Test": []string{"NONE"}}
	raw, _ := json.Marshal(value)
	if _, e := DeriveV28(raw); e == nil {
		t.Fatal("unsupported healthcheck accepted")
	}
}
