package admission

import (
	"database/sql"
	"encoding/json"

	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// sqliteHistoricReleaseKeys returns released keys whose tombstone record is
// NOT a valid verified-reuse TerminalEvidence, i.e. exclusions written by a
// pre-verification release path. Only a tombstone whose record decodes to
// evidence that still validates counts as verified; everything else stays
// distinct as historic, never silently reinterpreted.
func sqliteHistoricReleaseKeys(tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.Query("SELECT request_key,record FROM releases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var key string
		var record []byte
		if err := rows.Scan(&key, &record); err != nil {
			return nil, err
		}
		var evidence reuse.TerminalEvidence
		if len(record) == 0 || json.Unmarshal(record, &evidence) != nil || !evidence.Valid() {
			result[key] = true
		}
	}
	return result, rows.Err()
}
