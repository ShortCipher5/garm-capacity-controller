package admission

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const sqliteConnectionsSchema = `
CREATE TABLE IF NOT EXISTS onboarding_connections (
 id TEXT PRIMARY KEY NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
 state TEXT NOT NULL CHECK(state IN ('draft','approved','revoked')), document BLOB NOT NULL
) STRICT;
PRAGMA user_version=14;
`

// Connection contains nonsecret intent only. CredentialRef is a deployment
// alias, never a file path or credential. Approval is operator trust review,
// not a successful connection, host health or admission authorization.
type Connection struct {
	ID              string `json:"id"`
	Revision        int64  `json:"revision"`
	State           string `json:"state"`
	CredentialRef   string `json:"credential_ref"`
	Endpoint        string `json:"endpoint"`
	Project         string `json:"project"`
	ServerKeySHA256 string `json:"server_key_sha256"`
}

// ValidConnectionEndpoint deliberately allows only canonical literal-IP HTTPS
// authorities. Dynamic DNS, zones, userinfo, paths, redirects and proxies are
// unsupported until their own destination policy exists. Deployment policy
// must additionally authorize the exact endpoint for the selected credential.
func ValidConnectionEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}
	return endpoint == "https://"+host+":"+strconv.Itoa(port)
}
func ValidConnectionProject(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && i > 0)) {
			return false
		}
	}
	return true
}
func validConnection(c Connection) bool {
	if !ValidOnboardingProfileID(c.ID) || !ValidOnboardingProfileID(c.CredentialRef) || !ValidConnectionEndpoint(c.Endpoint) || !ValidConnectionProject(c.Project) || len(c.ServerKeySHA256) != 64 {
		return false
	}
	for _, r := range c.ServerKeySHA256 {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (s *SQLiteStore) Connection(ctx context.Context, id string) (Connection, error) {
	var c Connection
	if !ValidOnboardingProfileID(id) {
		return c, ErrDenied
	}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error { return readConnection(tx, id, &c) })
	return c, err
}
func readConnection(tx *sql.Tx, id string, c *Connection) error {
	var document []byte
	err := tx.QueryRow("SELECT document FROM onboarding_connections WHERE id=?", id).Scan(&document)
	if err == sql.ErrNoRows {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(document, c)
}
func (s *SQLiteStore) Connections(ctx context.Context) ([]Connection, error) {
	result := []Connection{}
	err := s.readTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT document FROM onboarding_connections ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var document []byte
			var c Connection
			if err = rows.Scan(&document); err != nil {
				return err
			}
			if err = json.Unmarshal(document, &c); err != nil {
				return err
			}
			result = append(result, c)
		}
		return rows.Err()
	})
	return result, err
}

// PutConnection creates/revises a draft with optimistic concurrency. Revising
// approved intent never retains approval; every edit invalidates old reviews.
func (s *SQLiteStore) PutConnection(ctx context.Context, c Connection, expected int64) (Connection, error) {
	if !validConnection(c) || expected < 0 || expected >= 9007199254740991 {
		return Connection{}, ErrDenied
	}
	c.Revision = expected + 1
	c.State = "draft"
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var revision int64
		err := tx.QueryRow("SELECT revision FROM onboarding_connections WHERE id=?", c.ID).Scan(&revision)
		if err == sql.ErrNoRows {
			if expected != 0 {
				return ErrConflict
			}
			var count int
			if err = tx.QueryRow("SELECT COUNT(*) FROM onboarding_connections").Scan(&count); err != nil {
				return err
			}
			if count >= 32 {
				return ErrDenied
			}
		} else if err != nil {
			return err
		} else if revision != expected {
			return ErrConflict
		}
		return s.writeConnection(tx, c)
	})
	return c, err
}
func (s *SQLiteStore) TransitionConnection(ctx context.Context, id string, expected int64, state, approvedKey string) (Connection, error) {
	var c Connection
	if !ValidOnboardingProfileID(id) || expected < 1 || expected >= 9007199254740991 || (state != "approved" && state != "revoked") {
		return c, ErrDenied
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := readConnection(tx, id, &c); err != nil {
			return err
		}
		if c.Revision != expected {
			return ErrConflict
		}
		if state == "approved" && (c.State != "draft" || approvedKey != c.ServerKeySHA256) {
			return ErrDenied
		}
		c.Revision++
		c.State = state
		return s.writeConnection(tx, c)
	})
	return c, err
}
func (s *SQLiteStore) writeConnection(tx *sql.Tx, c Connection) error {
	encoded, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO onboarding_connections(id,revision,state,document) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,state=excluded.state,document=excluded.document", c.ID, c.Revision, c.State, encoded); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE onboarding_reviews SET current=0 WHERE profile=?", c.ID); err != nil {
		return err
	}
	event, _ := json.Marshal(struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		State    string `json:"state"`
	}{c.ID, c.Revision, c.State})
	return sqliteAudit(tx, "onboarding_connection_changed", string(event), s.now())
}

// InvalidateConnectionReviews starts a new runtime policy epoch. Approved
// intents persist, but reviews from an earlier deployment target/credential
// policy cannot authorize even paused enrollment without fresh discovery.
func (s *SQLiteStore) InvalidateConnectionReviews(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE onboarding_reviews SET current=0 WHERE profile IN (SELECT id FROM onboarding_connections)"); err != nil {
			return err
		}
		return sqliteAudit(tx, "onboarding_connection_runtime_started", "", s.now())
	})
}
