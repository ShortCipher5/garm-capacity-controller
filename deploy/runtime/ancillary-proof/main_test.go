package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func proofFixture(t *testing.T, n int) input {
	t.Helper()
	raw := configFixture(n)
	nodes, e := DeriveV28(raw)
	if e != nil {
		t.Fatal(e)
	}
	v := input{ImageID: sum(raw), Config: raw, Nodes: map[string][]byte{}, Exporter: exporter{
		ServerVersion: "28.0.4", APIVersion: "1.48", GitCommit: "6430e49", Platform: "linux/amd64", Driver: "overlay2"}}
	for _, node := range nodes {
		v.Nodes[node.BlobDigest] = node.Bytes
	}
	return v
}
func TestExactDerivationAndClosedOutput(t *testing.T) {
	for _, offset := range []int{0, -8 * 3600} {
		old := time.Local
		time.Local = time.FixedZone("synthetic", offset)
		v := proofFixture(t, 3)
		time.Local = old
		raw, _ := json.Marshal(v)
		var out bytes.Buffer
		if run(bytes.NewReader(raw), &out) != nil {
			t.Fatal("proof")
		}
		var r result
		if json.Unmarshal(out.Bytes(), &r) != nil || !r.Derivation || r.Derived != 3 || r.Matched != 3 || r.Acceptance || r.Deployment {
			t.Fatal("result")
		}
		if bytes.Contains(out.Bytes(), []byte("SYNTHETIC")) {
			t.Fatal("data leaked")
		}
	}
}
func TestProofRefusesUnboundOrChangedInput(t *testing.T) {
	mutations := []func(*input){
		func(v *input) { v.Exporter.GitCommit = "different" },
		func(v *input) { v.Exporter.ServerVersion = "29.0.0" },
		func(v *input) { v.Exporter.Driver = "overlayfs" },
		func(v *input) { v.Exporter.ImageStore = new(string) },
		func(v *input) { v.ImageID = "sha256:" + string(bytes.Repeat([]byte("f"), 64)) },
		func(v *input) {
			for key := range v.Nodes {
				delete(v.Nodes, key)
				break
			}
		},
		func(v *input) {
			for key, raw := range v.Nodes {
				delete(v.Nodes, key)
				raw = append(raw, ' ')
				v.Nodes[sum(raw)] = raw
				break
			}
		},
	}
	for _, change := range mutations {
		v := proofFixture(t, 2)
		change(&v)
		raw, _ := json.Marshal(v)
		var out bytes.Buffer
		if run(bytes.NewReader(raw), &out) == nil || out.Len() != 0 {
			t.Fatal("unverified output")
		}
	}
}
