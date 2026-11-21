package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

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

func ciHistoryPath(dir string) ([]string, bool) {
	if len(dir) > 1024 || !utf8.ValidString(dir) || strings.TrimSpace(dir) != dir || !filepath.IsAbs(dir) ||
		dir == "/" || filepath.Clean(dir) != dir || strings.ContainsFunc(dir, unicode.IsControl) {
		return nil, false
	}
	parts := strings.Split(dir[1:], "/")
	if len(parts) > 32 {
		return nil, false
	}
	for _, name := range parts {
		if len(name) == 0 || len(name) > 255 {
			return nil, false
		}
	}
	return parts, true
}

func validateCIHealthHistoryConfiguration(c configuration, repos map[int64]bool) error {
	history := c.CIHealth.History
	if history == nil && !c.EnableCIHealthHistory {
		return nil
	}
	if history == nil || !c.EnableCIHealthHistory || len(history.Workflows) == 0 || len(history.Workflows) > 64 ||
		history.Device == 0 || history.Inode == 0 {
		return errCIHealthConfiguration
	}
	if host, _, err := net.SplitHostPort(c.Admin.Listen); err != nil || !net.ParseIP(host).IsLoopback() {
		return errCIHealthConfiguration
	}
	if _, ok := ciHistoryPath(history.Directory); !ok {
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

// This walks held directory descriptors from /, refusing symlinks and
// group/other-writable or non-deployment-owned ancestors (even sticky ones).
// The final physical identity and mode must match the independent review.
// No code can prove that root or another process with the same UID is absent:
// dedicating that UID's storage, including trustworthy rename/fsync behavior,
// is still the operator's responsibility.
func openCIHistoryRoot(ctx context.Context, c ciHealthHistoryConfiguration) (*os.Root, error) {
	parts, valid := ciHistoryPath(c.Directory)
	if !valid || ctx == nil || ctx.Err() != nil {
		return nil, errCIHealthConfiguration
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, errCIHealthConfiguration
	}
	success := false
	defer func() {
		if !success {
			_ = root.Close()
		}
	}()
	uid := uint32(os.Geteuid())
	allowed := func(info os.FileInfo, final bool) bool {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return false
		}
		if final {
			return stat.Uid == uid && info.Mode().Perm() == 0700 && uint64(stat.Dev) == c.Device && stat.Ino == c.Inode
		}
		return (stat.Uid == 0 || stat.Uid == uid) && info.Mode().Perm()&0022 == 0
	}
	if info, err := root.Stat("."); err != nil || !allowed(info, false) {
		return nil, errCIHealthConfiguration
	}
	for i, name := range parts {
		if ctx.Err() != nil {
			return nil, errCIHealthConfiguration
		}
		final := i == len(parts)-1
		before, err := root.Lstat(name)
		if err != nil || !allowed(before, final) {
			return nil, errCIHealthConfiguration
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return nil, errCIHealthConfiguration
		}
		after, err := child.Stat(".")
		if err != nil || !allowed(after, final) || !os.SameFile(before, after) {
			_ = child.Close()
			return nil, errCIHealthConfiguration
		}
		_ = root.Close()
		root = child
	}
	if ctx.Err() != nil {
		return nil, errCIHealthConfiguration
	}
	success = true
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
	if runtime == nil || !runtime.source.LifetimeActive() || runtime.source.SourceID() != history.SourceID {
		return errCIHealthConfiguration
	}
	root, err := openCIHistoryRoot(ctx, history)
	if err != nil {
		return err
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
		return errCIHealthConfiguration
	}
	if ctx.Err() != nil || runtime.attachOwnedHistoryWithTrends(owned, exact) != nil {
		_ = owned.Close()
		return errCIHealthConfiguration
	}
	return nil
}
