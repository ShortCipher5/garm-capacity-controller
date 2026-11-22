package demandrepair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

type CIArtifactAvailability string

const (
	CIArtifactAvailabilityUnknown CIArtifactAvailability = "availability_unknown"
	CIArtifactListedUnexpired     CIArtifactAvailability = "listed_unexpired"
	CIArtifactExpired             CIArtifactAvailability = "expired"
)

// CIArtifactMetadata is deliberately scalar and run-scoped. ListedUnexpired
// is a metadata observation, not proof that an archive can be downloaded.
// Zero times and SizeKnown=false mean absent evidence, not observed zero.
// AdvertisedSHA256 is only the digest advertised by GitHub's metadata.
// Unpinned rows (AdvertisedSHA256Known=false) cannot supply an archive pin.
// A pin is not proof of downloaded bytes, a producing job, or an attempt.
type CIArtifactMetadata struct {
	ID                    int64
	SizeInBytes           int64
	SizeKnown             bool
	CreatedAt, ExpiresAt  time.Time
	Availability          CIArtifactAvailability
	AdvertisedSHA256      [sha256.Size]byte
	AdvertisedSHA256Known bool
}

// CIArtifactPage refers only to one enrolled repository and numeric workflow
// run ID, never to a run attempt or producer job. It does not establish an
// atomic listing or prove that an artifact contains any kind of report.
type CIArtifactPage struct {
	SourceID            string
	RepositoryID, RunID int64
	ObservedAt          time.Time
	Artifacts           []CIArtifactMetadata
	TotalCount          int
	NextPage            int
}

// ListCIRunArtifactMetadata reads GitHub's run-level artifact metadata endpoint
// through the same bounded, credential-scoped, no-redirect Actions reader.
// A fresh attempt capability is only an authorization gate for the enrolled
// repo and run. GitHub's documented metadata does not carry run_attempt, job
// ID, or a producer attestation; even version-specific extra fields are not
// consumed as such. Do not join these items to an attempt/job by name, creation
// time, branch, an archive path, or by the capability used to request this list.
//
// https://docs.github.com/en/rest/actions/artifacts#list-workflow-run-artifacts
func (s *CIHealthSource) ListCIRunArtifactMetadata(ctx context.Context, a CIAttempt, page int) (CIArtifactPage, error) {
	return s.listCIRunArtifactMetadata(ctx, a, page, ciArtifactNow)
}

func (s *CIHealthSource) listCIRunArtifactMetadata(ctx context.Context, a CIAttempt, page int, clock func() ciArtifactTick) (CIArtifactPage, error) {
	r, ok := s.enrolled(ctx, a.repository.GarmID)
	before := clock()
	if !ok || a.source != s || a.repository != r || !ciPage(page) || !ciArtifactLease(a, before) ||
		a.run.RunID <= 0 || a.run.Attempt <= 0 {
		return CIArtifactPage{}, ErrObservation
	}
	var wire ciArtifactPageWire
	if s.client.get(ctx, ciRoot(r)+ciRunPath(a.run.RunID)+"/artifacts", ciPageQuery(page), &wire) != nil || wire.Artifacts == nil {
		return CIArtifactPage{}, ErrObservation
	}
	next, valid := ciNextPage(page, wire.Total, len(wire.Artifacts))
	if !valid {
		return CIArtifactPage{}, ErrObservation
	}
	if len(wire.Artifacts) == 0 {
		// An empty reply cannot itself prove the repository's immutable ID.
		// Don't confirm an empty list after a locator has been repurposed.
		var current ciRepoWire
		if s.client.get(ctx, ciRoot(r), url.Values{}, &current) != nil || !current.matches(r) {
			return CIArtifactPage{}, ErrObservation
		}
	}
	at := clock()
	if !at.notBefore(before) || !ciArtifactLease(a, at) || ctx.Err() != nil {
		return CIArtifactPage{}, ErrObservation
	}
	out := CIArtifactPage{
		SourceID: s.SourceID(), RepositoryID: r.GitHubID, RunID: a.run.RunID,
		ObservedAt: at.wall, Artifacts: make([]CIArtifactMetadata, 0, len(wire.Artifacts)), TotalCount: *wire.Total, NextPage: next,
	}
	seen := make(map[int64]bool, len(wire.Artifacts))
	for _, raw := range wire.Artifacts {
		row, valid := raw.validate(r.GitHubID, a.run.RunID, at.wall)
		if !valid || seen[row.ID] {
			return CIArtifactPage{}, ErrObservation
		}
		seen[row.ID] = true
		out.Artifacts = append(out.Artifacts, row)
	}
	// Projection is part of the operation: neither a successfully decoded
	// page nor a confirmed empty page may escape after cancellation, expiry
	// or rollback. Retain the actual observation time, not this final check.
	end := clock()
	if !end.notBefore(at) || !ciArtifactLease(a, end) || ctx.Err() != nil {
		return CIArtifactPage{}, ErrObservation
	}
	return out, nil
}

