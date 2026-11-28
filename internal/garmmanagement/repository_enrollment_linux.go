// Repository enrollment shares the management journal and its single unresolved
// write slot with pool staging. No stock writer implements this capability.
package garmmanagement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

const repoEnrollmentOp = "enroll_github_repository_v1"
const repoEnrollmentReason = "enroll reviewed GitHub repository"

// RepositoryLinkPlan contains deployment-owned identifiers, never credentials,
// URLs or a browser-provided policy. ForgeIdentity is the canonical forge's
// SHA-256 identity. RecipeRevision binds the selected credential reference,
// webhook policy and account/installation; BudgetRevision binds the fixed pool
// template and shared-budget target. Human-readable names are not identities.
type RepositoryLinkPlan struct {
	ForgeIdentity      string `json:"forge_identity"`
	GitHubRepositoryID int64  `json:"github_repository_id"`
	RecipeRevision     string `json:"recipe_revision"`
	BudgetRevision     string `json:"budget_revision"`
}

type repositoryLinkIntent struct {
	Operation string             `json:"operation"`
	Plan      RepositoryLinkPlan `json:"plan"`
}

// RepositoryEnrollment is historical receipt evidence, not a budget attachment,
// active pool, or permission to create runners.
type RepositoryEnrollment struct {
	RequestID          string             `json:"request_id"`
	ManagementIdentity string             `json:"management_identity"`
	JournalLineage     string             `json:"journal_lineage"`
	Plan               RepositoryLinkPlan `json:"plan"`
	RepositoryID       string             `json:"garm_repository_id"`
	Confirmed          bool               `json:"confirmed"`
}

// Implementations must enforce exclusive enrollment and verify the live forge
// identity, installation authorization, fixed credential/webhook policy and
// budget revision immediately before dispatch. Confirm reads the exact GARM
// UUID and verifies its binding to the immutable forge repository, not a name.
// A provider unable to establish this contract must not opt in.
type repositoryEnrollmentRemote interface {
	RepositoryEnrollmentEnabled() bool
	ReviewRepositoryLink(context.Context, RepositoryLinkPlan) error
	PostReviewedRepository(context.Context, string, RepositoryLinkPlan) (string, error)
	ReadSettledRepository(context.Context, string, string, RepositoryLinkPlan) (RepositoryEnrollmentProof, error)
}

// The adapter builds this only from verified upstream evidence, not by
// echoing its arguments. A generic repository GET cannot establish settlement.
type RepositoryEnrollmentProof struct {
	RequestID          string
	ManagementIdentity string
	RepositoryID       string
	Plan               RepositoryLinkPlan
	OperationSettled   bool
}

func verifiedRepositoryLink(ctx context.Context, remote repositoryEnrollmentRemote, r RepositoryEnrollment) bool {
	proof, err := remote.ReadSettledRepository(ctx, r.RequestID, r.RepositoryID, r.Plan)
	return err == nil && proof == (RepositoryEnrollmentProof{r.RequestID, r.ManagementIdentity, r.RepositoryID, r.Plan, true})
}

func (p RepositoryLinkPlan) valid() bool {
	return validJournalLineage(p.ForgeIdentity) && p.GitHubRepositoryID > 0 && validJournalLineage(p.RecipeRevision) && validJournalLineage(p.BudgetRevision)
}

