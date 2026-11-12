//go:build linux && cgo

package supervisoraudit

import (
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mattn/go-sqlite3"
)

// MaxPreflightBytes bounds raw main-file copying. The format caps facts at 4096;
// 16 MiB leaves substantial room for its fixed schema, rows and indexes, but is
// not an unbounded arbitrary-SQLite reader. Oversize refuses, never truncates.
const MaxPreflightBytes int64 = 16 << 20
const copiedMetadataAttempts = 8
const copiedMetadataBudget = time.Second
const copiedMetadataPause = 2 * time.Millisecond

// Only this private COPY-read error can be considered for a fresh observation.
// It never escapes exported methods and is not a retryable original/commit error.
type metadataReadFailure struct{ cause error }

func (*metadataReadFailure) Error() string { return ErrHistory.Error() }
func (*metadataReadFailure) Unwrap() error { return ErrHistory }

func copiedMetadataCorrupt(e error) bool {
	var failure *metadataReadFailure
	if !errors.As(e, &failure) || failure == nil {
		return false
	}
	var sqliteError sqlite3.Error
	return errors.As(failure.cause, &sqliteError) && sqliteError.Code == sqlite3.ErrCorrupt
}

func openPinnedSource(path string, expected os.FileInfo) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrHistory
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		syscall.Close(fd)
		return nil, ErrHistory
	}
	i, e := f.Stat()
	if e != nil || !validPrivateInfo(i, false) || !os.SameFile(i, expected) {
		f.Close()
		return nil, ErrHistory
	}
	return f, nil
}

func (j *Journal) preflightMetadata() error { return j.preflightCopiedMetadata(j.inspectPrivateCopy) }

// inspect is always inspectPrivateCopy in the exported path. Injection is private
// and used only to prove retry exhaustion and guard refusal without corrupting IO.
func (j *Journal) preflightCopiedMetadata(inspect func() error) error {
	if inspect == nil {
		return ErrHistory
	}
	deadline := time.Now().Add(copiedMetadataBudget)
	for attempt := 0; attempt < copiedMetadataAttempts; attempt++ {
		if time.Now().After(deadline) || j.guard() != nil {
			return ErrHistory
		}
		e := inspect()
		if j.guard() != nil || time.Now().After(deadline) {
			return ErrHistory
		}
		if e == nil {
			return nil
		}
		if !copiedMetadataCorrupt(e) || attempt+1 == copiedMetadataAttempts {
			return ErrHistory
		}
		time.Sleep(copiedMetadataPause)
	}
	return ErrHistory
}

// inspectPrivateCopy never opens original SQLite. SQLite's immutable premise is
// true ONLY for the closed private copy being parsed. Raw bytes may straddle a
// checkpoint, so this is NOT a consistent database backup/live-fact snapshot.
// Successful exact STATIC metadata merely permits the unchanged locked original
// validation to proceed. No copied fact/absence/uniqueness decision is exposed.
func (j *Journal) inspectPrivateCopy() (result error) {
	if j.guard() != nil {
		return ErrHistory
	}
	source, e := openPinnedSource(j.path, j.file)
	if e != nil {
		return ErrHistory
	}
	defer func() {
		if source.Close() != nil {
			result = ErrHistory
		}
	}()
	before, e := source.Stat()
	if e != nil || before.Size() < 100 || before.Size() > MaxPreflightBytes {
		return ErrHistory
	}
	directory, e := os.MkdirTemp("", "supervisoraudit-preflight-")
	if e != nil {
		return ErrHistory
	}
	defer func() {
		if e := os.Remove(directory); e != nil && !os.IsNotExist(e) {
			result = ErrHistory
		}
	}()
	directoryInfo, e := private(directory, true)
	if e != nil {
		return ErrHistory
	}
	path := filepath.Join(directory, "metadata.db")
	copy, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return ErrHistory
	}
	defer func() {
		if copy != nil && copy.Close() != nil {
			result = ErrHistory
		}
		if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
			result = ErrHistory
		}
	}()
	n, e := io.Copy(copy, io.LimitReader(source, MaxPreflightBytes+1))
	if e != nil || n > MaxPreflightBytes {
		return ErrHistory
	}
	if e = copy.Close(); e != nil {
		copy = nil
		return ErrHistory
	}
	copy = nil
	copyInfo, e := private(path, false)
	if e != nil {
		return ErrHistory
	}
	after, e := source.Stat()
	if e != nil || !validPrivateInfo(after, false) || !os.SameFile(after, j.file) || after.Size() < 100 || after.Size() > MaxPreflightBytes || j.guard() != nil {
		return ErrHistory
	}
	uri := url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	uri.RawQuery = query.Encode()
	pre, e := sql.Open("sqlite3", uri.String())
	if e != nil {
		return ErrHistory
	}
	pre.SetMaxOpenConns(1)
	result = j.inspectMetadata(pre)
	closeError := pre.Close()
	current, e := private(path, false)
	currentDirectory, directoryError := private(directory, true)
	last, se := source.Stat()
	if closeError != nil || e != nil || !os.SameFile(current, copyInfo) || directoryError != nil || !os.SameFile(currentDirectory, directoryInfo) || se != nil || !validPrivateInfo(last, false) || !os.SameFile(last, j.file) || last.Size() < 100 || last.Size() > MaxPreflightBytes || j.guard() != nil {
		return ErrHistory
	}
	return result
}
