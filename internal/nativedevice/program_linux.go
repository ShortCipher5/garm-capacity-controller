//go:build linux

// Package nativedevice installs a pinned, deny-default kernel device filter in
// the independently managed native parent cgroup. It is not runner authority.
package nativedevice

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

var ErrDevice = errors.New("native device boundary unavailable; preserve consumed attempt")

type instruction struct {
	op, regs byte
	off      int16
	imm      int32
}

func ins(op, dst, src byte, off int16, imm int32) instruction {
	return instruction{op, dst | src<<4, off, imm}
}

// cgroup_dev_ctx has access_type, major, minor. Access bits are mknod/read/write
// in the upper word; device type is block=1,char=2 in the lower word.
func program(zfsMajor, zfsMinor uint32) ([]byte, error) {
	if zfsMajor == 0 || zfsMajor > 4095 || zfsMinor > 1048575 {
		return nil, ErrDevice
	}
	rules := [][4]int32{{1, 3, 7, 0}, {1, 5, 7, 0}, {1, 7, 7, 0}, {1, 8, 7, 0}, {1, 9, 7, 0}, {5, 0, 7, 0}, {5, 2, 7, 0}, {136, 0, 7, 1}, {int32(zfsMajor), int32(zfsMinor), 6, 0}}
	// No externally configurable wildcard rule or block device exception.
	code := []instruction{ins(0xb7, 0, 0, 0, 0), ins(0x61, 2, 1, 0, 0), ins(0x57, 2, 0, 0, 65535), ins(0x15, 2, 0, 1, 2), ins(0x95, 0, 0, 0, 0), ins(0x61, 2, 1, 4, 0), ins(0x61, 3, 1, 8, 0), ins(0x61, 4, 1, 0, 0), ins(0x77, 4, 0, 0, 16)}
	for _, r := range rules {
		block := []instruction{ins(0x55, 2, 0, 0, r[0])}
		if r[3] == 0 {
			block = append(block, ins(0x55, 3, 0, 0, r[1]))
		}
		block = append(block, ins(0xbf, 5, 4, 0, 0), ins(0x57, 5, 0, 0, ^r[2]), ins(0x55, 5, 0, 2, 0), ins(0xb7, 0, 0, 0, 1), ins(0x95, 0, 0, 0, 0))
		block[0].off = int16(len(block) - 1)
		if r[3] == 0 {
			block[1].off = int16(len(block) - 2)
		}
		code = append(code, block...)
	}
	code = append(code, ins(0x95, 0, 0, 0, 0))
	raw := make([]byte, len(code)*8)
	for i, v := range code {
		p := raw[i*8:]
		p[0] = v.op
		p[1] = v.regs
		binary.LittleEndian.PutUint16(p[2:], uint16(v.off))
		binary.LittleEndian.PutUint32(p[4:], uint32(v.imm))
	}
	return raw, nil
}
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
