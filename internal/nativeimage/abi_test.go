package nativeimage

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"strings"
	"testing"
)

func TestGNUVersionRequirements(t *testing.T) {
	m := fixture()
	m.Files["usr/lib/libc.so.6"] = File{strings.Repeat("c", 64), 0444}
	actual := map[string]elfVersions{"usr/lib/liblxc.so.1": {needed: map[string][]string{"libc.so.6": {"GLIBC_2.43"}}}, "usr/lib/libc.so.6": {defined: map[string]bool{"GLIBC_2.43": true}}}
	if !versionsSatisfied(m, actual) {
		t.Fatal("exact version")
	}
	actual["usr/lib/libc.so.6"] = elfVersions{defined: map[string]bool{"GLIBC_2.38": true}}
	if versionsSatisfied(m, actual) {
		t.Fatal("older libc accepted")
	}
	actual["usr/lib/libc.so.6"] = elfVersions{defined: map[string]bool{"GLIBC_2.43": true}}
	m.Files["lib/libc.so.6"] = File{strings.Repeat("d", 64), 0444}
	actual["lib/libc.so.6"] = elfVersions{defined: map[string]bool{"GLIBC_2.38": true}}
	if versionsSatisfied(m, actual) {
		t.Fatal("ambiguous old candidate accepted")
	}
}

// Build a minimal ELF fixture rather than depending on the host loader or
// executing/downloading a library in tests.
func versionELFFixture() []byte {
	order := binary.LittleEndian
	b := make([]byte, 512)
	h := elf.Header64{Type: uint16(elf.ET_DYN), Machine: uint16(elf.EM_X86_64), Version: 1, Shoff: 64, Ehsize: 64, Shentsize: 64, Shnum: 5}
	copy(h.Ident[:], []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	var header bytes.Buffer
	binary.Write(&header, order, h)
	copy(b, header.Bytes())
	add := func(index int, typ elf.SectionType, link uint32, entsize uint64, data []byte) {
		s := elf.Section64{Type: uint32(typ), Off: uint64(len(b)), Size: uint64(len(data)), Link: link, Addralign: 1, Entsize: entsize}
		var raw bytes.Buffer
		binary.Write(&raw, order, s)
		copy(b[64+index*64:], raw.Bytes())
		b = append(b, data...)
	}
	add(1, elf.SHT_STRTAB, 0, 0, []byte("\x00libc.so.6\x00GLIBC_2.43\x00foo\x00"))
	add(2, elf.SHT_DYNSYM, 1, 24, make([]byte, 48))
	add(3, elf.SHT_GNU_VERSYM, 2, 2, []byte{0, 0, 2, 0})
	need := make([]byte, 32)
	order.PutUint16(need[0:], 1)
	order.PutUint16(need[2:], 1)
	order.PutUint32(need[4:], 1)
	order.PutUint32(need[8:], 16)
	order.PutUint16(need[22:], 2)
	order.PutUint32(need[24:], 11)
	add(4, elf.SHT_GNU_VERNEED, 1, 0, need)
	return b
}
func TestReadRealELFVersionTable(t *testing.T) {
	v, e := readVersions(bytes.NewReader(versionELFFixture()))
	if e != nil || len(v.needed["libc.so.6"]) != 1 || v.needed["libc.so.6"][0] != "GLIBC_2.43" {
		t.Fatalf("%+v %v", v, e)
	}
	if _, e := readVersions(bytes.NewReader([]byte("not an ELF"))); e == nil {
		t.Fatal("plaintext")
	}
}
