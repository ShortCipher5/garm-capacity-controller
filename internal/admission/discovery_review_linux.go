package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"
)

const sqliteDiscoveryReviewSchema = `
CREATE TABLE IF NOT EXISTS onboarding_reviews (
 id TEXT PRIMARY KEY NOT NULL, revision TEXT NOT NULL, profile TEXT NOT NULL,
 observed_at TEXT NOT NULL, snapshot BLOB NOT NULL, detected BLOB,
 current INTEGER NOT NULL CHECK(current IN (0,1)), consumed_by TEXT NOT NULL DEFAULT ''
) STRICT;
PRAGMA user_version=13;
`

// DiscoveryReview identifies an immutable, sanitized server observation, not a
// transport binding, physical authority, health check or activation permit.
type DiscoveryReview struct {
	ID        string    `json:"id"`
	Revision  string    `json:"revision"`
	Profile   string    `json:"profile_id"`
	ExpiresAt time.Time `json:"-"` // presentation only; enrollment rechecks current durable policy
}

// RecordDiscoveryReview retains the bounded view that the operator actually
// reviews. A new discovery supersedes earlier reviews for that profile, even if
// incomplete. Only trusted server discovery callers may supply this record.
func (s *SQLiteStore) RecordDiscoveryReview(ctx context.Context, id, profile string, observedAt time.Time, snapshot []byte, detected *Resources) (DiscoveryReview, error) {
	return s.RecordConnectionDiscoveryReview(ctx, id, profile, observedAt, snapshot, detected, 0)
}

// RecordConnectionDiscoveryReview rejects observations that completed after a
// connection revision/revocation. The check and review persistence are atomic.
func (s *SQLiteStore) RecordConnectionDiscoveryReview(ctx context.Context, id, profile string, observedAt time.Time, snapshot []byte, detected *Resources, connectionRevision int64) (DiscoveryReview, error) {
	review := DiscoveryReview{ID: id, Profile: profile}
	if !ValidOnboardingRequestID(id) || !ValidOnboardingProfileID(profile) || observedAt.IsZero() || len(snapshot) == 0 || len(snapshot) > 2<<20 || !json.Valid(snapshot) {
		return review, ErrDenied
	}
	if detected != nil && (!nonnegative(*detected) || !validStoredMagnitude(*detected)) {
		return review, ErrDenied
	}
	sum := sha256.Sum256(snapshot)
	review.Revision = hex.EncodeToString(sum[:])
	var totals []byte
	if detected != nil {
		totals, _ = json.Marshal(detected)
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var connection Connection
		connectionErr := readConnection(tx, profile, &connection)
		if connectionRevision > 0 {
			if connectionErr != nil || connection.State != "approved" || connection.Revision != connectionRevision {
				return ErrConflict
			}
		} else if connectionErr == nil {
			return ErrConflict
		} else if connectionErr != ErrDenied {
			return connectionErr
		}
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		age := s.now().Sub(observedAt)
		if age < 0 || age > config.MaxObservationAge {
			return ErrDenied
		}
		review.ExpiresAt = observedAt.Add(config.MaxObservationAge).UTC()
		var latest string
		latestErr := tx.QueryRow("SELECT observed_at FROM onboarding_reviews WHERE profile=? AND current=1", profile).Scan(&latest)
		if latestErr != nil && latestErr != sql.ErrNoRows {
			return latestErr
		}
		if latestErr == nil {
			previous, parseErr := time.Parse(time.RFC3339Nano, latest)
			if parseErr != nil {
				return parseErr
			}
			if !observedAt.After(previous) {
				return ErrConflict
			}
		}
		if _, err = tx.Exec("UPDATE onboarding_reviews SET current=0 WHERE profile=?", profile); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO onboarding_reviews(id,revision,profile,observed_at,snapshot,detected,current) VALUES(?,?,?,?,?,?,1)", id, review.Revision, profile, observedAt.UTC().Format(time.RFC3339Nano), snapshot, totals); err != nil {
			return err
		}
		return sqliteAudit(tx, "onboarding_review_recorded", id, s.now())
	})
	return review, err
}

// EnrollHostFromReview consumes the exact current, fresh review and inserts a
// paused, unhealthy host in one transaction. No browser totals or observation
// time are trusted. Consumption survives restarts; replay cannot duplicate
// capacity under another name. Operator budgets remain explicit configuration.
func (s *SQLiteStore) EnrollHostFromReview(ctx context.Context, hostID string, budget Resources, mode string, review DiscoveryReview) error {
	if !validHostID(hostID) || !nonnegative(budget) || !validStoredMagnitude(budget) || (mode != "disabled" && mode != "draining") || !ValidOnboardingRequestID(review.ID) || !ValidOnboardingProfileID(review.Profile) || len(review.Revision) != 64 {
		return ErrDenied
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var revision, profile, observed, consumed string
		var detected []byte
		var current int
		err := tx.QueryRow("SELECT revision,profile,observed_at,detected,current,consumed_by FROM onboarding_reviews WHERE id=?", review.ID).Scan(&revision, &profile, &observed, &detected, &current, &consumed)
		if err == sql.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if revision != review.Revision || profile != review.Profile || current != 1 || consumed != "" {
			return ErrConflict
		}
		when, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			return err
		}
		config, _, _, _, err := sqliteMetadata(tx)
		if err != nil {
			return err
		}
		age := s.now().Sub(when)
		if age < 0 || age > config.MaxObservationAge {
			return ErrConflict
		}
		var totals *Resources
		if len(detected) > 0 {
			totals = new(Resources)
			if err = json.Unmarshal(detected, totals); err != nil {
				return err
			}
		}
		if err = s.enrollHostTx(tx, hostID, budget, mode, totals, when); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE onboarding_reviews SET consumed_by=? WHERE id=?", hostID, review.ID); err != nil {
			return err
		}
		binding, _ := json.Marshal(struct {
			Review DiscoveryReview `json:"review"`
			Host   string          `json:"host"`
			Budget Resources       `json:"budget"`
		}{review, hostID, budget})
		return sqliteAudit(tx, "onboarding_review_consumed", string(binding), s.now())
	})
}
