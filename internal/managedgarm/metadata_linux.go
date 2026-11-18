package managedgarm

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
	"io"
	"net/http"
	"strings"
)

// ReadScopedMetadata returns original stock JIT bytes only after observing the
// exact pending instance in a configured repository/provider pool. The instance
// token is used only at stock's fixed metadata endpoint, never as admin auth.
func (s *Session) ReadScopedMetadata(ctx context.Context, pool, name, file, instanceToken string, providers, repositories map[string]bool) ([]byte, error) {
	if !stockUUID(pool) || !metadataName(name) || !metadataFile(file) || len(instanceToken) == 0 || len(instanceToken) > 16384 || strings.IndexFunc(instanceToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, ErrBoundary
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.token == "" {
		return nil, ErrBoundary
	}
	return readScopedMetadata(func(path, token string) ([]byte, error) { return s.exchange(ctx, http.MethodGet, path, token, nil) }, s.token, pool, name, file, instanceToken, providers, repositories)
}

// The private read projection is separated for failure-injection tests; only
// Session supplies its production transport and root-held admin credential.
func readScopedMetadata(read func(string, string) ([]byte, error), adminToken, pool, name, file, instanceToken string, providers, repositories map[string]bool) ([]byte, error) {
	readPool := func() (stockidentity.Facts, error) {
		raw, e := read("/api/v1/pools/"+pool, adminToken)
		if e != nil {
			return stockidentity.Facts{}, ErrBoundary
		}
		f, e := stockidentity.ParseFacts(raw)
		if e != nil || f.Pool != pool || !providers[f.Provider] || !repositories[f.Repository] {
			return stockidentity.Facts{}, ErrBoundary
		}
		return f, nil
	}
	facts, e := readPool()
	if e != nil {
		return nil, ErrBoundary
	}
	readInstance := func() (pendingMetadataInstance, error) {
		raw, e := read("/api/v1/instances/"+name, adminToken)
		if e != nil {
			return pendingMetadataInstance{}, ErrBoundary
		}
		return parsePendingMetadata(raw, pool, name, facts.Provider)
	}
	before, e := readInstance()
	if e != nil {
		return nil, ErrBoundary
	}
	readFile := func(file string) ([]byte, error) {
		raw, e := read("/api/v1/metadata/credentials/"+file, instanceToken)
		if e != nil || len(raw) == 0 || len(raw) > 1<<19 {
			return nil, ErrBoundary
		}
		return raw, nil
	}
	runner, e := readFile("runner")
	if e != nil || !metadataRunnerMatches(runner, before) {
		return nil, ErrBoundary
	}
	result := runner
	if file != "runner" {
		result, e = readFile(file)
		if e != nil {
			return nil, ErrBoundary
		}
	}
	afterRunner, e := readFile("runner")
	if e != nil || !bytes.Equal(runner, afterRunner) {
		return nil, ErrBoundary
	}
	after, e := readInstance()
	if e != nil || after != before {
		return nil, ErrBoundary
	}
	afterPool, e := readPool()
	if e != nil || afterPool != facts {
		return nil, ErrBoundary
	}
	return result, nil
}
func metadataName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func metadataFile(s string) bool {
	return s == "runner" || s == "credentials" || s == "credentials_rsaparams"
}

type pendingMetadataInstance struct {
	ID, Name, Pool, Provider string
	AgentID                  int64
}

func metadataObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > 1<<20 {
		return nil, ErrBoundary
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return nil, ErrBoundary
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		tok, e = d.Token()
		key, ok := tok.(string)
		if e != nil || !ok {
			return nil, ErrBoundary
		}
		key = strings.ToLower(key)
		if _, ok := out[key]; ok {
			return nil, ErrBoundary
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrBoundary
		}
		out[key] = v
	}
	tok, e = d.Token()
	if e != nil || tok != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrBoundary
	}
	return out, nil
}
func parsePendingMetadata(raw []byte, pool, name, provider string) (pendingMetadataInstance, error) {
	f, e := metadataObject(raw)
	if e != nil {
		return pendingMetadataInstance{}, e
	}
	var v pendingMetadataInstance
	for k, p := range map[string]any{"id": &v.ID, "name": &v.Name, "pool_id": &v.Pool, "provider_name": &v.Provider, "agent_id": &v.AgentID} {
		if json.Unmarshal(f[k], p) != nil {
			return pendingMetadataInstance{}, ErrBoundary
		}
	}
	var status, runner, providerID string
	var scaleset uint64
	if !stockUUID(v.ID) || v.Name != name || v.Pool != pool || v.Provider != provider || v.AgentID <= 0 || json.Unmarshal(f["status"], &status) != nil || (status != "creating" && status != "pending_create") || json.Unmarshal(f["runner_status"], &runner) != nil || runner != "pending" {
		return pendingMetadataInstance{}, ErrBoundary
	}
	if b, ok := f["provider_id"]; ok && (json.Unmarshal(b, &providerID) != nil || providerID != "") {
		return pendingMetadataInstance{}, ErrBoundary
	}
	if b, ok := f["scale_set_id"]; ok && (json.Unmarshal(b, &scaleset) != nil || scaleset != 0) {
		return pendingMetadataInstance{}, ErrBoundary
	}
	return v, nil
}
func metadataRunnerMatches(raw []byte, v pendingMetadataInstance) bool {
	f, e := metadataObject(raw)
	if e != nil {
		return false
	}
	var id int64
	var name string
	var ephemeral bool
	return json.Unmarshal(f["agentid"], &id) == nil && json.Unmarshal(f["agentname"], &name) == nil && json.Unmarshal(f["ephemeral"], &ephemeral) == nil && id == v.AgentID && name == v.Name && ephemeral
}

// Scoped read failures do not invalidate the unrelated root admin credential.
// All responses still pass topology/TLS and post-read boundary checks first.
func (s *Session) checkResponseStatus(method, path string, status int) error {
	if status == http.StatusOK {
		return nil
	}
	if method == http.MethodGet {
		const metadata = "/api/v1/metadata/credentials/"
		if strings.HasPrefix(path, metadata) && metadataFile(strings.TrimPrefix(path, metadata)) && (status == 401 || status == 403 || status == 404) {
			return ErrBoundary
		}
		const instances = "/api/v1/instances/"
		if strings.HasPrefix(path, instances) && metadataName(strings.TrimPrefix(path, instances)) && status == 404 {
			return ErrBoundary
		}
	}
	s.revoked = true
	s.token = ""
	return ErrBoundary
}
