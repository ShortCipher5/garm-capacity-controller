package admission

import (
	"context"
	"database/sql"
)

// NativeDomainEnrollments returns canonical enrollment records, including domains
// not yet referenced by a pool profile. It grants no execution authority.
func (s *SQLiteStore) NativeDomainEnrollments(ctx context.Context) ([]NativeDomainBinding, error) {
	out, _, err := s.NativeDomainEnrollmentView(ctx)
	return out, err
}

// NativeDomainEnrollmentView binds the review sequence and rows to one read snapshot.
func (s *SQLiteStore) NativeDomainEnrollmentView(ctx context.Context) ([]NativeDomainBinding, int64, error) {
	out := []NativeDomainBinding{}
	var sequence int64
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.Query("SELECT record_sequence FROM native_domain_bindings ORDER BY record_sequence LIMIT 1001")
		if e != nil {
			return e
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
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
		if len(ids) > 1000 {
			return ErrNativeExecution
		}
		for _, id := range ids {
			v, e := nativeDomainRecord(tx, id)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return tx.QueryRow("SELECT COALESCE(max(sequence),0) FROM audit").Scan(&sequence)
	})
	if err != nil {
		return nil, 0, err
	}
	return out, sequence, nil
}
