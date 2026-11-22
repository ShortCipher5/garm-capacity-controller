package cihealthreport

import (
	"crypto/sha256"
	"sort"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
)

// ReviewedCaseImpactInput is one independently reviewed case-and-shard cohort.
type ReviewedCaseImpactInput struct {
	Cohort       ReviewedCaseCohort
	Observations []ReviewedCaseObservation
}

// RankedCaseImpact contains only numeric reviewer-bound case/shard identity
// and the existing exact comparison; raw labels and proof are not returned.
type RankedCaseImpact struct {
	CaseID, ShardID int64
	Comparison      CaseComparison
}

type rankedCaseKey struct{ caseID, shardID int64 }
type rankedEntry struct {
	artifactID int64
	occurrence int
}
type rankedArtifact struct {
	job    cihealth.JobKey
	shard  string
	retry  uint16
	xmlSHA [sha256.Size]byte
}

// RankReviewedCaseImpact compares one source/repository/workflow/configuration/
// revision/runner-class category at a time, within MaxComparisonObservations
// total occurrences. Timed cases rank by descending summed observed duration
// (not job time or inferred coverage). Measured zero ranks ahead of missing
// timing, with numeric case then shard identity breaking ties. Untimed rows
// remain visible with Timed == 0. Any invalid/conflicting review rejects the
// whole ranking. This function does not fetch or authenticate reviews.
func RankReviewedCaseImpact(inputs []ReviewedCaseImpactInput) ([]RankedCaseImpact, error) {
	if len(inputs) == 0 || len(inputs) > MaxComparisonObservations {
		return nil, ErrCaseComparison
	}
	remaining := MaxComparisonObservations
	for _, input := range inputs {
		n := len(input.Observations)
		if n == 0 || n > remaining {
			return nil, ErrCaseComparison
		}
		remaining -= n
	}

	seen := make(map[rankedCaseKey]bool, len(inputs))
	shardLabels, shardIDs := make(map[int64]string), make(map[string]int64)
	artifacts := make(map[int64]rankedArtifact)
	entries := make(map[rankedEntry]bool)
	out := make([]RankedCaseImpact, 0, len(inputs))
	category := impactCategory(inputs[0].Cohort)
	for _, input := range inputs {
		c := input.Cohort
		key := rankedCaseKey{c.CaseID, c.ShardID}
		if impactCategory(c) != category || seen[key] {
			return nil, ErrCaseComparison
		}
		if label, exists := shardLabels[c.ShardID]; exists && label != c.ShardKey {
			return nil, ErrCaseComparison
		}
		if id, exists := shardIDs[c.ShardKey]; exists && id != c.ShardID {
			return nil, ErrCaseComparison
		}
		cmp, err := CompareReviewedCase(c, input.Observations, 0)
		if err != nil {
			return nil, err
		}
		seen[key] = true
		shardLabels[c.ShardID], shardIDs[c.ShardKey] = c.ShardKey, c.ShardID
		for _, observation := range input.Observations {
			// The reducer has already verified each review against the exact
			// report. Reject cross-cohort reuse or conflicting metadata too.
			r := observation.Review
			entry := rankedEntry{r.ArtifactID, r.Occurrence}
			proof := rankedArtifact{r.Job, c.ShardKey, r.Retry, r.ExpectedReportSHA256}
			if prior, exists := artifacts[r.ArtifactID]; (exists && prior != proof) || entries[entry] {
				return nil, ErrCaseComparison
			}
			artifacts[r.ArtifactID], entries[entry] = proof, true
		}
		out = append(out, RankedCaseImpact{c.CaseID, c.ShardID, cmp})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		x, y := a.Comparison.All, b.Comparison.All
		if (x.Timed > 0) != (y.Timed > 0) {
			return x.Timed > 0
		}
		if x.TotalTimedDuration != y.TotalTimedDuration {
			return x.TotalTimedDuration > y.TotalTimedDuration
		}
		if a.CaseID != b.CaseID {
			return a.CaseID < b.CaseID
		}
		return a.ShardID < b.ShardID
	})
	return out, nil
}

func impactCategory(c ReviewedCaseCohort) ReviewedCaseCohort {
	c.CaseID, c.ShardID, c.ShardKey = 0, 0, ""
	return c
}
