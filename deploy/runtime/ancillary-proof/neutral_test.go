package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func diffs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
	}
	return out
}
func TestOneNeutralPrefixExactZeroConfig(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	nodes, e := Derive(diffs(2))
	if e != nil || len(nodes) != 1 {
		t.Fatal("derive", e)
	}
	var got map[string]json.RawMessage
	if json.Unmarshal(nodes[0].Bytes, &got) != nil {
		t.Fatal("JSON")
	}
	expected := `{"Hostname":"","Domainname":"","User":"","AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"Tty":false,"OpenStdin":false,"StdinOnce":false,"Env":null,"Cmd":null,"Image":"","Volumes":null,"WorkingDir":"","Entrypoint":null,"OnBuild":null,"Labels":null}`
	if string(got["container_config"]) != expected || string(got["created"]) != `"1970-01-01T00:00:00Z"` || string(got["os"]) != `"linux"` || got["parent"] != nil {
		t.Fatalf("synthetic neutral mismatch created=%s config=%s os=%s parent=%s", got["created"], got["container_config"], got["os"], got["parent"])
	}
	if n, e := Match(diffs(2), map[string][]byte{nodes[0].BlobDigest: nodes[0].Bytes}); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestTwoNeutralPrefixesAndOrderedParents(t *testing.T) {
	nodes, e := Derive(diffs(3))
	if e != nil || len(nodes) != 2 {
		t.Fatal(e)
	}
	var first, second neutral
	json.Unmarshal(nodes[0].Bytes, &first)
	json.Unmarshal(nodes[1].Bytes, &second)
	if second.Parent != first.ID || first.ID == second.ID {
		t.Fatal("parent")
	}
	blobs := map[string][]byte{}
	for _, n := range nodes {
		blobs[n.BlobDigest] = n.Bytes
	}
	if n, e := Match(diffs(3), blobs); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	reversed := diffs(3)
	reversed[0], reversed[1] = reversed[1], reversed[0]
	if _, e := Match(reversed, blobs); e == nil {
		t.Fatal("reordered prefixes accepted")
	}
}
func TestMalformedOrChangedPrefixesRefused(t *testing.T) {
	for _, v := range [][]string{nil, diffs(1), {"sha256:bad", diffs(2)[1]}, diffs(257)} {
		if _, e := Derive(v); e == nil {
			t.Fatal("malformed prefix accepted")
		}
	}
	nodes, _ := Derive(diffs(2))
	if _, e := Match(diffs(2), nil); e == nil {
		t.Fatal("missing node accepted")
	}
	changed := append(append([]byte{}, nodes[0].Bytes...), byte(' '))
	if _, e := Match(diffs(2), map[string][]byte{nodes[0].BlobDigest: changed}); e == nil {
		t.Fatal("changed bytes accepted")
	}
}

func TestNeutralEpochUsesExporterTimezone(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	time.Local = time.FixedZone("synthetic", -8*60*60)
	nodes, e := Derive(diffs(2))
	if e != nil {
		t.Fatal(e)
	}
	var value map[string]json.RawMessage
	json.Unmarshal(nodes[0].Bytes, &value)
	if string(value["created"]) != `"1969-12-31T16:00:00-08:00"` {
		t.Fatal("timezone not preserved")
	}
	time.Local = time.UTC
	utc, e := Derive(diffs(2))
	if e != nil || utc[0].BlobDigest == nodes[0].BlobDigest {
		t.Fatal("different exporter timezone not distinguished")
	}
}