type ciArtifactTick struct {
	wall, monotonic time.Time
}

func ciArtifactNow() ciArtifactTick {
	now := time.Now()
	return ciArtifactTick{wall: now.UTC(), monotonic: now}
}

func (now ciArtifactTick) notBefore(prior ciArtifactTick) bool {
	return !now.wall.Before(prior.wall) && !now.monotonic.Before(prior.monotonic)
}

func ciArtifactLease(a CIAttempt, now ciArtifactTick) bool {
	// Round(0) strips the monotonic reading. Fail closed if it was lost;
	// ordinary time comparisons would silently fall back to wall time.
	if a.at.IsZero() || !a.at.Equal(a.minted.UTC()) || a.minted == a.minted.Round(0) ||
		now.monotonic == now.monotonic.Round(0) {
		return false
	}
	start := ciArtifactTick{wall: a.at, monotonic: a.minted}
	return now.notBefore(start) && now.wall.Before(a.at.Add(ciAttemptLifetime)) &&
		now.monotonic.Before(a.minted.Add(ciAttemptLifetime))
}

type ciArtifactPageWire struct {
	Total     *int             `json:"total_count"`
	Artifacts []ciArtifactWire `json:"artifacts"`
}

type ciArtifactWire struct {
	ID          int64             `json:"id"`
	Size        *int64            `json:"size_in_bytes"`
	Digest      *string           `json:"digest"`
	Expired     *bool             `json:"expired"`
	Created     *time.Time        `json:"created_at"`
	Expires     *time.Time        `json:"expires_at"`
	WorkflowRun ciArtifactRunWire `json:"workflow_run"`
}

type ciArtifactRunWire struct {
	ID           int64 `json:"id"`
	RepositoryID int64 `json:"repository_id"`
}

func (v *ciArtifactPageWire) UnmarshalJSON(raw []byte) error {
	type plain ciArtifactPageWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciArtifactWire) UnmarshalJSON(raw []byte) error {
	type plain ciArtifactWire
	return ciObject(raw, (*plain)(v))
}
func (v *ciArtifactRunWire) UnmarshalJSON(raw []byte) error {
	type plain ciArtifactRunWire
	return ciObject(raw, (*plain)(v))
}

func (w ciArtifactWire) validate(repositoryID, runID int64, now time.Time) (CIArtifactMetadata, bool) {
	if w.ID <= 0 || w.WorkflowRun.ID != runID || w.WorkflowRun.RepositoryID != repositoryID ||
		w.Size != nil && *w.Size < 0 || !ciClocks(w.Created, nil, now) ||
		!ciArtifactTime(w.Created) || !ciArtifactTime(w.Expires) ||
		w.Created != nil && w.Expires != nil && w.Expires.Before(*w.Created) {
		return CIArtifactMetadata{}, false
	}
	digest, known, valid := ciAdvertisedArtifactSHA256(w.Digest)
	if !valid {
		return CIArtifactMetadata{}, false
	}
	out := CIArtifactMetadata{ID: w.ID, Availability: CIArtifactAvailabilityUnknown,
		AdvertisedSHA256: digest, AdvertisedSHA256Known: known}
	if w.Size != nil {
		out.SizeInBytes, out.SizeKnown = *w.Size, true
	}
	if w.Created != nil {
		out.CreatedAt = w.Created.UTC()
	}
	if w.Expires != nil {
		out.ExpiresAt = w.Expires.UTC()
	}
	switch {
	case w.Expired != nil && *w.Expired, w.Expires != nil && !now.Before(out.ExpiresAt):
		out.Availability = CIArtifactExpired
	case w.Expired != nil && !*w.Expired && w.Expires != nil:
		out.Availability = CIArtifactListedUnexpired
	}
	return out, true
}

func ciArtifactTime(t *time.Time) bool {
	if t == nil {
		return true
	}
	year := t.UTC().Year()
	return !t.IsZero() && year > 0 && year <= 9999
}

// Missing and unsupported algorithms stay unpinned. If the response claims
// SHA-256, reject invalid encodings rather than treating a broken pin as absent.
func ciAdvertisedArtifactSHA256(raw *string) (digest [sha256.Size]byte, known, valid bool) {
	if raw == nil {
		return digest, false, true
	}
	algorithm, payload, separated := strings.Cut(*raw, ":")
	if !strings.EqualFold(algorithm, "sha256") {
		return digest, false, true
	}
	if !separated || algorithm != "sha256" || len(payload) != hex.EncodedLen(len(digest)) || strings.ToLower(payload) != payload {
		return digest, false, false
	}
	n, err := hex.Decode(digest[:], []byte(payload))
	if err != nil || n != len(digest) || digest == ([sha256.Size]byte{}) {
		return [sha256.Size]byte{}, false, false
	}
	return digest, true, true
}
