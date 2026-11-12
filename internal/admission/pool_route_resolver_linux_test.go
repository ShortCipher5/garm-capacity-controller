package admission

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func routeResolverFixture(t *testing.T) (*SQLiteStore, *PoolRegistrar, *inactiveRouteSource, *PoolRouteResolver) {
	t.Helper()
	s, r, source := inactiveRouteFixture(t)
	resolver, err := NewPoolRouteResolver(s, r.controller, r.managementIdentity, r.journalLineage, []PoolRouteTemplate{r.templates["small-linux"]})
	if err != nil {
		t.Fatal(err)
	}
	return s, r, source, resolver
}
func TestPoolRouteResolverFreshSnapshotNoSourceOrWriterEffects(t *testing.T) {
	s, r, source, resolver := routeResolverFixture(t)
	ctx := context.Background()
	if _, err := resolver.Resolve(ctx, source.facts.PoolID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unknown route became defaults", err)
	}
	record, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	before := sqliteSnapshot(t, s)
	calls := source.calls.Load()
	got, err := resolver.Resolve(ctx, source.facts.PoolID)
	if err != nil || !got.Record.Equal(record) || got.ReferenceStatus != "current" || got.Record.Mode != "inactive" || got.PolicyRevision != PolicyRevision(before.Config) || !reflect.DeepEqual(got.CurrentPolicy, before.Config.Pools[source.facts.PoolID]) {
		t.Fatal("request snapshot lost canonical identity/current policy", got, err)
	}
	if source.calls.Load() != calls || !reflect.DeepEqual(before, sqliteSnapshot(t, s)) {
		t.Fatal("resolution called source or changed state")
	}
	// Mutating either the returned policy or nested historical identity cannot
	// affect trusted startup references, the next read or durable history.
	got.Record.Identity.Template.Hosts[0] = "forged"
	got.Record.Identity.Template.SourceFingerprints[0] = bindingHash("forged")
	got.Record.Identity.HostBindingSequences["a"] = 999
	*got.Record.Identity.Template.JITEnabled = false
	got.CurrentPolicy.Hosts[0] = "forged"
	again, err := resolver.Resolve(ctx, source.facts.PoolID)
	if err != nil || !again.Record.Equal(record) || again.ReferenceStatus != "current" {
		t.Fatal("caller mutated private/durable catalog", err)
	}
	budget := Resources{CPU: 500, RAM: 512, Runners: 1}
	if err := s.ApplyPolicyChange(ctx, PolicyChange{Revision: got.PolicyRevision, Action: "pool_budget", Target: source.facts.PoolID, Budget: &budget, Reason: "narrow route budget"}); err != nil {
		t.Fatal(err)
	}
	next, err := resolver.Resolve(ctx, source.facts.PoolID)
	if err != nil || next.CurrentPolicy.Budget != budget || next.PolicyRevision == got.PolicyRevision || !next.Record.Equal(record) {
		t.Fatal("resolver cached stale accounting policy", err)
	}
}
func TestPoolRouteResolverReferenceDriftKeepsHistoryButNeverActivates(t *testing.T) {
	for _, kind := range []string{"missing", "revision", "hosts", "source-group", "actors", "jit", "image", "management", "lineage", "controller"} {
		t.Run(kind, func(t *testing.T) {
			s, r, source, _ := routeResolverFixture(t)
			ctx := context.Background()
			original, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
			if err != nil {
				t.Fatal(err)
			}
			template := cloneRouteTemplate(r.templates["small-linux"])
			templates := []PoolRouteTemplate{template}
			controller, management, lineage := r.controller, r.managementIdentity, r.journalLineage
			expected := "changed_template"
			switch kind {
			case "missing":
				templates = nil
				expected = "missing_template"
			case "revision":
				templates[0].Revision = bindingHash("new revision")
			case "hosts":
				templates[0].Hosts = []string{"a"}
			case "source-group":
				templates[0].SourceActorGroup = "other-source"
			case "actors":
				templates[0].SourceFingerprints = []string{bindingHash("other leaf")}
			case "jit":
				v := false
				templates[0].JITEnabled = &v
			case "image":
				templates[0].Execution.Image = "other-image"
			case "management":
				management = bindingHash("other endpoint")
				expected = "changed_scope"
			case "lineage":
				lineage = bindingHash("other journal")
				expected = "changed_scope"
			case "controller":
				controller = "other-controller"
				expected = "changed_scope"
			}
			resolver, err := NewPoolRouteResolver(s, controller, management, lineage, templates)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolver.Resolve(ctx, source.facts.PoolID)
			if err != nil || got.ReferenceStatus != expected || !got.Record.Equal(original) || got.Record.Mode != "inactive" {
				t.Fatal("drift reinterpreted original route or granted activation", got, err)
			}
			if len(sqliteSnapshot(t, s).Reservations) != 0 {
				t.Fatal("reference drift admitted allocation")
			}
		})
	}
}
func TestPoolRouteResolverOwnsStartupReferences(t *testing.T) {
	s, r, source, _ := routeResolverFixture(t)
	ctx := context.Background()
	template := cloneRouteTemplate(r.templates["small-linux"])
	resolver, err := NewPoolRouteResolver(s, r.controller, r.managementIdentity, r.journalLineage, []PoolRouteTemplate{template})
	if err != nil {
		t.Fatal(err)
	}
	template.Hosts[0] = "forged"
	template.RepositoryIDs[0] = "forged"
	template.SourceFingerprints[0] = bindingHash("forged")
	template.Execution.Profiles[0] = "forged"
	*template.JITEnabled = false
	if _, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config)); err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Resolve(ctx, source.facts.PoolID)
	if err != nil || got.ReferenceStatus != "current" {
		t.Fatal("caller changed resolver startup refs", err)
	}
}
func TestPoolRouteResolverReopenArchiveAndMissingGraphFailClosed(t *testing.T) {
	s, r, source, _ := routeResolverFixture(t)
	ctx := context.Background()
	original, err := r.RegisterInactive(ctx, source.facts.RequestID, "small-linux", PolicyRevision(sqliteSnapshot(t, s).Config))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(sqliteBackupDir(t), "catalog.db")
	if err := s.BackupSQLite(ctx, archive); err != nil {
		t.Fatal(err)
	}
	copy, err := OpenSQLite(archive, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	resolver, err := NewPoolRouteResolver(copy, r.controller, r.managementIdentity, r.journalLineage, []PoolRouteTemplate{r.templates["small-linux"]})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Resolve(ctx, source.facts.PoolID)
	if err != nil || !got.Record.Equal(original) || got.Record.Mode != "inactive" || sqliteSnapshot(t, copy).Ready {
		t.Fatal("closed archive changed authority", err)
	}
	if _, err := copy.db.Exec("DROP TRIGGER pool_routes_no_delete;DELETE FROM pool_routes"); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.Resolve(ctx, source.facts.PoolID); !errors.Is(err, ErrPoolRoute) || !reflect.DeepEqual(got, PoolRouteResolution{}) {
		t.Fatal("lost history fell back to missing/static route", got, err)
	}
}
