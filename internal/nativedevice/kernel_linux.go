//go:build linux

package nativedevice

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"runtime"
	"unsafe"
)

func u32(b []byte, o int, v uint32) { binary.LittleEndian.PutUint32(b[o:], v) }
func u64(b []byte, o int, v uint64) { binary.LittleEndian.PutUint64(b[o:], v) }
func ptr(b []byte) uint64           { return uint64(uintptr(unsafe.Pointer(&b[0]))) }
func bpf(cmd int, a []byte) (int, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return -1, ErrDevice
	}
	r, _, e := unix.Syscall(unix.SYS_BPF, uintptr(cmd), uintptr(unsafe.Pointer(&a[0])), uintptr(len(a)))
	runtime.KeepAlive(a)
	if e != 0 {
		return -1, ErrDevice
	}
	return int(r), nil
}
func load(raw []byte) (int, error) {
	license := []byte("GPL\x00")
	var pinned runtime.Pinner
	pinned.Pin(&raw[0])
	pinned.Pin(&license[0])
	defer pinned.Unpin()
	a := make([]byte, 72)
	u32(a, 0, unix.BPF_PROG_TYPE_CGROUP_DEVICE)
	u32(a, 4, uint32(len(raw)/8))
	u64(a, 8, ptr(raw))
	u64(a, 16, ptr(license))
	copy(a[48:64], "garm_devices")
	u32(a, 68, unix.BPF_CGROUP_DEVICE)
	fd, e := bpf(unix.BPF_PROG_LOAD, a)
	runtime.KeepAlive(raw)
	runtime.KeepAlive(license)
	return fd, e
}
func link(prog, cgroup int) (int, error) {
	a := make([]byte, 16)
	u32(a, 0, uint32(prog))
	u32(a, 4, uint32(cgroup))
	u32(a, 8, unix.BPF_CGROUP_DEVICE)
	return bpf(unix.BPF_LINK_CREATE, a)
}
func pin(fd int, path string) error {
	p := append([]byte(path), 0)
	var pinned runtime.Pinner
	pinned.Pin(&p[0])
	defer pinned.Unpin()
	a := make([]byte, 16)
	u64(a, 0, ptr(p))
	u32(a, 8, uint32(fd))
	_, e := bpf(unix.BPF_OBJ_PIN, a)
	runtime.KeepAlive(p)
	return e
}
func get(path string) (int, error) {
	p := append([]byte(path), 0)
	var pinned runtime.Pinner
	pinned.Pin(&p[0])
	defer pinned.Unpin()
	a := make([]byte, 16)
	u64(a, 0, ptr(p))
	fd, e := bpf(unix.BPF_OBJ_GET, a)
	runtime.KeepAlive(p)
	return fd, e
}
func info(fd, size int) ([]byte, error) {
	out := make([]byte, size)
	var pinned runtime.Pinner
	pinned.Pin(&out[0])
	defer pinned.Unpin()
	a := make([]byte, 16)
	u32(a, 0, uint32(fd))
	u32(a, 4, uint32(size))
	u64(a, 8, ptr(out))
	_, e := bpf(unix.BPF_OBJ_GET_INFO_BY_FD, a)
	runtime.KeepAlive(out)
	if e != nil || binary.LittleEndian.Uint32(a[4:]) < uint32(size) {
		return nil, ErrDevice
	}
	return out, nil
}
func effective(cg int, id uint32) bool {
	ids := make([]byte, 256)
	var pinned runtime.Pinner
	pinned.Pin(&ids[0])
	defer pinned.Unpin()
	a := make([]byte, 32)
	u32(a, 0, uint32(cg))
	u32(a, 4, unix.BPF_CGROUP_DEVICE)
	u32(a, 8, 1)
	u64(a, 16, ptr(ids))
	u32(a, 24, 64)
	_, e := bpf(unix.BPF_PROG_QUERY, a)
	runtime.KeepAlive(ids)
	n := binary.LittleEndian.Uint32(a[24:])
	if e != nil || n > 64 {
		return false
	}
	for i := uint32(0); i < n; i++ {
		if binary.LittleEndian.Uint32(ids[i*4:]) == id {
			return true
		}
	}
	return false
}
func matchesLink(raw []byte, progID, linkID uint32, cgroupID uint64) bool {
	return len(raw) >= 32 && binary.LittleEndian.Uint32(raw) == unix.BPF_LINK_TYPE_CGROUP && binary.LittleEndian.Uint32(raw[4:]) == linkID && binary.LittleEndian.Uint32(raw[8:]) == progID && binary.LittleEndian.Uint64(raw[16:]) == cgroupID && binary.LittleEndian.Uint32(raw[24:]) == unix.BPF_CGROUP_DEVICE
}
