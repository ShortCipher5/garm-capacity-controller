package remotedisktelemetry

import (
	"bytes"
	"testing"

	"golang.org/x/sys/unix"
)

func localKernelFixture(t *testing.T) (localReview, unix.Statx_t, unix.Statx_t) {
	t.Helper()
	_, r, _, _, _ := fixture(t)
	review := localReview{scope: r.Scope, inode: r.DirectoryInode, until: r.ExpiresAt}
	common := unix.Statx_t{Mask: unix.STATX_INO | unix.STATX_TYPE, Mode: unix.S_IFDIR,
		Ino: review.inode, Dev_major: r.Scope.DeviceMajor, Dev_minor: r.Scope.DeviceMinor}
	ordinary, unique := common, common
	ordinary.Mask |= unix.STATX_MNT_ID
	unique.Mask |= unix.STATX_MNT_ID_UNIQUE
	ordinary.Mnt_id, unique.Mnt_id = 125, r.Scope.MountID
	return review, ordinary, unique
}

func TestLocalKernelOrdinaryAndUniqueMountIDsRemainSeparate(t *testing.T) {
	r, ordinary, unique := localKernelFixture(t)
	got, ok := localHeldMountFacts(r, ordinary, unique)
	if !ok || got.ordinary != 125 || got.unique != r.scope.MountID {
		t.Fatal("valid separate measurements refused")
	}
	for _, alter := range []func(*unix.Statx_t, *unix.Statx_t){
		func(o, u *unix.Statx_t) { *o, *u = *u, *o },
		func(o, _ *unix.Statx_t) { o.Mask |= unix.STATX_MNT_ID_UNIQUE },
		func(_, u *unix.Statx_t) { u.Mask &^= unix.STATX_MNT_ID_UNIQUE },
		func(_, u *unix.Statx_t) { u.Mnt_id++ },
		func(o, _ *unix.Statx_t) { o.Mnt_id = 0 },
		func(o, _ *unix.Statx_t) { o.Dev_minor++ },
		func(_, u *unix.Statx_t) { u.Ino++ },
		func(o, _ *unix.Statx_t) { o.Mode = unix.S_IFREG },
	} {
		o, u := ordinary, unique
		alter(&o, &u)
		if got, ok := localHeldMountFacts(r, o, u); ok || got != (localHeldMount{}) {
			t.Fatal("ambiguous or foreign descriptor observation accepted")
		}
	}
}

func TestLocalKernelRequiresExactlyOneVisibleExt4OrdinaryMount(t *testing.T) {
	r, ordinary, unique := localKernelFixture(t)
	row := []byte("125 99 8:2 /test\\040root /test rw,nosuid shared:13 - ext4 /dev/fixture rw\n")
	other := []byte("80 21 8:2 / /other rw - overlay overlay rw\n")
	if !localMountInfoCrosswalk(append(bytes.Clone(other), row...), ordinary.Mnt_id, r) {
		t.Fatal("expected fixed ordinary mount crosswalk and filesystem kind")
	}
	if localMountInfoCrosswalk(row, unique.Mnt_id, r) {
		t.Fatal("unique ID treated as ordinary mountinfo ID")
	}
	for _, raw := range [][]byte{
		other, row[:len(row)-1], append(bytes.Clone(row), row...),
		bytes.Replace(row, []byte(" - ext4 "), []byte(" - ext3 "), 1),
		bytes.Replace(row, []byte(" - ext4 "), []byte(" - ext2 "), 1),
		bytes.Replace(row, []byte("8:2"), []byte("8:3"), 1),
		bytes.Replace(row, []byte("125 "), []byte("0125 "), 1),
		bytes.Replace(row, []byte(" - "), []byte(" bogus "), 1),
		append(bytes.Clone(row), bytes.Repeat([]byte{'x'}, localMountInfoLimit)...),
	} {
		if localMountInfoCrosswalk(raw, ordinary.Mnt_id, r) {
			t.Fatal("hidden or ambiguous mount accepted")
		}
	}
}
