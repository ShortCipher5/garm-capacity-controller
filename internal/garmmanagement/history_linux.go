package garmmanagement

import (
	"context"
	"strings"
)

// RecentIntents is a bounded operator view of sanitized settings changes. It is
// read-only, never a recovery/unblock mechanism. Includes confirmed, rejected,
// unknown and pending intents, newest first, without remote service traffic.
func (m *Manager) RecentIntents(ctx context.Context, limit int) ([]Intent, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrJournal
	}
	return m.recentIntents(ctx, "ORDER BY sequence DESC LIMIT ?", []any{limit})
}

// RecentScopedIntents prevents a narrower current console scope from exposing
// historical settings/repository names for targets removed from that scope.
// A global unresolved slot still blocks writes even if its target was removed.
func (m *Manager) RecentScopedIntents(ctx context.Context, limit int, pools, repositories []string) ([]Intent, error) {
	if limit < 1 || limit > 100 || len(pools)+len(repositories) > 1000 {
		return nil, ErrJournal
	}
	clauses := []string{"0"}
	args := []any{}
	for _, scope := range []struct {
		kind string
		ids  []string
	}{{"pool", pools}, {"repository", repositories}} {
		if len(scope.ids) == 0 {
			continue
		}
		slots := []string{}
		for _, id := range scope.ids {
			if !validUUID(id) {
				return nil, ErrJournal
			}
			slots = append(slots, "?")
			args = append(args, id)
		}
		clauses = append(clauses, "(kind='"+scope.kind+"' AND target IN ("+strings.Join(slots, ",")+"))")
	}
	args = append(args, limit)
	return m.recentIntents(ctx, "WHERE ("+strings.Join(clauses, " OR ")+") ORDER BY sequence DESC LIMIT ?", args)
}
func (m *Manager) recentIntents(ctx context.Context, suffix string, args []any) ([]Intent, error) {
	rows, err := m.journal.db.QueryContext(ctx, "SELECT sequence,request_id,kind,target,state,reason,created_at,updated_at,before_json,desired_json,result_json FROM intents "+suffix, args...)
	if err != nil {
		return nil, ErrJournal
	}
	defer rows.Close()
	result := []Intent{}
	for rows.Next() {
		var item Intent
		var before, desired, recorded []byte
		if rows.Scan(&item.Sequence, &item.RequestID, &item.Kind, &item.Target, &item.State, &item.Reason, &item.CreatedAt, &item.UpdatedAt, &before, &desired, &recorded) != nil {
			return nil, ErrJournal
		}
		item.Before = before
		item.Desired = desired
		item.Result = recorded
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, ErrJournal
	}
	return result, nil
}
