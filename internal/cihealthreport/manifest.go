package cihealthreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

const (
	// ReportManifestFormat identifies the published provenance manifest. A
	// manifest under this name is a reviewed claim set, not proof: ingestion
	// must still bind it to the authorized scope and the exact bytes.
	ReportManifestFormat = "garm-test-report-manifest-v1"
	// MaxReportManifestBytes bounds the published manifest independently of
	// any archive ceiling; a manifest never contains report bodies.
	MaxReportManifestBytes = 64 << 10
)

var ErrReportManifest = errors.New("invalid report provenance manifest")

// ReportManifestJob is the independently reviewed producer binding for one
// artifact. Every value must be established out of band; nothing in the
// archive, its name or the report XML may populate it.
type ReportManifestJob struct {
	JobID int64
	Shard string
	Retry uint16 // zero is a known initial execution of this shard
}

// ReportManifestArtifact pins one artifact's bytes and, only when the
// producer is independently proved, its extracted report entry.
type ReportManifestArtifact struct {
	ArtifactID   int64
	ArchiveBytes int64
	// ArchiveSHA256 pins the exact downloaded archive bytes.
	ArchiveSHA256 [sha256.Size]byte
	// Job is nil for a run-only metadata row: the artifact stays metadata
	// and must never be published as job-level test evidence.
	Job *ReportManifestJob
	// Entry fields are populated exactly when Job is non-nil.
	EntryName   string
	EntryBytes  int64
	EntrySHA256 [sha256.Size]byte
}

// ReportManifest is one exact attempt's reviewed claim set. It binds the
// source, repository, workflow, run and attempt to a bounded set of pinned
// artifacts. It does not authenticate itself: ingestion validates it against
// the authorized scope and the exact bytes before anything is published.
type ReportManifest struct {
	Source                   string
	RepositoryID, WorkflowID int64
	RunID, Attempt           int64
	Artifacts                []ReportManifestArtifact
}

