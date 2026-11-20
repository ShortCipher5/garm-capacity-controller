package cihealthreport

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
	"time"
)

func TestReviewedZIPCanonicalFramesMatchGoZIP32(t *testing.T) {
	plain := zipFixtureMember{header: zip.FileHeader{
		Name: zipTestName, Method: zip.Store, ReaderVersion: 10, CompressedSize64: uint64(len(zipTestXML)),
		UncompressedSize64: uint64(len(zipTestXML)), CRC32: crc32.ChecksumIEEE(zipTestXML),
	}, body: zipTestXML, raw: true}
	withDescriptor := fixtureZIPEntry("reports/second.xml", zipTestXML)
	withDescriptor.header.Modified = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	raw := buildJUnitZIP(t, plain, withDescriptor)
	members, ok := reviewedZIPFraming(raw)
	if !ok || len(members) != 2 || members[0].local != 0 || members[0].end != members[1].local ||
		members[0].header.Flags&0x08 != 0 || members[1].header.Flags&0x08 == 0 ||
		members[1].end != int(zip32(raw[len(raw)-zipEndSize+16:])) {
		t.Fatal("canonical Store/Deflate frames lost their exact boundaries")
	}
	for _, entry := range []string{zipTestName, "reports/second.xml"} {
		report, err := ParseReviewedJUnitZIP(raw, zipFixtureReview(raw, entry, zipTestXML))
		if err != nil || report.Summary().Timed != 1 {
			t.Fatalf("canonical entry unavailable: %v", err)
		}
	}
}

func TestReviewedZIPDirectoryAndLocalMetadataFailClosed(t *testing.T) {
	entry := fixtureZIPEntry(zipTestName, zipTestXML)
	entry.header.Modified = time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	base := buildJUnitZIP(t, entry)
	frames, ok := reviewedZIPFraming(base)
	if !ok || len(frames) != 1 {
		t.Fatal("test fixture is not a canonical archive")
	}
	end := len(base) - zipEndSize
	central := int(zip32(base[end+16:]))
	file := frames[0]
	desc := file.end - zipDescriptorLen
	localExtra := zipLocalSize + len(zipTestName)
	for name, mutate := range map[string]func([]byte){
		"declared entry count too large": func(b []byte) {
			binary.LittleEndian.PutUint16(b[end+8:], MaxJUnitZIPEntries+1)
			binary.LittleEndian.PutUint16(b[end+10:], MaxJUnitZIPEntries+1)
		},
		"directory count cannot fit": func(b []byte) {
			binary.LittleEndian.PutUint16(b[end+8:], 2)
			binary.LittleEndian.PutUint16(b[end+10:], 2)
		},
		"directory span too long": func(b []byte) { binary.LittleEndian.PutUint32(b[end+12:], ^uint32(0)) },
		"directory start invalid": func(b []byte) { binary.LittleEndian.PutUint32(b[end+16:], uint32(end)) },
		"multi disk":              func(b []byte) { b[end+4] = 1 },
		"local begins in central": func(b []byte) { binary.LittleEndian.PutUint32(b[central+42:], uint32(central)) },
		"payload exceeds local span": func(b []byte) {
			binary.LittleEndian.PutUint32(b[central+20:], ^uint32(0))
		},
		"local name length disagrees":  func(b []byte) { b[26]++ },
		"local name differs":           func(b []byte) { b[zipLocalSize] = 'X' },
		"local extra differs":          func(b []byte) { b[localExtra+5] ^= 1 },
		"local flags differ":           func(b []byte) { b[6] ^= 0x02 },
		"local method differs":         func(b []byte) { b[8] = 0 },
		"local size not placeholder":   func(b []byte) { b[18] = 1 },
		"missing descriptor signature": func(b []byte) { b[desc] ^= 1 },
		"descriptor size differs":      func(b []byte) { b[desc+12] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			raw := bytes.Clone(base)
			mutate(raw)
			if got, ok := reviewedZIPFraming(raw); ok || got != nil {
				t.Fatal("invalid framing reached standard ZIP parser boundary")
			}
			expectJUnitZIPRefusal(t, raw, zipFixtureReview(raw, zipTestName, zipTestXML))
		})
	}
}

func TestReviewedZIPRejectsDisjointFramingViolations(t *testing.T) {
	first := reviewedZIPMember{local: 0, data: 30, end: 64}
	second := reviewedZIPMember{local: 64, data: 94, end: 128}
	if !contiguousZIPMembers([]reviewedZIPMember{second, first}, 128) {
		t.Fatal("directory ordering changed physical extent validation")
	}
	for _, invalid := range []struct {
		name    string
		next    reviewedZIPMember
		central int
	}{
		{"overlapping full extents", reviewedZIPMember{local: 63, data: 94, end: 128}, 128},
		{"unclaimed gap", reviewedZIPMember{local: 65, data: 94, end: 128}, 128},
		{"ends inside directory", second, 127},
		{"unclaimed trailing bytes", second, 129},
		{"empty local header", reviewedZIPMember{local: 64, data: 64, end: 128}, 128},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if contiguousZIPMembers([]reviewedZIPMember{first, invalid.next}, invalid.central) {
				t.Fatal("unqualified extent passed")
			}
		})
	}
}
