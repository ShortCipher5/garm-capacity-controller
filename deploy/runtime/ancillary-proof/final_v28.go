// Narrow source-study projection for Moby v28.0.4, commit6430e49a55babd9b8f4d08e70ecb2b68900770fe.
// Unsupported optional healthchecks fail closed. No runtime/archive wiring.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

type stringsV28 []string

func (s *stringsV28) UnmarshalJSON(b []byte) error {
	p := make([]string, 0, 1)
	if e := json.Unmarshal(b, &p); e != nil {
		var single string
		if e = json.Unmarshal(b, &single); e != nil {
			return e
		}
		p = append(p, single)
	}
	*s = p
	return nil
}

type configV28 struct {
	Hostname        string
	Domainname      string
	User            string
	AttachStdin     bool
	AttachStdout    bool
	AttachStderr    bool
	ExposedPorts    map[string]struct{} `json:",omitempty"`
	Tty             bool
	OpenStdin       bool
	StdinOnce       bool
	Env             []string
	Cmd             stringsV28
	Healthcheck     json.RawMessage `json:",omitempty"`
	ArgsEscaped     bool            `json:",omitempty"`
	Image           string
	Volumes         map[string]struct{}
	WorkingDir      string
	Entrypoint      stringsV28
	NetworkDisabled bool   `json:",omitempty"`
	MacAddress      string `json:",omitempty"`
	OnBuild         []string
	Labels          map[string]string
	StopSignal      string     `json:",omitempty"`
	StopTimeout     *int       `json:",omitempty"`
	Shell           stringsV28 `json:",omitempty"`
}
type v1V28 struct {
	ID              string     `json:"id,omitempty"`
	Parent          string     `json:"parent,omitempty"`
	Comment         string     `json:"comment,omitempty"`
	Created         *time.Time `json:"created"`
	Container       string     `json:"container,omitempty"`
	ContainerConfig configV28  `json:"container_config,omitempty"`
	DockerVersion   string     `json:"docker_version,omitempty"`
	Author          string     `json:"author,omitempty"`
	Config          *configV28 `json:"config,omitempty"`
	Architecture    string     `json:"architecture,omitempty"`
	Variant         string     `json:"variant,omitempty"`
	OS              string     `json:"os,omitempty"`
	Size            int64      `json:",omitempty"`
}
type imageV28 struct {
	v1V28
	Parent string `json:"parent,omitempty"` // Shadows V1Image.Parent exactly as upstream.
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
	History    json.RawMessage `json:"history"`
	OSVersion  string          `json:"os.version"`
	OSFeatures []string        `json:"os.features"`
}

// DeriveV28 returns candidate exact ancillary bytes under the pinned source model.
// It is NOT proof that a daemon used that model or that actual archive blobs match.
func DeriveV28(raw []byte) ([]Node, error) {
	if len(raw) == 0 || len(raw) > 16<<20 {
		return nil, errPrefix
	}
	// Reject ambiguous duplicate keys before typed Go conversion.
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d, 0) != nil {
		return nil, errPrefix
	}
	if _, e := d.Token(); e != io.EOF {
		return nil, errPrefix
	}
	var img imageV28
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&img) != nil || img.OS != "linux" || img.Architecture != "amd64" ||
		img.RootFS.Type != "layers" || len(img.RootFS.DiffIDs) < 1 || len(img.RootFS.DiffIDs) > 256 ||
		len(img.ContainerConfig.Healthcheck) != 0 || img.Config != nil && len(img.Config.Healthcheck) != 0 {
		return nil, errPrefix
	}
	for _, diff := range img.RootFS.DiffIDs {
		if !valid(diff) {
			return nil, errPrefix
		}
	}
	var nodes []Node
	if len(img.RootFS.DiffIDs) > 1 {
		var e error
		nodes, e = Derive(img.RootFS.DiffIDs)
		if e != nil {
			return nil, e
		}
	}
	parent := ""
	if len(nodes) > 0 {
		var previous neutral
		if json.Unmarshal(nodes[len(nodes)-1].Bytes, &previous) != nil {
			return nil, errPrefix
		}
		parent = "sha256:" + previous.ID
	}
	chain := img.RootFS.DiffIDs[0]
	for _, diff := range img.RootFS.DiffIDs[1:] {
		chain = sum([]byte(chain + " " + diff))
	}
	v := img.v1V28
	v.ID = ""
	initial, e := json.Marshal(v)
	if e != nil {
		return nil, errPrefix
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(initial, &object) != nil {
		return nil, errPrefix
	}
	object["layer_id"], _ = json.Marshal(chain)
	if parent != "" {
		object["parent"], _ = json.Marshal(parent)
	}
	initial, e = json.Marshal(object)
	if e != nil {
		return nil, errPrefix
	}
	identity := sum(initial)
	v.ID = identity[7:]
	if parent != "" {
		v.Parent = parent[7:]
	}
	v.OS = img.OS
	saved, e := json.Marshal(v)
	if e != nil {
		return nil, errPrefix
	}
	return append(nodes, Node{BlobDigest: sum(saved), Bytes: saved}), nil
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errPrefix
	}
	token, e := d.Token()
	if e != nil {
		return errPrefix
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return errPrefix
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errPrefix
			}
			seen[s] = true
			if uniqueJSON(d, depth+1) != nil {
				return errPrefix
			}
		}
		token, e = d.Token()
		if e != nil || token != json.Delim('}') {
			return errPrefix
		}
	case '[':
		for d.More() {
			if uniqueJSON(d, depth+1) != nil {
				return errPrefix
			}
		}
		token, e = d.Token()
		if e != nil || token != json.Delim(']') {
			return errPrefix
		}
	default:
		return errPrefix
	}
	return nil
}
