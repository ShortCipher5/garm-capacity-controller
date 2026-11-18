package managedgarm

import (
	"encoding/hex"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// verityDigest reads the kernel fs-verity measurement, not a user-supplied hash.
// It never enables verity or changes file metadata. Unsupported filesystems and
// unsealed files fail closed. A measurement pins inode contents, NOT its path.
func verityDigest(f *os.File) (string, error) {
	if f == nil {
		return "", ErrBoundary
	}
	var out struct {
		Algorithm uint16
		Size      uint16
		Digest    [64]byte
	}
	out.Size = 64
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), uintptr(unix.FS_IOC_MEASURE_VERITY), uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(f)
	if errno != 0 || out.Algorithm != 1 || out.Size != 32 {
		return "", ErrBoundary
	}
	return hex.EncodeToString(out.Digest[:32]), nil
}

// sealedFileIdentity is deliberately private: neither a hash nor an fs-verity
// measurement alone can authorize start or prove the process loaded that inode.
type sealedFileIdentity struct {
	Device, Inode uint64
	Size          int64
	Verity        string
}

func measuredFileIdentity(f *os.File) (sealedFileIdentity, error) {
	if f == nil {
		return sealedFileIdentity{}, ErrBoundary
	}
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return sealedFileIdentity{}, ErrBoundary
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return sealedFileIdentity{}, ErrBoundary
	}
	digest, err := verityDigest(f)
	if err != nil {
		return sealedFileIdentity{}, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return sealedFileIdentity{}, ErrBoundary
	}
	end, ok := after.Sys().(*syscall.Stat_t)
	if !ok || st.Ctim != end.Ctim {
		return sealedFileIdentity{}, ErrBoundary
	}
	return sealedFileIdentity{Device: uint64(st.Dev), Inode: st.Ino, Size: st.Size, Verity: digest}, nil
}
