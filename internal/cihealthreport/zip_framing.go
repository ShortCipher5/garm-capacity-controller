package cihealthreport

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
)

const (
	zipEndSize       = 22
	zipCentralSize   = 46
	zipLocalSize     = 30
	zipDescriptorLen = 16

	zipEndMagic        = 0x06054b50
	zipCentralMagic    = 0x02014b50
	zipLocalMagic      = 0x04034b50
	zipDescriptorMagic = 0x08074b50
)

type reviewedZIPMember struct {
	header           zip.FileHeader
	local, data, end int
}

// reviewedZIPFraming is a bounded envelope check, not a ZIP decompressor. It
// supports only a plain single-disk ZIP32, with no comments, preamble, gaps,
// ZIP64, dangling entries, or unsigned descriptors. Confirm actual central
// count/span before zip.NewReader can allocate based on the directory. Every
// local header, compressed payload and optional signed ZIP32 descriptor must
// occupy a disjoint extent entirely before the central directory.
func reviewedZIPFraming(archive []byte) ([]reviewedZIPMember, bool) {
	if len(archive) <= zipEndSize || len(archive) > MaxJUnitZIPBytes {
		return nil, false
	}
	endStart := len(archive) - zipEndSize
	end := archive[endStart:]
	if zip32(end) != zipEndMagic || zip16(end[4:]) != 0 || zip16(end[6:]) != 0 ||
		zip16(end[20:]) != 0 || zip16(end[8:]) != zip16(end[10:]) {
		return nil, false
	}
	count := int(zip16(end[10:]))
	centralSize := uint64(zip32(end[12:]))
	start := uint64(zip32(end[16:]))
	if count == 0 || count > MaxJUnitZIPEntries || start == 0 || centralSize < uint64(count*zipCentralSize) ||
		start+centralSize != uint64(endStart) {
		return nil, false
	}
	centralStart := int(start) // safe after exact bounded end check
	directory := archive[centralStart:endStart]
	members := make([]reviewedZIPMember, 0, count)
	names := make(map[string]bool, count)
	var totalExpanded uint64
	for i := 0; i < count; i++ {
		if len(directory) < zipCentralSize || zip32(directory) != zipCentralMagic ||
			zip16(directory[32:]) != 0 || zip16(directory[34:]) != 0 ||
			zip16(directory[36:]) > 1 {
			return nil, false
		}
		nameLen, extraLen := int(zip16(directory[28:])), int(zip16(directory[30:]))
		recordLen := zipCentralSize + nameLen + extraLen
		if nameLen == 0 || nameLen > maxJUnitZIPEntryName || (extraLen != 0 && extraLen != 9) || recordLen > len(directory) {
			return nil, false
		}
		record := directory[:recordLen]
		name := string(record[zipCentralSize : zipCentralSize+nameLen])
		lowerName := strings.ToLower(name) // accepted names are ASCII
		h := zip.FileHeader{
			Name: name, Extra: record[zipCentralSize+nameLen:],
			CreatorVersion: zip16(record[4:]), ReaderVersion: zip16(record[6:]),
			Flags: zip16(record[8:]), Method: zip16(record[10:]),
			ModifiedTime: zip16(record[12:]), ModifiedDate: zip16(record[14:]),
			CRC32: zip32(record[16:]), CompressedSize64: uint64(zip32(record[20:])),
			UncompressedSize64: uint64(zip32(record[24:])), ExternalAttrs: zip32(record[38:]),
		}
		localOffset := uint64(zip32(record[42:]))
		if !validJUnitZIPName(name) || names[lowerName] || !plainJUnitZIPMember(&h) ||
			h.UncompressedSize64 > MaxJUnitZIPExpandedBytes-totalExpanded || localOffset >= start {
			return nil, false
		}
		f := reviewedZIPMember{header: h, local: int(localOffset)}
		if !f.matchLocal(archive[:centralStart]) {
			return nil, false
		}
		members = append(members, f)
		names[lowerName] = true
		totalExpanded += h.UncompressedSize64
		directory = directory[recordLen:]
	}
	if len(directory) != 0 || !contiguousZIPMembers(members, centralStart) {
		return nil, false
	}
	for name := range names {
		for i := 0; i < len(name); i++ {
			if name[i] == '/' && names[name[:i]] {
				return nil, false // a file may not also be an ancestor directory
			}
		}
	}
	return members, true
}

func (f *reviewedZIPMember) matchLocal(data []byte) bool {
	h := &f.header
	headerLen := zipLocalSize + len(h.Name) + len(h.Extra)
	if f.local < 0 || len(data)-f.local < headerLen {
		return false
	}
	raw := data[f.local:]
	if zip32(raw) != zipLocalMagic || zip16(raw[4:]) != h.ReaderVersion || zip16(raw[6:]) != h.Flags ||
		zip16(raw[8:]) != h.Method || zip16(raw[10:]) != h.ModifiedTime || zip16(raw[12:]) != h.ModifiedDate ||
		zip16(raw[26:]) != uint16(len(h.Name)) || zip16(raw[28:]) != uint16(len(h.Extra)) ||
		!bytes.Equal(raw[zipLocalSize:zipLocalSize+len(h.Name)], []byte(h.Name)) ||
		!bytes.Equal(raw[zipLocalSize+len(h.Name):headerLen], h.Extra) {
		return false
	}
	f.data = f.local + headerLen
	compressed := h.CompressedSize64
	if compressed > uint64(len(data)-f.data) {
		return false
	}
	f.end = f.data + int(compressed)
	if h.Flags&0x08 == 0 {
		return zip32(raw[14:]) == h.CRC32 && uint64(zip32(raw[18:])) == compressed &&
			uint64(zip32(raw[22:])) == h.UncompressedSize64
	}
	if zip32(raw[14:]) != 0 || zip32(raw[18:]) != 0 || zip32(raw[22:]) != 0 ||
		len(data)-f.end < zipDescriptorLen {
		return false
	}
	desc := data[f.end : f.end+zipDescriptorLen]
	if zip32(desc) != zipDescriptorMagic || zip32(desc[4:]) != h.CRC32 ||
		uint64(zip32(desc[8:])) != compressed || uint64(zip32(desc[12:])) != h.UncompressedSize64 {
		return false
	}
	f.end += zipDescriptorLen
	return true
}

func contiguousZIPMembers(members []reviewedZIPMember, centralStart int) bool {
	// Preserve original central order for subsequent standard-parser checks.
	spans := slices.Clone(members)
	slices.SortFunc(spans, func(a, b reviewedZIPMember) int { return a.local - b.local })
	next := 0
	for _, span := range spans {
		if span.local != next || span.local >= span.data || span.data >= span.end || span.end > centralStart {
			return false
		}
		next = span.end
	}
	return next == centralStart
}

func zip16(p []byte) uint16 { return binary.LittleEndian.Uint16(p) }
func zip32(p []byte) uint32 { return binary.LittleEndian.Uint32(p) }