// ParseReportManifest decodes one bounded manifest with strict shape checks:
// no unknown fields, no duplicate or case-folded keys, no trailing data, and
// no floats or strings where exact integer identities belong. A manifest is
// a claim set only; callers must not treat a successful parse as review.
func ParseReportManifest(raw []byte) (ReportManifest, error) {
	fail := func() (ReportManifest, error) { return ReportManifest{}, ErrReportManifest }
	if len(raw) == 0 || len(raw) > MaxReportManifestBytes || !utf8.Valid(raw) {
		return fail()
	}
	if err := manifestObject(raw); err != nil {
		return fail()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var wire reportManifestWire
	if d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF {
		return fail()
	}
	out, ok := wire.validate()
	if !ok {
		return fail()
	}
	return out, nil
}

type reportManifestWire struct {
	Format       string                       `json:"format"`
	Source       string                       `json:"source"`
	RepositoryID int64                        `json:"repository_id"`
	WorkflowID   int64                        `json:"workflow_id"`
	RunID        int64                        `json:"run_id"`
	Attempt      int64                        `json:"attempt"`
	Artifacts    []reportManifestArtifactWire `json:"artifacts"`
}

type reportManifestArtifactWire struct {
	ArtifactID    int64                  `json:"artifact_id"`
	ArchiveBytes  int64                  `json:"archive_bytes"`
	ArchiveSHA256 string                 `json:"archive_sha256"`
	Job           *reportManifestJobWire `json:"job"`
	EntryName     string                 `json:"entry_name"`
	EntryBytes    int64                  `json:"entry_bytes"`
	EntrySHA256   string                 `json:"entry_sha256"`
}

type reportManifestJobWire struct {
	JobID int64  `json:"job_id"`
	Shard string `json:"shard"`
	Retry uint16 `json:"retry"`
}

func (w reportManifestWire) validate() (ReportManifest, bool) {
	fail := func() (ReportManifest, bool) { return ReportManifest{}, false }
	if w.Format != ReportManifestFormat || !sourceAtom(w.Source) || w.RepositoryID <= 0 || w.WorkflowID <= 0 ||
		w.RunID <= 0 || w.Attempt <= 0 || len(w.Artifacts) == 0 || len(w.Artifacts) > cihealth.MaxRetainedAttemptReports {
		return fail()
	}
	out := ReportManifest{
		Source: w.Source, RepositoryID: w.RepositoryID, WorkflowID: w.WorkflowID,
		RunID: w.RunID, Attempt: w.Attempt, Artifacts: make([]ReportManifestArtifact, 0, len(w.Artifacts)),
	}
	artifacts := make(map[int64]bool, len(w.Artifacts))
	slots := make(map[manifestSlot]bool, len(w.Artifacts))
	for _, raw := range w.Artifacts {
		row, slot, ok := raw.validate()
		if !ok || artifacts[row.ArtifactID] || (row.Job != nil && slots[slot]) {
			return fail()
		}
		artifacts[row.ArtifactID] = true
		if row.Job != nil {
			slots[slot] = true
		}
		out.Artifacts = append(out.Artifacts, row)
	}
	return out, true
}

type manifestSlot struct {
	shard string
	retry uint16
}

func (w reportManifestArtifactWire) validate() (ReportManifestArtifact, manifestSlot, bool) {
	fail := func() (ReportManifestArtifact, manifestSlot, bool) {
		return ReportManifestArtifact{}, manifestSlot{}, false
	}
	archive, ok := manifestSHA256(w.ArchiveSHA256)
	if !ok || w.ArtifactID <= 0 || w.ArchiveBytes <= 0 || w.ArchiveBytes > MaxJUnitZIPBytes {
		return fail()
	}
	row := ReportManifestArtifact{ArtifactID: w.ArtifactID, ArchiveBytes: w.ArchiveBytes, ArchiveSHA256: archive}
	if w.Job == nil {
		// A run-only row pins the archive only; entry claims without a
		// producer proof are inconsistent, not a partial proof.
		if w.EntryName != "" || w.EntryBytes != 0 || w.EntrySHA256 != "" {
			return fail()
		}
		return row, manifestSlot{}, true
	}
	if w.Job.JobID <= 0 || !sourceAtom(w.Job.Shard) {
		return fail()
	}
	entry, ok := manifestSHA256(w.EntrySHA256)
	if !ok || !validJUnitZIPName(w.EntryName) || w.EntryBytes <= 0 || w.EntryBytes > MaxReportBytes {
		return fail()
	}
	row.Job = &ReportManifestJob{JobID: w.Job.JobID, Shard: w.Job.Shard, Retry: w.Job.Retry}
	row.EntryName, row.EntryBytes, row.EntrySHA256 = w.EntryName, w.EntryBytes, entry
	return row, manifestSlot{w.Job.Shard, w.Job.Retry}, true
}

func manifestSHA256(s string) ([sha256.Size]byte, bool) {
	zero := [sha256.Size]byte{}
	if len(s) != hex.EncodedLen(sha256.Size) {
		return zero, false
	}
	var out [sha256.Size]byte
	n, err := hex.Decode(out[:], []byte(s))
	if err != nil || n != len(out) || out == zero {
		return zero, false
	}
	return out, true
}

// manifestObject rejects duplicate or Unicode-fold-equivalent keys anywhere
// in the document and requires one exact top-level JSON object. encoding/json
// would otherwise let a later duplicate or case-aliased key win silently.
// Field names and value types are enforced by the strict typed decode.
func manifestObject(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') {
		return ErrReportManifest
	}
	if err := manifestKeys(d); err != nil {
		return err
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return ErrReportManifest
	}
	return nil
}

func manifestKeys(d *json.Decoder) error {
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		k, ok := key.(string)
		if err != nil || !ok || seen[manifestFoldKey(k)] {
			return ErrReportManifest
		}
		seen[manifestFoldKey(k)] = true
		if err := manifestValue(d); err != nil {
			return err
		}
	}
	return nil
}

func manifestValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return ErrReportManifest
	}
	switch t {
	case json.Delim('{'):
		if err := manifestKeys(d); err != nil {
			return err
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return ErrReportManifest
		}
	case json.Delim('['):
		for d.More() {
			if err := manifestValue(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return ErrReportManifest
		}
	}
	return nil
}

func manifestFoldKey(key string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			smallest = min(smallest, next)
		}
		return smallest
	}, key)
}
