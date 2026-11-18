//go:build linux

package nativebundle

import (
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
)

// RegistrationCatalogSnapshot contains deployment configuration references and
// hashes only, never private file bytes. Persist only in protected local state;
// this is not a browser DTO, authority observation or permission to materialize.
type RegistrationCatalogSnapshot struct {
	Version     string
	Catalog     RegistrationCatalog
	Commitments map[string]string
}

func cloneRegistrationSnapshot(s RegistrationCatalogSnapshot) RegistrationCatalogSnapshot {
	b, _ := json.Marshal(s)
	var out RegistrationCatalogSnapshot
	_ = json.Unmarshal(b, &out)
	return out
}
func (c *RegistrationCompiler) CatalogSnapshot() RegistrationCatalogSnapshot {
	if c == nil {
		return RegistrationCatalogSnapshot{}
	}
	return cloneRegistrationSnapshot(c.snapshot)
}
func (s RegistrationCatalogSnapshot) Revision() string {
	if s.Version != RegistrationVersion {
		return ""
	}
	values := map[string]any{"version": s.Version, "catalog": s.Catalog}
	for k, v := range s.Commitments {
		if k == "version" || k == "catalog" || !hash(v) {
			return ""
		}
		values[k] = v
	}
	return nativeplanning.Digest(values)
}

// Historical preserves the original commitment/metadata grammar. It accepts
// only an exact subset of the current frozen catalog, including unchanged
// private-byte commitments, reference paths, endpoint scope and output parents.
// It never reloads older credentials or silently replaces an existing alias.
func (c *RegistrationCompiler) Historical(s RegistrationCatalogSnapshot) (*RegistrationCompiler, error) {
	if c == nil || s.Version != RegistrationVersion || s.Revision() == "" {
		return nil, ErrRegistration
	}
	old := s.Catalog
	now := c.snapshot.Catalog
	if old.PrivateParent != now.PrivateParent || old.OutputParent != now.OutputParent || old.JournalParent != now.JournalParent || len(old.Templates) < 1 || len(old.Identities) < 1 || len(old.Administrators) < 1 || len(old.Templates) > len(now.Templates) || len(old.Identities) > len(now.Identities) || len(old.Administrators) > len(now.Administrators) {
		return nil, ErrRegistration
	}
	out := &RegistrationCompiler{templates: map[string]frozenTemplate{}, identities: map[string]frozenIdentity{}, administrators: map[string]RegistrationAdministrator{}, privateParent: c.privateParent, outputParent: c.outputParent, journalParent: c.journalParent, revision: s.Revision(), snapshot: cloneRegistrationSnapshot(s)}
	expected := map[string]string{}
	for alias, spec := range old.Templates {
		current, ok := now.Templates[alias]
		if !ok || nativeplanning.Digest(spec) != nativeplanning.Digest(current) {
			return nil, ErrRegistration
		}
		out.templates[alias] = c.templates[alias]
		expected["template:"+alias] = c.snapshot.Commitments["template:"+alias]
	}
	for alias, spec := range old.Administrators {
		current, ok := now.Administrators[alias]
		if !ok || nativeplanning.Digest(spec) != nativeplanning.Digest(current) {
			return nil, ErrRegistration
		}
		out.administrators[alias] = c.administrators[alias]
	}
	for alias, spec := range old.Identities {
		current, ok := now.Identities[alias]
		_, template := out.templates[spec.Template]
		_, admin := out.administrators[spec.Administrator]
		if !ok || !template || !admin || nativeplanning.Digest(spec) != nativeplanning.Digest(current) {
			return nil, ErrRegistration
		}
		out.identities[alias] = c.identities[alias]
		for _, name := range []string{"owner-server.crt", "owner-server.key", "owner-client.crt", "owner-client.key", "controller-ca.crt"} {
			key := alias + ":" + name
			expected[key] = c.snapshot.Commitments[key]
		}
	}
	if nativeplanning.Digest(expected) != nativeplanning.Digest(s.Commitments) {
		return nil, ErrRegistration
	}
	return out, nil
}
