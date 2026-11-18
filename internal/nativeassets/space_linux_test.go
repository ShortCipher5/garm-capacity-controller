//go:build linux

package nativeassets

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSpaceAccountingRefusesUnknownAndPreservesHeadroom(t *testing.T) {
	valid := unix.Statfs_t{Frsize: 4096, Blocks: 1 << 30, Bavail: 1 << 29, Ffree: 1000}
	if enoughSpace(valid, 16<<30) != nil {
		t.Fatal("valid budget refused")
	}
	cases := []unix.Statfs_t{valid, valid, valid, valid, valid, valid}
	cases[0].Frsize = 0
	cases[1].Bavail = 0
	cases[2].Ffree = 79
	cases[3].Flags = unix.ST_RDONLY
	cases[4].Bavail = valid.Blocks + 1
	cases[5].Frsize = 1 << 20
	for _, s := range cases {
		if enoughSpace(s, 64<<20) == nil {
			t.Fatal("unsafe space accepted", s)
		}
	}
	exact := valid
	exact.Bavail = uint64((diskHeadroom + metadataAllowance + (64 << 20)) / 4096)
	if enoughSpace(exact, 64<<20) != nil {
		t.Fatal("exact budget refused")
	}
	exact.Bavail--
	if enoughSpace(exact, 64<<20) == nil {
		t.Fatal("spent reserved headroom")
	}
	for _, remaining := range []int64{-1, 18 << 30} {
		if enoughSpace(valid, remaining) == nil {
			t.Fatal("invalid byte count")
		}
	}
}
func TestPreflightFailureDoesNotCopyAndMidstreamFailureStaysConsumed(t *testing.T) {
	source, parent, m, d := fixture(t)
	uid := uint32(os.Geteuid())
	if _, e := prepareWithSpace(t.Context(), source, parent, d, uid, func(int, int64) error { return ErrAssets }); e == nil {
		t.Fatal("accepted unavailable space")
	}
	if _, e := os.Stat(filepath.Join(parent, m.ID)); !os.IsNotExist(e) {
		t.Fatal("preflight copied assets")
	}
	calls := 0
	if _, e := prepareWithSpace(t.Context(), source, parent, d, uid, func(_ int, remaining int64) error {
		calls++
		if remaining < 0 {
			t.Fatal("negative accounting")
		}
		if calls >= 3 {
			return ErrAssets
		}
		return nil
	}); e == nil {
		t.Fatal("ignored falling headroom")
	}
	st, e := os.Stat(filepath.Join(parent, m.ID, "runtime.raw"))
	if e != nil || st.Size() != 1<<20 {
		t.Fatal("expected preserved partial file", st, e)
	}
	if _, e = os.Stat(filepath.Join(parent, m.ID, "complete.json")); !os.IsNotExist(e) {
		t.Fatal("false completion")
	}
	if _, e = prepare(t.Context(), source, parent, d, uid); e == nil {
		t.Fatal("replayed consumed ID")
	}
	if _, e = inspect(t.Context(), parent, m.ID, d, uid); e == nil {
		t.Fatal("partial counted complete")
	}
}
func TestHelperLockRefusesConcurrentAndSymlinkWriters(t *testing.T) {
	source, parent, m, d := fixture(t)
	uid := uint32(os.Geteuid())
	lock, e := prepareLock(parent, uid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prepare(t.Context(), source, parent, d, uid); e == nil {
		t.Fatal("parallel helper accepted")
	}
	if _, e = os.Stat(filepath.Join(parent, m.ID)); !os.IsNotExist(e) {
		t.Fatal("parallel helper consumed new destination")
	}
	lock.Close()
	path := filepath.Join(parent, "staging.lock")
	if os.Remove(path) != nil {
		t.Fatal("fixture remove")
	}
	if os.Symlink(filepath.Join(source, "manifest.json"), path) != nil {
		t.Fatal("fixture symlink")
	}
	if _, e = prepareLock(parent, uid); e == nil {
		t.Fatal("followed lock symlink")
	}
}
