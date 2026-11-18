//go:build linux

package nativequota

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DatasetLifetime retains privately observed identities, not caller GUIDs.
// Absence is a scoped identity observation, NEVER reclaimed bytes or release.
type DatasetLifetime struct {
	reader   *Reader
	root     FilesystemIdentity
	original Observation
}
type DatasetIdentityAbsence struct {
	root, rootGUID, dataset, guid string
	at                            time.Time
}

func (o DatasetIdentityAbsence) Dataset() string       { return o.dataset }
func (o DatasetIdentityAbsence) GUID() string          { return o.guid }
func (o DatasetIdentityAbsence) Root() string          { return o.root }
func (o DatasetIdentityAbsence) RootGUID() string      { return o.rootGUID }
func (o DatasetIdentityAbsence) ObservedAt() time.Time { return o.at }
func (o DatasetIdentityAbsence) Valid() bool {
	return validDataset(o.root) && validDataset(o.dataset) && o.guid != "" && o.rootGUID != "" && !o.at.IsZero()
}

// CaptureLifetime requires actual original quota evidence and an actual immutable
// ancestor identity; all are re-read. No persisted-record reconstruction exists.
func (r *Reader) CaptureLifetime(ctx context.Context, root FilesystemIdentity, original Observation) (DatasetLifetime, error) {
	if r == nil || ctx == nil || !original.Valid() || !validDataset(root.dataset) || root.guid == "" || !strings.HasPrefix(original.dataset, root.dataset+"/") {
		return DatasetLifetime{}, ErrQuota
	}
	parent, e := r.ReadFilesystem(ctx, root.dataset)
	if e != nil || parent != root {
		return DatasetLifetime{}, ErrQuota
	}
	current, e := r.Read(ctx, original.dataset, original.bytes)
	if e != nil || current.guid != original.guid {
		return DatasetLifetime{}, ErrQuota
	}
	parent, e = r.ReadFilesystem(ctx, root.dataset)
	if e != nil || parent != root {
		return DatasetLifetime{}, ErrQuota
	}
	return DatasetLifetime{r, root, current}, nil
}

type inventoryIdentity struct{ name, guid, kind, origin, deferDestroy, userrefs string }

func inventoryName(s string) bool {
	for _, sep := range []string{"@", "#"} {
		if strings.Contains(s, sep) {
			parts := strings.Split(s, sep)
			return len(parts) == 2 && validDataset(parts[0]) && validDataset("pool/"+parts[1])
		}
	}
	return validDataset(s) || validPool(s)
}
func parseIdentityInventory(raw []byte, root FilesystemIdentity) ([]inventoryIdentity, error) {
	if len(raw) == 0 || len(raw) > 8<<20 || raw[len(raw)-1] != '\n' {
		return nil, ErrQuota
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) > 100000 {
		return nil, ErrQuota
	}
	seen := map[string]bool{}
	out := make([]inventoryIdentity, 0, len(lines))
	rootSeen := false
	for _, line := range lines {
		f := strings.Split(line, "\t")
		if len(f) != 6 || !inventoryName(f[0]) || seen[f[0]] || !(f[0] == root.dataset || strings.HasPrefix(f[0], root.dataset+"/") || strings.HasPrefix(f[0], root.dataset+"@") || strings.HasPrefix(f[0], root.dataset+"#")) {
			return nil, ErrQuota
		}
		seen[f[0]] = true
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != f[1] {
			return nil, ErrQuota
		}
		switch f[2] {
		case "filesystem", "volume":
			if strings.ContainsAny(f[0], "@#") {
				return nil, ErrQuota
			}
		case "snapshot":
			if !strings.Contains(f[0], "@") {
				return nil, ErrQuota
			}
		case "bookmark":
			if !strings.Contains(f[0], "#") {
				return nil, ErrQuota
			}
		default:
			return nil, ErrQuota
		}
		if f[3] != "-" && (!inventoryName(f[3]) || !strings.Contains(f[3], "@")) {
			return nil, ErrQuota
		}
		if f[4] != "-" && f[4] != "on" && f[4] != "off" {
			return nil, ErrQuota
		}
		if f[5] != "-" {
			n, e := strconv.ParseUint(f[5], 10, 64)
			if e != nil || strconv.FormatUint(n, 10) != f[5] {
				return nil, ErrQuota
			}
		}
		if f[0] == root.dataset {
			if (root.guid != "" && f[1] != root.guid) || f[2] != "filesystem" {
				return nil, ErrQuota
			}
			rootSeen = true
		}
		out = append(out, inventoryIdentity{f[0], f[1], f[2], f[3], f[4], f[5]})
	}
	if !rootSeen {
		return nil, ErrQuota
	}
	return out, nil
}
func identityGone(items []inventoryIdentity, original Observation) bool {
	for _, v := range items {
		if v.guid == original.guid || v.name == original.dataset || strings.HasPrefix(v.name, original.dataset+"/") || strings.HasPrefix(v.name, original.dataset+"@") || strings.HasPrefix(v.name, original.dataset+"#") || strings.HasPrefix(v.origin, original.dataset+"@") {
			return false
		}
	}
	return true
}
func (l DatasetLifetime) ObserveIdentityAbsence(ctx context.Context) (DatasetIdentityAbsence, error) {
	if l.reader == nil || ctx == nil || !l.original.Valid() {
		return DatasetIdentityAbsence{}, ErrQuota
	}
	before, e := l.reader.ReadFilesystem(ctx, l.root.dataset)
	if e != nil || before != l.root {
		return DatasetIdentityAbsence{}, ErrQuota
	}
	raw, e := l.reader.readInventory(ctx, l.root.dataset)
	if e != nil {
		return DatasetIdentityAbsence{}, ErrQuota
	}
	items, e := parseIdentityInventory(raw, l.root)
	if e != nil || !identityGone(items, l.original) {
		return DatasetIdentityAbsence{}, ErrQuota
	}
	after, e := l.reader.ReadFilesystem(ctx, l.root.dataset)
	if e != nil || after != l.root {
		return DatasetIdentityAbsence{}, ErrQuota
	}
	return DatasetIdentityAbsence{l.root.dataset, l.root.guid, l.original.dataset, l.original.guid, time.Now().UTC()}, nil
}

type inventoryBuffer struct{ bytes.Buffer }

func (b *inventoryBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8<<20 {
		return 0, ErrQuota
	}
	return b.Buffer.Write(p)
}
func (r *Reader) readInventory(ctx context.Context, root string) ([]byte, error) {
	if r == nil || ctx == nil || os.Geteuid() != 0 || !(validDataset(root) || validPool(root)) {
		return nil, ErrQuota
	}
	binary, e := pinned(r.config.Binary, r.config.BinarySHA256)
	if e != nil {
		return nil, e
	}
	defer binary.Close()
	helper, e := pinned(r.config.Helper, r.config.HelperSHA256)
	if e != nil {
		return nil, e
	}
	defer helper.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mode := "--identity-inventory"
	if validPool(root) {
		mode = "--pool-identity-inventory"
	}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/4", mode, root)
	cmd.ExtraFiles = []*os.File{binary, helper}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var output inventoryBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || ctx.Err() != nil {
		return nil, ErrQuota
	}
	return output.Bytes(), nil
}
