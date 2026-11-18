package overheadrefresh

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// This journal is operation metadata, not a second capacity ledger. Its service
// owner and ancestors are trusted, like private gateway configuration files.
type operationJournal struct {
	root *os.Root
	lock *os.File
}

func privateFile(f *os.File) bool {
	s, e := f.Stat()
	if e != nil {
		return false
	}
	v, ok := s.Sys().(*syscall.Stat_t)
	return ok && s.Mode().IsRegular() && s.Mode().Perm() == 0600 && v.Uid == uint32(os.Geteuid()) && v.Nlink == 1
}
func openOperationJournal(path, lineage string) (*operationJournal, map[string]Operation, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !digestString(lineage) {
		return nil, nil, ErrService
	}
	st, e := os.Lstat(path)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, nil, ErrService
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, nil, ErrService
	}
	j := &operationJournal{root: root}
	ok := false
	defer func() {
		if !ok {
			j.close()
		}
	}()
	j.lock, e = root.OpenFile("collection.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if e != nil || !privateFile(j.lock) || syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil, nil, ErrService
	}
	dir, e := root.Open(".")
	if e != nil {
		return nil, nil, ErrService
	}
	entries, e := dir.ReadDir(20003)
	dir.Close()
	if e != nil && e != io.EOF || len(entries) > 20002 {
		return nil, nil, ErrService
	}
	raw, e := j.read("lineage.json", 1024)
	if os.IsNotExist(e) {
		if len(entries) != 1 || entries[0].Name() != "collection.lock" {
			return nil, nil, ErrService
		}
		if j.atomic("lineage.json", []byte(`"`+lineage+`"`)) != nil {
			return nil, nil, ErrService
		}
	} else if e != nil || string(raw) != `"`+lineage+`"` {
		return nil, nil, ErrService
	}
	out := map[string]Operation{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "collection.lock" || name == "lineage.json" {
			continue
		}
		// Interrupted temporary writes are not receipts and never authorize replay.
		if strings.HasPrefix(name, ".pending-") {
			continue
		}
		if !strings.HasSuffix(name, ".json") || !requestPattern.MatchString(strings.TrimSuffix(name, ".json")) || len(out) >= 10000 {
			return nil, nil, ErrService
		}
		raw, e := j.read(name, 32768)
		if e != nil {
			return nil, nil, ErrService
		}
		var op Operation
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&op) != nil || d.Decode(new(any)) != io.EOF || validateOperation(op) != nil || name != op.RequestID+".json" {
			return nil, nil, ErrService
		}
		out[op.RequestID] = op
	}
	ok = true
	return j, out, nil
}
func validateOperation(v Operation) error {
	if !requestPattern.MatchString(v.RequestID) || !namePattern.MatchString(v.Recipe) || !digestString(v.Revision) || v.StartedAt.IsZero() || len(v.Results) > 10 {
		return ErrService
	}
	if v.State == "pending" {
		if !v.FinishedAt.IsZero() || len(v.Results) != 0 {
			return ErrService
		}
	} else if v.State == "complete" || v.State == "partial" || v.State == "failed" {
		if v.FinishedAt.IsZero() || v.FinishedAt.Before(v.StartedAt) || ((v.State == "partial" || v.State == "complete") && len(v.Results) == 0) || (v.State == "failed" && len(v.Results) != 0) {
			return ErrService
		}
	} else {
		return ErrService
	}
	seen := map[int64]bool{}
	for _, r := range v.Results {
		if r.BindingSequence <= 0 || seen[r.BindingSequence] || r.DomainSequence < 0 || r.ArtifactSequence < 0 || r.DomainSequence == 0 && r.ArtifactSequence == 0 {
			return ErrService
		}
		seen[r.BindingSequence] = true
	}
	return nil
}
func (j *operationJournal) read(name string, max int64) ([]byte, error) {
	f, e := j.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if !privateFile(f) {
		return nil, ErrService
	}
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrService
	}
	return b, nil
}
func (j *operationJournal) persist(op Operation) error {
	if validateOperation(op) != nil {
		return ErrService
	}
	raw, e := json.Marshal(op)
	if e != nil || len(raw) > 32768 {
		return ErrService
	}
	return j.atomic(op.RequestID+".json", raw)
}
func (j *operationJournal) atomic(name string, raw []byte) error {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ErrService
	}
	tmp := ".pending-" + hex.EncodeToString(b[:])
	f, e := j.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ErrService
	}
	defer j.root.Remove(tmp)
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrService
	}
	if j.root.Rename(tmp, name) != nil {
		return ErrService
	}
	dir, e := j.root.Open(".")
	if e != nil {
		return ErrService
	}
	defer dir.Close()
	return dir.Sync()
}
func (j *operationJournal) close() {
	if j.lock != nil {
		j.lock.Close()
		j.lock = nil
	}
	if j.root != nil {
		j.root.Close()
		j.root = nil
	}
}
