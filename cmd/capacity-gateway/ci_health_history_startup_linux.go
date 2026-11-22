package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net"
	"os"

	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealth"
	"github.com/ShortCipher5/garm-capacity-controller/internal/cihealthsource"
)

// This review is supplied in the existing private operator configuration,
// not inferred from the current contents of a writable directory.
type ciHealthHistoryConfiguration struct {
	Directory      string                 `json:"directory"`
	Device         uint64                 `json:"directory_device"`
	Inode          uint64                 `json:"directory_inode"`
	SnapshotSHA256 string                 `json:"snapshot_sha256"`
	SourceID       string                 `json:"source_id"`
	Workflows      []ciHealthHistoryScope `json:"workflows"`
}

type ciHealthHistoryScope struct {
	RepositoryID int64  `json:"repository_id"`
	WorkflowID   int64  `json:"workflow_id"`
	Revision     *int64 `json:"start_revision"`
	Checkpoint   *int64 `json:"start_checkpoint"`
}

func ciHistoryDigest(value string) ([sha256.Size]byte, bool) {
	var digest [sha256.Size]byte
	if len(value) != hex.EncodedLen(len(digest)) {
		return digest, false
	}
	n, err := hex.Decode(digest[:], []byte(value))
	return digest, err == nil && n == len(digest) && digest != ([sha256.Size]byte{}) && hex.EncodeToString(digest[:]) == value
}

func validateCIHealthHistoryConfiguration(c configuration, repos map[int64]bool) error {
	history := c.CIHealth.History
	if history == nil && !c.EnableCIHealthHistory {
		return nil
	}
	if history == nil || !c.EnableCIHealthHistory || len(history.Workflows) == 0 || len(history.Workflows) > 64 {
		return errCIHealthConfiguration
	}
	if host, _, err := net.SplitHostPort(c.Admin.Listen); err != nil || !net.ParseIP(host).IsLoopback() {
		return errCIHealthConfiguration
	}
	if !history.directoryReview().Valid() {
		return errCIHealthConfiguration
	}
	if _, ok := ciHistoryDigest(history.SnapshotSHA256); !ok {
		return errCIHealthConfiguration
	}
	if _, ok := ciHistoryDigest(history.SourceID); !ok {
		return errCIHealthConfiguration
	}
	seen := make(map[[2]int64]bool, len(history.Workflows))
	for _, w := range history.Workflows {
		key := [2]int64{w.RepositoryID, w.WorkflowID}
		if !repos[w.RepositoryID] || w.WorkflowID <= 0 || seen[key] || w.Revision == nil || w.Checkpoint == nil ||
			*w.Revision < 0 || *w.Revision == math.MaxInt64 || *w.Checkpoint < 0 || (*w.Revision == 0 && *w.Checkpoint != 0) {
			return errCIHealthConfiguration
		}
		seen[key] = true
	}
	return nil
}

func (c ciHealthHistoryConfiguration) directoryReview() cihealth.HistoryDirectoryReview {
	return cihealth.HistoryDirectoryReview{Directory: c.Directory, Device: c.Device, Inode: c.Inode}
}

func openCIHistoryRoot(ctx context.Context, c ciHealthHistoryConfiguration) (*os.Root, error) {
	root, err := cihealth.OpenReviewedHistoryDirectory(ctx, c.directoryReview())
	if err != nil {
		return nil, errCIHealthConfiguration
	}
	return root, nil
}

// Only the real startup path calls this helper. It neither reads credentials
// nor starts a collector, and hands off ownership only after exact review.
func attachConfiguredCIHistory(ctx context.Context, c configuration, runtime *ciHealthRuntime) error {
	if err := validateCIHealthConfiguration(c); err != nil {
		return err
	}
	if !c.EnableCIHealthHistory {
		return nil
	}
	history := *c.CIHealth.History
	if runtime == nil || !runtime.source.LifetimeActive() {
		return historySourceUnavailable
	}
	if runtime.source.SourceID() != history.SourceID {
		return historySourceReviewMismatch
	}
	root, err := openCIHistoryRoot(ctx, history)
	if err != nil {
		return historyCustodyUnverified
	}
	defer root.Close()
	digest, _ := ciHistoryDigest(history.SnapshotSHA256)
	reviewed := make([]cihealthsource.HistoryExpectation, 0, len(history.Workflows))
	for _, w := range history.Workflows {
		reviewed = append(reviewed, cihealthsource.HistoryExpectation{
			Scope:  cihealth.HistoryScope{Source: history.SourceID, RepositoryID: w.RepositoryID, WorkflowID: w.WorkflowID},
			Cursor: cihealth.HistoryCursor{Revision: *w.Revision, Checkpoint: *w.Checkpoint},
		})
	}
	owned, exact, err := cihealthsource.ReopenReviewedCycleHistory(ctx, runtime.source, root, digest, reviewed)
	if err != nil {
		return historyStartupRefusal(err)
	}
	if ctx.Err() != nil || runtime.attachOwnedHistoryWithTrends(owned, exact) != nil {
		_ = owned.Close()
		return errCIHealthConfiguration
	}
	return nil
}

// These fixed failure kinds are the only ones exposed from the serve path.
// They carry no raw cause, private path, identity, or reviewed value.
type historyStartupRefusalKind uint8

const (
	historySourceUnavailable historyStartupRefusalKind = iota + 1
	historySourceReviewMismatch
	historyCustodyUnverified
	historyPublicationUnverified
	historySnapshotReviewMismatch
	historyScopeReviewMismatch
	historyCursorReviewMismatch
	historyReopenUnsafe
)

func (k historyStartupRefusalKind) message() string {
	switch k {
	case historySourceUnavailable:
		return "configured history source is unavailable"
	case historySourceReviewMismatch:
		return "configured history source does not match the review"
	case historyCustodyUnverified:
		return "reviewed history directory custody could not be verified"
	case historyPublicationUnverified:
		return "history publication could not be verified; preserve the existing store for review"
	case historySnapshotReviewMismatch:
		return "history snapshot does not match independent review"
	case historyScopeReviewMismatch:
		return "stored history scope does not match independent review"
	case historyCursorReviewMismatch:
		return "history checkpoint does not match independent review"
	case historyReopenUnsafe:
		return "existing history snapshot could not be safely reopened"
	}
	return ""
}

func (k historyStartupRefusalKind) Error() string {
	return errCIHealthConfiguration.Error() + ": " + k.message()
}

func (k historyStartupRefusalKind) Unwrap() error { return errCIHealthConfiguration }

func historyStartupRefusal(err error) error {
	switch {
	case errors.Is(err, cihealth.ErrHistoryUncertain):
		return historyPublicationUnverified
	case errors.Is(err, cihealth.ErrHistorySnapshotReview):
		return historySnapshotReviewMismatch
	case errors.Is(err, cihealth.ErrHistoryScopeReview):
		return historyScopeReviewMismatch
	case errors.Is(err, cihealthsource.ErrHistoryCursorReview):
		return historyCursorReviewMismatch
	default:
		return historyReopenUnsafe
	}
}
