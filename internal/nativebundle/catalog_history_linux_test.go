//go:build linux

package nativebundle

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogHistoricalCompilerExactRevisionAndSnapshotOwnership(t *testing.T) {
	catalog, r := registrationFixture(t)
	old, e := NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := old.CatalogSnapshot()
	if snapshot.Revision() != old.Revision() {
		t.Fatal("original hash grammar changed")
	}
	r.Revision = old.Revision()
	original, e := old.Compile(r)
	if e != nil {
		t.Fatal(e)
	}
	// An unused administrator alias is an additive catalog change, not a scope
	// change to an existing credential or an excuse to rebind old metadata.
	catalog.Administrators["additional"] = catalog.Administrators["ssh"]
	current, e := NewRegistrationCompiler(catalog)
	if e != nil {
		t.Fatal(e)
	}
	historical, e := current.Historical(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := historical.Compile(r)
	if e != nil || !reflect.DeepEqual(original.Metadata(), replay.Metadata()) {
		t.Fatal("historical configuration changed", e)
	}
	if _, e = current.Compile(r); e == nil {
		t.Fatal("new requests bypass current review revision")
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "BEGIN CERTIFICATE") {
		t.Fatal("private file content journaled")
	}
	returned := current.CatalogSnapshot()
	delete(returned.Catalog.Identities, "owner1")
	returned.Commitments["template:standard"] = strings.Repeat("f", 64)
	if _, e = current.Historical(snapshot); e != nil {
		t.Fatal("snapshot caller mutated compiler", e)
	}
	for _, mode := range []string{"hash", "parent", "extra", "identity", "admin", "missing"} {
		t.Run(mode, func(t *testing.T) {
			bad := cloneRegistrationSnapshot(snapshot)
			switch mode {
			case "hash":
				bad.Commitments["template:standard"] = strings.Repeat("f", 64)
			case "parent":
				bad.Catalog.PrivateParent += "/new"
			case "extra":
				bad.Commitments["invented"] = strings.Repeat("e", 64)
			case "identity":
				v := bad.Catalog.Identities["owner1"]
				v.HostPrefix = ""
				bad.Catalog.Identities["owner1"] = v
			case "admin":
				v := bad.Catalog.Administrators["ssh"]
				v.Ports = []uint16{22, 23}
				bad.Catalog.Administrators["ssh"] = v
			case "missing":
				delete(bad.Commitments, "owner1:owner-client.key")
			}
			if _, e = current.Historical(bad); e == nil {
				t.Fatal("unverified historical configuration")
			}
		})
	}
}
