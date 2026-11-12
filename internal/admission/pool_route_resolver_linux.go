package admission

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
)

// poolRouteCatalogRecords reads the complete registration graph in one database
// snapshot. Used by archives and the future runtime resolver: an absent row must
// never turn a previously registered dynamic pool into an ordinary static pool.
func poolRouteCatalogRecords(tx *sql.Tx) (Config, map[string]PoolRouteRecord, error) {
	if err := checkPoolRouteStateAuditAnchors(tx); err != nil {
		return Config{}, nil, err
	}
	var missing int
	if err := tx.QueryRow(`SELECT count(*) FROM audit a LEFT JOIN pool_routes p ON p.identity_sequence=a.sequence
WHERE a.kind='pool_route_registered_inactive' AND (p.identity_sequence IS NULL OR p.pool_id!=a.request_key)`).Scan(&missing); err != nil {
		return Config{}, nil, err
	}
	if missing != 0 {
		return Config{}, nil, ErrPoolRoute
	}
	rows, err := tx.Query("SELECT pool_id FROM pool_routes ORDER BY pool_id")
	if err != nil {
		return Config{}, nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Config{}, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Config{}, nil, err
	}
	config, _, _, _, err := sqliteMetadata(tx)
	if err != nil {
		return Config{}, nil, err
	}
	records := make(map[string]PoolRouteRecord, len(ids))
	for _, id := range ids {
		record, err := poolRouteRecord(tx, id)
		if err != nil {
			return Config{}, nil, err
		}
		// Original budget is historical. Current authorized budget edits do not
		// rewrite identity; accounting/admission independently enforce current policy.
		pool, ok := config.Pools[id]
		if !ok {
			return Config{}, nil, ErrPoolRoute
		}
		hosts := append([]string(nil), pool.Hosts...)
		sort.Strings(hosts)
		if !reflect.DeepEqual(hosts, record.Identity.Template.Hosts) {
			return Config{}, nil, ErrPoolRoute
		}
		for host, seq := range record.Identity.HostBindingSequences {
			binding, err := scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", host).Scan)
			if err != nil || binding.Sequence != seq {
				return Config{}, nil, ErrHostBinding
			}
		}
		if _, err := poolRouteLatestState(tx, record); err != nil {
			return Config{}, nil, err
		}
		records[id] = record
	}
	return config, records, nil
}

// PoolRouteResolution is a READ observation, not a dispatch/activation token.
// Record always preserves original registration. CurrentPolicy and Revision are
// from the same SQLite snapshot. ReferenceStatus only explains whether private
// startup scope/template settings still match; it is never activation permission.
type PoolRouteResolution struct {
	State           PoolRouteState // historical read only; never a permit
	Record          PoolRouteRecord
	CurrentPolicy   Pool
	PolicyRevision  string
	ReferenceStatus string // current, missing_template, changed_template, changed_scope
}

// PoolRouteResolver owns immutable private template references. It performs fresh
// database reads per request, not edits or caching of Facade maps. It does not
// contact Garm or confer source actor grants, admission or activation authority.
type PoolRouteResolver struct {
	store                                          *SQLiteStore
	controller, managementIdentity, journalLineage string
	templates                                      map[string]PoolRouteTemplate
}

func NewPoolRouteResolver(store *SQLiteStore, controller, managementIdentity, journalLineage string, templates []PoolRouteTemplate) (*PoolRouteResolver, error) {
	if store == nil || !bindingName(controller) || !bindingFingerprint(managementIdentity) || !bindingFingerprint(journalLineage) || len(templates) > 128 {
		return nil, ErrPoolRoute
	}
	r := &PoolRouteResolver{store: store, controller: controller, managementIdentity: managementIdentity, journalLineage: journalLineage, templates: map[string]PoolRouteTemplate{}}
	for _, template := range templates {
		t, err := normalizeRouteTemplate(template, controller)
		if err != nil || r.templates[t.Reference].Reference != "" {
			return nil, ErrPoolRoute
		}
		r.templates[t.Reference] = t
	}
	return r, nil
}
func (r *PoolRouteResolver) Resolve(ctx context.Context, poolID string) (PoolRouteResolution, error) {
	var result PoolRouteResolution
	if !routeUUID(poolID) {
		return result, ErrPoolRoute
	}
	err := r.store.readTransaction(ctx, func(tx *sql.Tx) error {
		config, records, err := poolRouteCatalogRecords(tx)
		if err != nil {
			return err
		}
		record, ok := records[poolID]
		if !ok {
			return sql.ErrNoRows
		}
		status := "current"
		identity := record.Identity
		template, ok := r.templates[identity.Template.Reference]
		switch {
		case identity.Controller != r.controller || identity.ManagementIdentity != r.managementIdentity || identity.JournalLineage != r.journalLineage:
			status = "changed_scope"
		case !ok:
			status = "missing_template"
		case routeTemplateDigest(template) != identity.TemplateDigest:
			status = "changed_template"
		}
		state, err := poolRouteLatestState(tx, record)
		if err != nil {
			return err
		}
		result = PoolRouteResolution{State: state, Record: record, CurrentPolicy: config.Pools[poolID], PolicyRevision: PolicyRevision(config), ReferenceStatus: status}
		return nil
	})
	if err != nil {
		return PoolRouteResolution{}, err
	}
	return result, nil
}
