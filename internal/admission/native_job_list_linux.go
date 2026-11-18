package admission

import (
	"context"
	"database/sql"
)

// NativeRunnerBindings pages immutable lineage, not live capacity inventory.
func (s *SQLiteStore) NativeRunnerBindings(ctx context.Context, after string, limit int) ([]NativeRunnerBinding, error) {
	if limit < 1 || limit > 64 {
		return nil, ErrNativeJob
	}
	out := []NativeRunnerBinding{}
	e := s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.Query("SELECT permit_id FROM native_runner_bindings WHERE permit_id>? ORDER BY permit_id LIMIT ?", after, limit)
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			v, e := nativeRunnerBinding(tx, id)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return out, e
}