// The journal operation key is stable across retries, operator sessions, and
// changed recipe revisions. A changed plan conflicts rather than re-POSTing.
// It is NOT a GARM UUID and must never be queried against GARM.
func (p RepositoryLinkPlan) operationKey(management string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", repoEnrollmentOp, management, p.ForgeIdentity, p.GitHubRepositoryID)))
	sum[6] = (sum[6] & 0x0f) | 0x80
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
func (m *Manager) linkIntent(p RepositoryLinkPlan) (RepositoryEnrollment, repositoryLinkIntent, string, error) {
	identity := m.remote.ManagementIdentity()
	if !p.valid() || !validJournalLineage(identity) {
		return RepositoryEnrollment{}, repositoryLinkIntent{}, "", garmapi.ErrManagementUnsafe
	}
	key := p.operationKey(identity)
	desired := repositoryLinkIntent{repoEnrollmentOp, p}
	hash, e := requestHash(key, key, p.RecipeRevision, "repository", repoEnrollmentReason, desired)
	return RepositoryEnrollment{RequestID: key, ManagementIdentity: identity, JournalLineage: m.journal.Lineage(), Plan: p}, desired, hash, e
}

func decodeEnrollment(raw []byte, want RepositoryEnrollment, confirmed bool) (RepositoryEnrollment, error) {
	var v RepositoryEnrollment
	if json.Unmarshal(raw, &v) != nil || !bytes.Equal(encoded(v), raw) || !validUUID(v.RepositoryID) || v.RepositoryID == "00000000-0000-0000-0000-000000000000" {
		return RepositoryEnrollment{}, ErrJournal
	}
	want.RepositoryID = v.RepositoryID
	want.Confirmed = confirmed
	if v != want {
		return RepositoryEnrollment{}, ErrJournal
	}
	return v, nil
}

// EnrollReviewedRepository performs at most one dispatch for a reviewed forge
// repository in this journal. Unknown outcomes never cause an automatic retry.
func (m *Manager) EnrollReviewedRepository(ctx context.Context, p RepositoryLinkPlan) (RepositoryEnrollment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e := m.enter(ctx); e != nil {
		return RepositoryEnrollment{}, e
	}
	defer m.leave()
	remote, ok := m.remote.(repositoryEnrollmentRemote)
	if !ok || !remote.RepositoryEnrollmentEnabled() {
		return RepositoryEnrollment{}, garmapi.ErrManagementDisabled
	}
	result, desired, hash, e := m.linkIntent(p)
	if e != nil {
		return RepositoryEnrollment{}, e
	}
	key := result.RequestID
	if raw, found, e := m.journal.replay(ctx, key, hash); e != nil {
		return RepositoryEnrollment{}, e
	} else if found {
		return decodeEnrollment(raw, result, true)
	}
	if e := remote.ReviewRepositoryLink(ctx, p); e != nil {
		return RepositoryEnrollment{}, garmapi.ErrManagementConflict
	}
	intent := Intent{RequestID: key, Kind: "repository", Target: key, Reason: repoEnrollmentReason, Before: encoded(struct{}{}), Desired: encoded(desired)}
	if raw, e := m.journal.begin(ctx, intent, hash, p.RecipeRevision); e != nil {
		return RepositoryEnrollment{}, e
	} else if raw != nil {
		return decodeEnrollment(raw, result, true)
	}
	id, postErr := remote.PostReviewedRepository(ctx, key, p)
	valid := validUUID(id) && id != "00000000-0000-0000-0000-000000000000"
	if valid {
		result.RepositoryID = id
		persist, done := context.WithTimeout(context.Background(), 5*time.Second)
		e = m.journal.transaction(persist, func(tx *sql.Tx) error {
			u, e := tx.Exec("UPDATE intents SET result_json=?,updated_at=? WHERE request_id=? AND request_hash=? AND state='pending' AND result_json IS NULL", encoded(result), timestamp(), key, hash)
			if e != nil {
				return ErrJournal
			}
			n, e := u.RowsAffected()
			if e != nil || n != 1 {
				return ErrJournal
			}
			return audit(tx, key, "repository_uuid_received", encoded(result))
		})
		done()
		if e != nil {
			return RepositoryEnrollment{}, ErrOutcomeUnknown
		}
	}
	if postErr != nil || !valid || !verifiedRepositoryLink(ctx, remote, result) {
		persist, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = m.journal.finish(persist, key, "unknown", encoded(result))
		return RepositoryEnrollment{}, ErrOutcomeUnknown
	}
	result.Confirmed = true
	persist, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if e := m.journal.finish(persist, key, "confirmed", encoded(result)); e != nil {
		return RepositoryEnrollment{}, ErrOutcomeUnknown
	}
	return result, nil
}

// RecoverReviewedRepository uses only a captured exact UUID; no write to GARM,
// search by slug or substitution of a newly listed repository is allowed.
// A missing ACK deliberately remains blocked; independent upstream settlement
// evidence would be needed. A crash after the ACK is recoverable only when
// authoritative readback proves that exact operation is already settled.
func (m *Manager) RecoverReviewedRepository(ctx context.Context, p RepositoryLinkPlan) (RepositoryEnrollment, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e := m.enter(ctx); e != nil {
		return RepositoryEnrollment{}, e
	}
	defer m.leave()
	r, ok := m.remote.(repositoryEnrollmentRemote)
	if !ok || !r.RepositoryEnrollmentEnabled() {
		return RepositoryEnrollment{}, garmapi.ErrManagementDisabled
	}
	want, desired, hash, e := m.linkIntent(p)
	if e != nil {
		return RepositoryEnrollment{}, e
	}
	var h, state string
	var raw, written []byte
	if e = m.journal.db.QueryRowContext(ctx, "SELECT request_hash,state,desired_json,result_json FROM intents WHERE request_id=?", want.RequestID).Scan(&h, &state, &written, &raw); e != nil {
		return RepositoryEnrollment{}, ErrJournal
	}
	if h != hash || !bytes.Equal(written, encoded(desired)) {
		return RepositoryEnrollment{}, ErrRequestConflict
	}
	if state == "confirmed" {
		return decodeEnrollment(raw, want, true)
	}
	if state != "unknown" && state != "pending" {
		return RepositoryEnrollment{}, ErrBlocked
	}
	got, e := decodeEnrollment(raw, want, false)
	if e != nil {
		return RepositoryEnrollment{}, ErrBlocked
	}
	if !verifiedRepositoryLink(ctx, r, got) {
		return RepositoryEnrollment{}, ErrOutcomeUnknown
	}
	got.Confirmed = true
	if e = m.journal.finish(ctx, got.RequestID, "confirmed", encoded(got)); e != nil {
		if raw, found, e := m.journal.replay(ctx, got.RequestID, hash); e == nil && found {
			return decodeEnrollment(raw, want, true)
		}
		return RepositoryEnrollment{}, ErrOutcomeUnknown
	}
	return got, nil
}
