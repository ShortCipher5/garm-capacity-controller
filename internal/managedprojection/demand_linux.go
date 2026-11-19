package managedprojection

import (
	"bytes"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedgarm"
	"strings"
)

// Scope and strip at the credential boundary. Unknown provider-owned pools in
// permitted repositories remain visible; visibility is not enrollment.
func Demand(raw []byte, path string, providers, repositories map[string]bool) ([]byte, error) {
	single := strings.HasPrefix(path, "/api/v1/pools/")
	jobs := path == "/api/v1/jobs"
	if !single && path != "/api/v1/pools" && !jobs {
		return nil, managedgarm.ErrBoundary
	}
	if len(raw) > 4<<20 {
		return nil, managedgarm.ErrBoundary
	}
	rows := []json.RawMessage{}
	if single {
		rows = append(rows, raw)
	} else if b := bytes.TrimSpace(raw); len(b) == 0 || b[0] != '[' || json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
		return nil, managedgarm.ErrBoundary
	}
	fields := []string{"id", "provider_name", "repo_id", "org_id", "enterprise_id", "generation", "priority", "max_runners", "min_idle_runners", "enabled", "tags", "flavor", "image", "os_type", "os_arch"}
	if jobs {
		fields = []string{"workflow_job_id", "repo_id", "status", "labels", "created_at", "locked_by", "scaleset_job_id"}
	}
	out := []map[string]json.RawMessage{}
	for _, row := range rows {
		if !singleObject(row) {
			return nil, managedgarm.ErrBoundary
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(row, &obj) != nil {
			return nil, managedgarm.ErrBoundary
		}
		var repo string
		if v, ok := obj["repo_id"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			if json.Unmarshal(v, &repo) != nil {
				return nil, managedgarm.ErrBoundary
			}
		}
		if !repositories[repo] {
			if single {
				return nil, managedgarm.ErrBoundary
			}
			continue
		}
		if !jobs {
			var provider string
			if json.Unmarshal(obj["provider_name"], &provider) != nil || provider == "" {
				return nil, managedgarm.ErrBoundary
			}
			if !providers[provider] {
				if single {
					return nil, managedgarm.ErrBoundary
				}
				continue
			}
			if single {
				var id string
				if json.Unmarshal(obj["id"], &id) != nil || id != strings.TrimPrefix(path, "/api/v1/pools/") {
					return nil, managedgarm.ErrBoundary
				}
			}
		}
		projected := map[string]json.RawMessage{}
		for _, k := range fields {
			if v, ok := obj[k]; ok {
				if k == "tags" && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
					var tags []json.RawMessage
					if json.Unmarshal(v, &tags) != nil || len(tags) > 100 {
						return nil, managedgarm.ErrBoundary
					}
					safe := []map[string]string{}
					for _, tag := range tags {
						if !singleObject(tag) {
							return nil, managedgarm.ErrBoundary
						}
						var fields map[string]json.RawMessage
						_ = json.Unmarshal(tag, &fields)
						var name string
						if json.Unmarshal(fields["name"], &name) != nil || len(name) > 256 {
							return nil, managedgarm.ErrBoundary
						}
						safe = append(safe, map[string]string{"name": name})
					}
					v, _ = json.Marshal(safe)
				}
				projected[k] = v
			}
		}
		out = append(out, projected)
	}
	if single {
		if len(out) != 1 {
			return nil, managedgarm.ErrBoundary
		}
		return json.Marshal(out[0])
	}
	return json.Marshal(out)
}
