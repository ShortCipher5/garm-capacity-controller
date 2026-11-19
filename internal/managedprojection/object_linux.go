package managedprojection

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

func singleObject(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	v, e := d.Token()
	if e != nil || v != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok || seen[key] || key != strings.ToLower(key) {
			return false
		}
		seen[key] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return false
		}
	}
	v, e = d.Token()
	return e == nil && v == json.Delim('}') && d.Decode(new(any)) == io.EOF
}
