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

func TestVersionLabelWithoutRequiredSymbolRejected(t *testing.T) {
	m := fixture()
	m.Files["usr/lib/libc.so.6"] = File{strings.Repeat("c", 64), 0444}
	consumer := elfVersions{needed: map[string][]string{"libc.so.6": {"GLIBC_2.43"}}, imports: map[string][]versionedSymbol{"libc.so.6": {{"required_call", "GLIBC_2.43", elf.STT_FUNC}}}}
	provider := elfVersions{defined: map[string]bool{"GLIBC_2.43": true}, exports: map[string]map[string]elf.SymType{"GLIBC_2.43": {"another_call": elf.STT_FUNC}}}
	actual := map[string]elfVersions{"usr/lib/liblxc.so.1": consumer, "usr/lib/libc.so.6": provider}
	if versionsSatisfied(m, actual) {
		t.Fatal("version label accepted without imported symbol")
	}
	provider.exports["GLIBC_2.43"]["required_call"] = elf.STT_OBJECT
	if versionsSatisfied(m, actual) {
		t.Fatal("data object accepted for function")
	}
	provider.exports["GLIBC_2.43"]["required_call"] = elf.STT_GNU_IFUNC
	if !versionsSatisfied(m, actual) {
		t.Fatal("GNU indirect function rejected")
	}
	delete(provider.exports["GLIBC_2.43"], "required_call")
	provider.exports["GLIBC_2.42"] = map[string]elf.SymType{"required_call": elf.STT_FUNC}
	if versionsSatisfied(m, actual) {
		t.Fatal("wrong symbol version accepted")
	}
}
func TestRealELFVersionedImportExtraction(t *testing.T) {
	raw := versionELFFixture()
	order := binary.LittleEndian
	// The second dynamic symbol is at the declared DYNSYM offset + one null entry.
	symOffset := order.Uint64(raw[64+2*64+24:]) + 24
	stringsOffset := order.Uint64(raw[64+1*64+24:])
	name := bytes.Index(raw[stringsOffset:], []byte("foo\x00"))
	order.PutUint32(raw[symOffset:], uint32(name))
	raw[symOffset+4] = byte(elf.STB_GLOBAL)<<4 | byte(elf.STT_FUNC)
	v, e := readVersions(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	got := v.imports["libc.so.6"]
	if len(got) != 1 || got[0].name != "foo" || got[0].version != "GLIBC_2.43" || got[0].kind != elf.STT_FUNC {
		t.Fatalf("%+v", got)
	}
	raw[symOffset+4] = byte(elf.STB_WEAK)<<4 | byte(elf.STT_FUNC)
	v, e = readVersions(bytes.NewReader(raw))
	if e != nil || len(v.imports) != 0 {
		t.Fatal("weak optional import became mandatory", e, v)
	}
}

func TestRealELFVersionedExportVisibility(t *testing.T) {
	raw := versionELFFixture()
	o := binary.LittleEndian
	sym := o.Uint64(raw[64+2*64+24:]) + 24
	str := o.Uint64(raw[64+1*64+24:])
	o.PutUint32(raw[sym:], uint32(bytes.Index(raw[str:], []byte("foo\x00"))))
	raw[sym+4] = byte(elf.STB_GLOBAL)<<4 | byte(elf.STT_FUNC)
	o.PutUint16(raw[sym+6:], 1)
	section := 64 + 4*64
	off := o.Uint64(raw[section+24:])
	o.PutUint32(raw[section+4:], uint32(elf.SHT_GNU_VERDEF))
	clear(raw[off:])
	o.PutUint16(raw[off:], 1)
	o.PutUint16(raw[off+4:], 2)
	o.PutUint16(raw[off+6:], 1)
	o.PutUint32(raw[off+12:], 20)
	o.PutUint32(raw[off+20:], 11)
	v, e := readVersions(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	if k, ok := v.exports["GLIBC_2.43"]["foo"]; !ok || k != elf.STT_FUNC {
		t.Fatal(v)
	}
	raw[sym+5] = byte(elf.STV_HIDDEN)
	v, e = readVersions(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := v.exports["GLIBC_2.43"]["foo"]; ok {
		t.Fatal("hidden export accepted")
	}
}
