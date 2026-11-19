// Package neutralprefix is an unintegrated offline source-study prototype.
// Source: moby/moby 6430e49a55babd9b8f4d08e70ecb2b68900770fe.
// It proves only generated NONFINAL V1 nodes, never complete archive acceptance.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var errPrefix = errors.New("neutral prefix unavailable")

// Exact non-omitempty zero fields, in declaration order, from the pinned
// api/types/container/config.go. Optional fields are zero/omitted for neutral
// intermediate nodes. This is NOT a decoder for arbitrary container configs.
type zeroConfig struct {
	Hostname     string
	Domainname   string
	User         string
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
	Tty          bool
	OpenStdin    bool
	StdinOnce    bool
	Env          []string
	Cmd          []string
	Image        string
	Volumes      map[string]struct{}
	WorkingDir   string
	Entrypoint   []string
	OnBuild      []string
	Labels       map[string]string
}

// Only fields nonzero during neutral generation, preserving pinned V1Image
// declaration order. All other original fields are omitted zero values.
type neutral struct {
	ID              string     `json:"id,omitempty"`
	Parent          string     `json:"parent,omitempty"`
	Created         *time.Time `json:"created"`
	ContainerConfig zeroConfig `json:"container_config"`
	OS              string     `json:"os,omitempty"`
}
type Node struct {
	BlobDigest string
	Bytes      []byte
}

func sum(raw []byte) string { v := sha256.Sum256(raw); return "sha256:" + hex.EncodeToString(v[:]) }
func valid(v string) bool {
	if len(v) != 71 || v[:7] != "sha256:" {
		return false
	}
	for _, c := range v[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Derive excludes the last DiffID because Moby uses the final V1Image there.
// No final-node semantics, runtime engine match or archive closure is asserted.
func Derive(diffIDs []string) ([]Node, error) {
	if len(diffIDs) < 2 || len(diffIDs) > 256 {
		return nil, errPrefix
	}
	for _, v := range diffIDs {
		if !valid(v) {
			return nil, errPrefix
		}
	}
	chain, parent := "", ""
	var out []Node
	for i, diff := range diffIDs[:len(diffIDs)-1] {
		if i == 0 {
			chain = diff
		} else {
			chain = sum([]byte(chain + " " + diff))
		}
		epoch := time.Unix(0, 0)
		v := neutral{Created: &epoch}
		raw, e := json.Marshal(v)
		if e != nil {
			return nil, errPrefix
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return nil, errPrefix
		}
		object["layer_id"], _ = json.Marshal(chain)
		if parent != "" {
			object["parent"], _ = json.Marshal(parent)
		}
		idInput, e := json.Marshal(object)
		if e != nil {
			return nil, errPrefix
		}
		identity := sum(idInput)
		v.ID = identity[7:]
		if parent != "" {
			v.Parent = parent[7:]
		}
		v.OS = "linux"
		raw, e = json.Marshal(v)
		if e != nil {
			return nil, errPrefix
		}
		out = append(out, Node{BlobDigest: sum(raw), Bytes: raw})
		parent = identity
	}
	return out, nil
}

// Match counts only exact derived neutral nodes. Unrelated input nodes are not
// accepted or authorized: this helper intentionally makes no claim about them.
func Match(diffIDs []string, blobs map[string][]byte) (int, error) {
	nodes, e := Derive(diffIDs)
	if e != nil || len(blobs) > 256 {
		return 0, errPrefix
	}
	for _, n := range nodes {
		raw, ok := blobs[n.BlobDigest]
		if !ok || len(raw) > 256*1024 || sum(raw) != n.BlobDigest || !bytes.Equal(raw, n.Bytes) {
			return 0, errPrefix
		}
	}
	return len(nodes), nil
}
