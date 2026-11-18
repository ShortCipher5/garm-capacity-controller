//go:build linux

package nativedevice

import (
	"context"
	"encoding/binary"
	"testing"
)

// interpret executes only the small instruction subset emitted by program. It
// checks policy semantics without installing a host BPF program.
func interpret(raw []byte, kind, access, major, minor uint32) bool {
	var regs [11]uint64
	ctx := []uint32{kind | access<<16, major, minor}
	for pc, steps := 0, 0; pc < len(raw)/8 && steps < 4096; pc, steps = pc+1, steps+1 {
		p := raw[pc*8:]
		op := p[0]
		dst, src := p[1]&15, p[1]>>4
		off := int(int16(binary.LittleEndian.Uint16(p[2:])))
		imm := int32(binary.LittleEndian.Uint32(p[4:]))
		switch op {
		case 0xb7:
			regs[dst] = uint64(int64(imm))
		case 0x61:
			if src != 1 || off%4 != 0 || off < 0 || off > 8 {
				panic("bad ctx")
			}
			regs[dst] = uint64(ctx[off/4])
		case 0x57:
			regs[dst] &= uint64(int64(imm))
		case 0x77:
			regs[dst] >>= uint(imm)
		case 0xbf:
			regs[dst] = regs[src]
		case 0x15:
			if regs[dst] == uint64(int64(imm)) {
				pc += off
			}
		case 0x55:
			if regs[dst] != uint64(int64(imm)) {
				pc += off
			}
		case 0x95:
			return regs[0] == 1
		default:
			panic("unexpected opcode")
		}
	}
	return false
}
func TestDevicePolicyDenyDefault(t *testing.T) {
	raw, e := program(10, 249)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range [][4]uint32{{2, 6, 10, 249}, {2, 7, 1, 3}, {2, 6, 136, 255}, {2, 2, 1, 8}} {
		if !interpret(raw, v[0], v[1], v[2], v[3]) {
			t.Fatal("allowed rejected", v)
		}
	}
	for _, v := range [][4]uint32{{1, 6, 8, 0}, {2, 6, 10, 200}, {2, 6, 10, 232}, {2, 6, 1, 1}, {2, 6, 5, 1}, {2, 1, 10, 249}, {2, 8, 10, 249}, {2, 6, 137, 0}, {9, 6, 1, 3}} {
		if interpret(raw, v[0], v[1], v[2], v[3]) {
			t.Fatal("device escaped", v)
		}
	}
}
func TestProgramDeterministicAndBoundToActualZFS(t *testing.T) {
	a, _ := program(10, 249)
	b, _ := program(10, 249)
	c, _ := program(10, 250)
	if digest(a) != digest(b) || digest(a) == digest(c) {
		t.Fatal("digest")
	}
	if _, e := program(0, 0); e == nil {
		t.Fatal("zero device")
	}
}
func TestZeroBoundaryRefused(t *testing.T) {
	var b Boundary
	if b.Recheck(context.Background()) == nil {
		t.Fatal("zero")
	}
}
func TestLinkInfoExactIdentity(t *testing.T) {
	raw := make([]byte, 32)
	u32(raw, 0, 3)
	u32(raw, 4, 12)
	u32(raw, 8, 13)
	u64(raw, 16, 14)
	u32(raw, 24, 6)
	if !matchesLink(raw, 13, 12, 14) {
		t.Fatal("valid")
	}
	for _, off := range []int{0, 4, 8, 16, 24} {
		bad := append([]byte(nil), raw...)
		bad[off]++
		if matchesLink(bad, 13, 12, 14) {
			t.Fatal(off)
		}
	}
}

func TestPinRequiresIndependentHostBPFFSMount(t *testing.T) {
	const root = "1 0 8:1 / / rw - ext4 /dev/root rw\n"
	const bpf = "2 1 0:29 / /sys/fs/bpf rw - bpf bpf rw\n"
	if !hostBPFMount([]byte(root+bpf), "/sys/fs/bpf/garm") {
		t.Fatal("host mount")
	}
	for _, s := range []string{root, root + "2 1 0:29 / /sys/fs/bpf rw - tmpfs tmpfs rw\n", root + bpf + "3 2 0:30 / /sys/fs/bpf/garm rw - tmpfs tmpfs rw\n", root + "2 1 0:29 /slice /sys/fs/bpf rw - bpf bpf rw\n"} {
		if hostBPFMount([]byte(s), "/sys/fs/bpf/garm") {
			t.Fatal("unretained or shadowed mount")
		}
	}
}
