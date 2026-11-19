// Offline byte-derivation checker only; never imports or accepts an image.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type exporter struct {
	ServerVersion string  `json:"server_version"`
	APIVersion    string  `json:"api_version"`
	GitCommit     string  `json:"engine_git_commit"`
	Platform      string  `json:"platform"`
	Driver        string  `json:"storage_driver"`
	ImageStore    *string `json:"image_store"`
}
type input struct {
	ImageID  string            `json:"image_id"`
	Config   []byte            `json:"config"`
	Nodes    map[string][]byte `json:"nodes"`
	Exporter exporter          `json:"exporter"`
}
type result struct {
	Schema     string `json:"schema"`
	Derived    int    `json:"derived_nodes"`
	Matched    int    `json:"matched_nodes"`
	Derivation bool   `json:"ancillary_derivation_verified"`
	Acceptance bool   `json:"archive_accepted"`
	Deployment bool   `json:"deployment_authorized"`
}

func checkedJSON(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d, 0) != nil {
		return errPrefix
	}
	if _, e := d.Token(); e != io.EOF {
		return errPrefix
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return errPrefix
	}
	return nil
}
func prove(v input) (result, error) {
	m := v.Exporter
	if m.ServerVersion != "28.0.4" || m.APIVersion != "1.48" ||
		(m.GitCommit != "6430e49" && m.GitCommit != "6430e49a55babd9b8f4d08e70ecb2b68900770fe") ||
		m.Platform != "linux/amd64" || m.Driver != "overlay2" || m.ImageStore != nil ||
		!valid(v.ImageID) || len(v.Config) == 0 || len(v.Config) > 16<<20 || sum(v.Config) != v.ImageID ||
		len(v.Nodes) < 1 || len(v.Nodes) > 256 {
		return result{}, errPrefix
	}
	var img imageV28
	if checkedJSON(v.Config, &img) != nil {
		return result{}, errPrefix
	}
	offset := 0
	found := false
	for digest, raw := range v.Nodes {
		if !valid(digest) || len(raw) == 0 || len(raw) > 256<<10 || sum(raw) != digest {
			return result{}, errPrefix
		}
		var node v1V28
		if checkedJSON(raw, &node) != nil {
			return result{}, errPrefix
		}
		if node.Created != nil && node.Created.Equal(time.Unix(0, 0)) {
			_, seconds := node.Created.Zone()
			if seconds%60 != 0 || seconds < -14*3600 || seconds > 14*3600 {
				return result{}, errPrefix
			}
			if found && seconds != offset {
				return result{}, errPrefix
			}
			offset = seconds
			found = true
		}
	}
	if len(img.RootFS.DiffIDs) > 1 && !found {
		return result{}, errPrefix
	}
	// Source exporter uses time.Local for generated epoch nodes. Offset is private
	// observed metadata, constrained to one zero-instant representation and finally
	// confirmed by full byte equality. Never modify daemon/host timezone.
	previous := time.Local
	time.Local = time.FixedZone("observed-exporter-offset", offset)
	defer func() { time.Local = previous }()
	nodes, e := DeriveV28(v.Config)
	if e != nil || len(nodes) != len(v.Nodes) {
		return result{}, errPrefix
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		raw, ok := v.Nodes[n.BlobDigest]
		if !ok || seen[n.BlobDigest] || !bytes.Equal(raw, n.Bytes) {
			return result{}, errPrefix
		}
		seen[n.BlobDigest] = true
	}
	return result{Schema: "moby-v28-ancillary-proof/v1", Derived: len(nodes), Matched: len(nodes), Derivation: true}, nil
}
func run(reader io.Reader, writer io.Writer) error {
	raw, e := io.ReadAll(io.LimitReader(reader, (112<<20)+1))
	if e != nil || len(raw) > 112<<20 {
		return errPrefix
	}
	var v input
	if checkedJSON(raw, &v) != nil {
		return errPrefix
	}
	value, e := prove(v)
	if e != nil {
		return e
	}
	return json.NewEncoder(writer).Encode(value)
}
func main() {
	if len(os.Args) != 1 || run(os.Stdin, os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, "Ancillary derivation unavailable; no archive acceptance")
		os.Exit(1)
	}
}
