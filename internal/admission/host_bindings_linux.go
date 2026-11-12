package admission

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrHostBinding = errors.New("verified unique backend binding required")
var ErrLegacyHostBinding = errors.New("legacy charged host requires qualified scope import")

const sqliteHostBindingSchema = `
ALTER TABLE dispatches ADD COLUMN host_binding_sequence INTEGER REFERENCES audit(sequence);
CREATE TRIGGER dispatch_host_binding_is_immutable BEFORE UPDATE OF host_binding_sequence ON dispatches
WHEN OLD.host_binding_sequence IS NOT NEW.host_binding_sequence
BEGIN SELECT RAISE(ABORT,'dispatch host binding is immutable'); END;

CREATE TABLE host_bindings (
 host TEXT PRIMARY KEY NOT NULL,
 server_key TEXT UNIQUE NOT NULL CHECK(length(server_key)=64),
 origin TEXT UNIQUE NOT NULL CHECK(length(origin)=64),
 scope TEXT UNIQUE NOT NULL CHECK(length(scope)=64),
 server_name TEXT UNIQUE NOT NULL COLLATE NOCASE,
 server_version TEXT NOT NULL,
 bound_sequence INTEGER UNIQUE NOT NULL REFERENCES audit(sequence),
 bound_at TEXT NOT NULL
) STRICT;
CREATE TABLE host_binding_history_exclusions (
 host TEXT PRIMARY KEY NOT NULL
) STRICT;
CREATE TRIGGER host_bindings_immutable_update BEFORE UPDATE ON host_bindings
BEGIN SELECT RAISE(ABORT,'host bindings are immutable'); END;
CREATE TRIGGER host_bindings_immutable_delete BEFORE DELETE ON host_bindings
BEGIN SELECT RAISE(ABORT,'host bindings are immutable'); END;
CREATE TRIGGER host_binding_history_immutable_update BEFORE UPDATE ON host_binding_history_exclusions
BEGIN SELECT RAISE(ABORT,'host binding history exclusions are immutable'); END;
CREATE TRIGGER host_binding_history_immutable_delete BEFORE DELETE ON host_binding_history_exclusions
BEGIN SELECT RAISE(ABORT,'host binding history exclusions are immutable'); END;
PRAGMA user_version=4;
`

// HostBinding is a trusted gateway's authenticated daemon observation. It is NOT
// physical-hardware attestation, whole-host inventory or a provisioning permit.
// Origins intentionally exclude project; scopes bind origin AND exact project.
// Never accept this structure as caller/operator JSON evidence over an API.
type HostBinding struct {
	Host                 string
	ServerKeyFingerprint string
	OriginFingerprint    string
	ScopeFingerprint     string
	ServerName           string
	ServerVersion        string
	ObservedAt           time.Time
	Sequence             int64
}

func bindingName(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func bindingFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return false
	}
	return hex.EncodeToString(decoded) == value
}
func validHostBinding(value HostBinding) bool {
	return bindingName(value.Host) && bindingName(value.ServerName) && bindingName(value.ServerVersion) && bindingFingerprint(value.ServerKeyFingerprint) && bindingFingerprint(value.OriginFingerprint) && bindingFingerprint(value.ScopeFingerprint)
}

