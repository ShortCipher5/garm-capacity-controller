package managedprojection

import (
	"bytes"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/managedboundary"
	"io"
	"strings"
)

// Project only inventory identity/state, never runner credentials or private job
// details. Keep every row for an authorized provider, even an unknown pool:
// omitting such rows could incorrectly release a physical accounting charge.
func Instances(raw []byte, providers map[string]bool) ([]byte, error) {
	if len(raw) > 4<<20 || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return nil, managedboundary.ErrBoundary
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
		return nil, managedboundary.ErrBoundary
	}
	allowed := map[string]bool{}
	for _, k := range []string{"id", "generation", "name", "provider_name", "provider_id", "pool_id", "scale_set_id", "status", "runner_status", "agent_id", "job"} {
		allowed[k] = true
	}
	output := []map[string]json.RawMessage{}
	for _, r := range rows {
		d := json.NewDecoder(bytes.NewReader(r))
		tok, err := d.Token()
		if err != nil || tok != json.Delim('{') {
			return nil, managedboundary.ErrBoundary
		}
		fields := map[string]json.RawMessage{}
		for d.More() {
			t, err := d.Token()
			key, ok := t.(string)
			if err != nil || !ok {
				return nil, managedboundary.ErrBoundary
			}
			if _, exists := fields[key]; exists {
				return nil, managedboundary.ErrBoundary
			}
			var v json.RawMessage
			if d.Decode(&v) != nil {
				return nil, managedboundary.ErrBoundary
			}
			fields[key] = v
			for known := range allowed {
				if strings.EqualFold(key, known) && key != known {
					return nil, managedboundary.ErrBoundary
				}
			}
		}
		if _, err := d.Token(); err != nil || d.Decode(new(any)) != io.EOF {
			return nil, managedboundary.ErrBoundary
		}
		var provider string
		if json.Unmarshal(fields["provider_name"], &provider) != nil || provider == "" {
			return nil, managedboundary.ErrBoundary
		}
		if !providers[provider] {
			continue
		}
		p := map[string]json.RawMessage{}
		for k, v := range fields {
			if !allowed[k] {
				continue
			}
			if k == "job" && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				v = json.RawMessage("{}")
			}
			p[k] = v
		}
		output = append(output, p)
	}
	return json.Marshal(output)
}
