//go:build linux

package nativeplanning

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// SavedSelections reconciles a lost Save response from actual protected files.
// Historical selections may be expired; only the builder's freshness check can
// accept them for creating a new bundle, and neither grants physical authority.
func (s *Service) SavedSelections(id string) ([]Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[id]
	if !ok {
		return nil, ErrSelection
	}
	return s.saved(b)
}
func (s *Service) saved(b Binding) ([]Saved, error) {
	if protectedDirectory(s.directory) != nil {
		return nil, ErrSelection
	}
	dir, e := os.Open(s.directory)
	if e != nil {
		return nil, ErrSelection
	}
	names, e := dir.Readdirnames(1025)
	dir.Close()
	if e != nil && e != io.EOF || len(names) > 1024 {
		return nil, ErrSelection
	}
	prefix := "selection-" + b.PreparationID + "-"
	out := []Saved{}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		hashPart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		if !strings.HasSuffix(name, ".json") || !hash(hashPart) || len(out) >= 64 {
			return nil, ErrSelection
		}
		f, e := os.OpenFile(filepath.Join(s.directory, name), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if e != nil {
			return nil, ErrSelection
		}
		st, e := f.Stat()
		if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() < 1 || st.Size() > 16384 {
			f.Close()
			return nil, ErrSelection
		}
		meta, ok := st.Sys().(*syscall.Stat_t)
		if !ok || meta.Uid != uint32(os.Geteuid()) || meta.Nlink != 1 {
			f.Close()
			return nil, ErrSelection
		}
		raw, e := io.ReadAll(io.LimitReader(f, 16385))
		f.Close()
		if e != nil || len(raw) > 16384 {
			return nil, ErrSelection
		}
		var value Selection
		if json.Unmarshal(raw, &value) != nil || ValidateSelection(value) != nil || Digest(value) != hashPart || Digest(value.Binding) != Digest(b) {
			return nil, ErrSelection
		}
		out = append(out, Saved{value, hashPart, "saved_configuration_not_applied"})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Selection.SavedAt.Equal(out[j].Selection.SavedAt) {
			return out[i].SelectionDigest < out[j].SelectionDigest
		}
		return out[i].Selection.SavedAt.Before(out[j].Selection.SavedAt)
	})
	return out, nil
}

// LookupSaved returns only a still-fresh, exact persisted selection for an
// independently configured internal builder. The path is never a browser input.
func (s *Service) LookupSaved(ctx context.Context, id, digest string) (string, Selection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[id]
	if !ok || !hash(digest) || s.current(ctx, b) != nil || ctx.Err() != nil {
		return "", Selection{}, ErrSelection
	}
	values, e := s.saved(b)
	if e != nil {
		return "", Selection{}, e
	}
	for _, v := range values {
		if v.SelectionDigest == digest {
			if ValidateFreshSelection(v.Selection, time.Now()) != nil {
				return "", Selection{}, ErrSelection
			}
			return filepath.Join(s.directory, "selection-"+b.PreparationID+"-"+digest+".json"), v.Selection, nil
		}
	}
	return "", Selection{}, ErrSelection
}