// Matches compares immutable binding identity, not the fresh observation time.
func (a HostBinding) Matches(b HostBinding) bool {
	return a.Host == b.Host && a.ServerKeyFingerprint == b.ServerKeyFingerprint && a.OriginFingerprint == b.OriginFingerprint && a.ScopeFingerprint == b.ScopeFingerprint && a.ServerName == b.ServerName && a.ServerVersion == b.ServerVersion
}
func scanHostBinding(scan func(...any) error) (HostBinding, error) {
	var value HostBinding
	var observed string
	err := scan(&value.Host, &value.ServerKeyFingerprint, &value.OriginFingerprint, &value.ScopeFingerprint, &value.ServerName, &value.ServerVersion, &value.Sequence, &observed)
	if err != nil {
		return HostBinding{}, err
	}
	value.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil || value.ObservedAt.IsZero() || value.Sequence <= 0 || !validHostBinding(value) {
		return HostBinding{}, ErrHostBinding
	}
	return value, nil
}
func sqliteHostBindings(tx *sql.Tx, config Config) (map[string]HostBinding, error) {
	rows, err := tx.Query("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]HostBinding{}
	for rows.Next() {
		binding, err := scanHostBinding(rows.Scan)
		if err != nil {
			return nil, err
		}
		if _, ok := config.Hosts[binding.Host]; !ok {
			return nil, ErrHostBinding
		}
		result[binding.Host] = binding
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// BindHostIdentities atomically checks the COMPLETE configured alias set under a
// live reconciliation barrier. Aliases cannot share an authenticated key, origin
// or reported name, even with different projects. Existing bindings are immutable
// across restarts/processes/backups; changes require a separate qualified import.
// Initial binding is refused for ANY charged/unqualified historical host. It does
// not release allocation, establish external writer fencing or open admission.
func (s *SQLiteStore) BindHostIdentities(ctx context.Context, fence Fence, bindings []HostBinding) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		config, ready, epoch, nonce, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		if ready || fence.Epoch <= 0 || fence.Nonce == "" || epoch != fence.Epoch || nonce != fence.Nonce {
			return ErrFence
		}
		if len(bindings) != len(config.Hosts) {
			return ErrHostBinding
		}
		var beganText string
		if err := tx.QueryRow("SELECT began_at FROM metadata WHERE id=1").Scan(&beganText); err != nil {
			return err
		}
		began, err := time.Parse(time.RFC3339Nano, beganText)
		if err != nil {
			return ErrFence
		}
		existing, err := sqliteHostBindings(tx, config)
		if err != nil {
			return err
		}
		hosts, keys, origins, scopes, names := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		now := s.now()
		for _, binding := range bindings {
			if !validHostBinding(binding) || hosts[binding.Host] || keys[binding.ServerKeyFingerprint] || origins[binding.OriginFingerprint] || scopes[binding.ScopeFingerprint] || names[strings.ToLower(binding.ServerName)] {
				return ErrHostBinding
			}
			if _, ok := config.Hosts[binding.Host]; !ok {
				return ErrHostBinding
			}
			if binding.ObservedAt.IsZero() || binding.ObservedAt.Before(began) || binding.ObservedAt.After(now) || now.Sub(binding.ObservedAt) > config.MaxObservationAge {
				return ErrHostBinding
			}
			hosts[binding.Host] = true
			keys[binding.ServerKeyFingerprint] = true
			origins[binding.OriginFingerprint] = true
			scopes[binding.ScopeFingerprint] = true
			names[strings.ToLower(binding.ServerName)] = true
			if old, ok := existing[binding.Host]; ok {
				if !old.Matches(binding) {
					return ErrHostBinding
				}
				continue
			}
			var unqualified bool
			if err := tx.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM host_binding_history_exclusions WHERE host=?
 UNION ALL SELECT 1 FROM reservations WHERE host=?
 UNION ALL SELECT 1 FROM inventory WHERE host=? LIMIT 1)`, binding.Host, binding.Host, binding.Host).Scan(&unqualified); err != nil {
				return err
			}
			if unqualified {
				return ErrLegacyHostBinding
			}
		}
		for _, binding := range bindings {
			if _, exists := existing[binding.Host]; exists {
				continue
			}
			if err := sqliteAudit(tx, "host_identity_bound", binding.Host, now); err != nil {
				return err
			}
			var sequence int64
			if err := tx.QueryRow("SELECT last_insert_rowid()").Scan(&sequence); err != nil || sequence <= 0 {
				if err != nil {
					return err
				}
				return ErrHostBinding
			}
			if _, err := tx.Exec("INSERT INTO host_bindings(host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at) VALUES(?,?,?,?,?,?,?,?)", binding.Host, binding.ServerKeyFingerprint, binding.OriginFingerprint, binding.ScopeFingerprint, binding.ServerName, binding.ServerVersion, sequence, binding.ObservedAt.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SQLiteStore) LookupHostBinding(ctx context.Context, host string) (HostBinding, error) {
	var result HostBinding
	if !bindingName(host) {
		return result, ErrHostBinding
	}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = scanHostBinding(tx.QueryRow("SELECT host,server_key,origin,scope,server_name,server_version,bound_sequence,bound_at FROM host_bindings WHERE host=?", host).Scan)
		return err
	})
	return result, err
}

func sqliteLegacyUnboundHosts(tx *sql.Tx, config Config) ([]string, error) {
	rows, err := tx.Query("SELECT host FROM host_binding_history_exclusions ORDER BY host")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, err
		}
		if _, ok := config.Hosts[host]; !ok {
			return nil, ErrHostBinding
		}
		result = append(result, host)
	}
	return result, rows.Err()
}
