//go:build linux

package nativeimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectCompletedChecksActualBytesNotReceipt(t *testing.T) {
	dir := t.TempDir()
	r, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, e := InspectCompleted(context.Background(), dir, strings.Repeat("a", 64), strings.Repeat("b", 64)); e == nil {
		t.Fatal("unprotected incomplete output accepted")
	}
	m := fixture()
	raw, _ := json.Marshal(m)
	if e = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0400); e != nil {
		t.Fatal(e)
	}
	// Synthetic sparse bytes test integrity plumbing only, not ext4/boot validity.
	var imageSHA string
	for _, n := range []string{"runtime.raw", "reproducibility.raw"} {
		f, e := os.OpenFile(filepath.Join(dir, n), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.Truncate(m.SizeMiB << 20); e != nil {
			t.Fatal(e)
		}
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			t.Fatal(e)
		}
		f.Close()
		imageSHA = hex.EncodeToString(h.Sum(nil))
	}
	result := Result{m.Digest(), imageSHA, filepath.Join(dir, "runtime.raw"), false}
	raw, _ = json.Marshal(result)
	if e = os.WriteFile(filepath.Join(dir, "complete.json"), raw, 0400); e != nil {
		t.Fatal(e)
	}
	inspect := func() error {
		_, e := inspectCompleted(context.Background(), r, dir, m.Digest(), imageSHA, uint32(os.Getuid()))
		return e
	}
	if e = inspect(); e != nil {
		t.Fatal(e)
	}
	if _, e = inspectCompleted(context.Background(), r, dir, m.Digest(), strings.Repeat("a", 64), uint32(os.Getuid())); e == nil {
		t.Fatal("wrong approved digest")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = inspectCompleted(ctx, r, dir, m.Digest(), imageSHA, uint32(os.Getuid())); e == nil {
		t.Fatal("cancel")
	}

	original := append([]byte(nil), raw...)
	if e = os.Chmod(filepath.Join(dir, "complete.json"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Result{{m.Digest(), imageSHA, filepath.Join(dir, "runtime.raw"), true}, {m.Digest(), imageSHA, "/different/runtime.raw", false}} {
		changed, _ := json.Marshal(bad)
		if e = os.WriteFile(filepath.Join(dir, "complete.json"), changed, 0400); e != nil {
			t.Fatal(e)
		}
		if inspect() == nil {
			t.Fatal("altered completion accepted")
		}
	}
	if e = os.WriteFile(filepath.Join(dir, "complete.json"), original, 0400); e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(dir, "reproducibility.raw"), os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteAt([]byte{1}, 0)
	f.Close()
	if inspect() == nil {
		t.Fatal("changed reproduction accepted")
	}
	os.Remove(filepath.Join(dir, "reproducibility.raw"))
	if e = unix.Mkfifo(filepath.Join(dir, "reproducibility.raw"), 0600); e != nil {
		t.Fatal(e)
	}
	if inspect() == nil {
		t.Fatal("FIFO accepted")
	}
	os.Remove(filepath.Join(dir, "reproducibility.raw"))
	os.Symlink("runtime.raw", filepath.Join(dir, "reproducibility.raw"))
	if inspect() == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(filepath.Join(dir, "reproducibility.raw"))
	os.Link(filepath.Join(dir, "runtime.raw"), filepath.Join(dir, "reproducibility.raw"))
	if inspect() == nil {
		t.Fatal("hardlink accepted")
	}
}
