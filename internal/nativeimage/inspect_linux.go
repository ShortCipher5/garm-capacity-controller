//go:build linux

package nativeimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// InspectCompleted verifies local build output against independently supplied
// approved digests. It does not mount or execute the image, attest its contents,
// grant authority, or make a previous failed build retryable. A matching receipt
// alone is insufficient: both image files are hashed from held descriptors.
func InspectCompleted(ctx context.Context, directory, manifestSHA, imageSHA string) (Result, error) {
	if ctx == nil || ctx.Err() != nil || !absolute(directory) || !hash(manifestSHA) || !hash(imageSHA) || safeDir(directory) != nil {
		return Result{}, ErrImage
	}
	r, e := os.OpenRoot(directory)
	if e != nil {
		return Result{}, ErrImage
	}
	defer r.Close()
	return inspectCompleted(ctx, r, directory, manifestSHA, imageSHA, 0)
}

func inspectCompleted(ctx context.Context, r *os.Root, directory, manifestSHA, imageSHA string, owner uint32) (Result, error) {
	if ctx == nil || ctx.Err() != nil || !hash(manifestSHA) || !hash(imageSHA) {
		return Result{}, ErrImage
	}
	read := func(name string, limit int64) ([]byte, error) {
		f, err := inspectionFile(r, name, limit, owner)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, limit+1))
	}
	raw, e := read("manifest.json", 8<<20)
	if e != nil {
		return Result{}, ErrImage
	}
	var m Manifest
	if decodeInspection(raw, &m) != nil || m.Validate() != nil || m.Digest() != manifestSHA {
		return Result{}, ErrImage
	}
	canonical, _ := json.Marshal(m)
	if !bytes.Equal(raw, canonical) {
		return Result{}, ErrImage
	}
	raw, e = read("complete.json", 16384)
	if e != nil {
		return Result{}, ErrImage
	}
	var result Result
	if decodeInspection(raw, &result) != nil || result.BootQualified || result.ManifestSHA256 != manifestSHA || result.ImageSHA256 != imageSHA || result.ImagePath != filepath.Join(directory, "runtime.raw") {
		return Result{}, ErrImage
	}
	canonical, _ = json.Marshal(result)
	if !bytes.Equal(raw, canonical) {
		return Result{}, ErrImage
	}
	for _, name := range []string{"runtime.raw", "reproducibility.raw"} {
		f, err := inspectionFile(r, name, m.SizeMiB<<20, owner)
		if err != nil {
			return Result{}, ErrImage
		}
		before, err := f.Stat()
		if err != nil || before.Size() != m.SizeMiB<<20 {
			f.Close()
			return Result{}, ErrImage
		}
		h := sha256.New()
		buf := make([]byte, 1<<20)
		reader := io.LimitReader(f, (m.SizeMiB<<20)+1)
		for {
			if ctx.Err() != nil {
				f.Close()
				return Result{}, ErrImage
			}
			n, re := reader.Read(buf)
			if n > 0 {
				h.Write(buf[:n])
			}
			if re == io.EOF {
				break
			}
			if re != nil {
				f.Close()
				return Result{}, ErrImage
			}
		}
		after, err := f.Stat()
		f.Close()
		if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Sys().(*syscall.Stat_t).Ctim != after.Sys().(*syscall.Stat_t).Ctim || hex.EncodeToString(h.Sum(nil)) != imageSHA {
			return Result{}, ErrImage
		}
	}
	if ctx.Err() != nil {
		return Result{}, ErrImage
	}
	return result, nil
}
func decodeInspection(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrImage
	}
	return nil
}
func inspectionFile(r *os.Root, name string, limit int64, owner uint32) (*os.File, error) {
	prior, e := r.Lstat(name)
	if e != nil || !prior.Mode().IsRegular() {
		return nil, ErrImage
	}
	f, e := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrImage
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, ErrImage
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(prior, st) || !st.Mode().IsRegular() || st.Size() < 1 || st.Size() > limit || st.Mode().Perm()&0022 != 0 || s.Uid != owner || s.Nlink != 1 {
		f.Close()
		return nil, ErrImage
	}
	return f, nil
}
