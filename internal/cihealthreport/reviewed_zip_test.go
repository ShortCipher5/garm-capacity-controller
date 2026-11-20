package cihealthreport

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"io/fs"
	"strings"
	"testing"
	"time"
)

var zipTestXML = []byte(`<?xml version="1.0"?><testsuite><testcase name="private_fixture_case" time="1.25"/></testsuite>`)

const zipTestName = "reports/private-fixture.xml"

type zipFixtureMember struct {
	header zip.FileHeader
	body   []byte
	raw    bool
}

func buildJUnitZIP(t *testing.T, members ...zipFixtureMember) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, m := range members {
		header := m.header
		var writer interface{ Write([]byte) (int, error) }
		var err error
		if m.raw {
			writer, err = w.CreateRaw(&header)
		} else {
			writer, err = w.CreateHeader(&header)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipFixtureReview(archive []byte, expected string, body []byte) ReviewedJUnitZIP {
	return ReviewedJUnitZIP{
		Binding: fixtureBinding, ArchiveBytes: int64(len(archive)), ArchiveSHA256: sha256.Sum256(archive),
		EntryName: expected, EntryBytes: int64(len(body)), EntrySHA256: sha256.Sum256(body),
	}
}
func fixtureZIPEntry(name string, body []byte) zipFixtureMember {
	return zipFixtureMember{header: zip.FileHeader{Name: name, Method: zip.Deflate}, body: body}
}

func TestReviewedJUnitZIPParsesOnlyExactReviewedEntry(t *testing.T) {
	selected := fixtureZIPEntry(zipTestName, zipTestXML)
	selected.header.Modified = time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	// A canonical unrelated ZIP member is not itself a JUnit report. Its
	// body is neither decompressed nor interpreted by this operation.
	otherXML := []byte("<note/>")
	bystander := zipFixtureMember{header: zip.FileHeader{
		Name: "reports/unselected.xml", Method: zip.Store, ReaderVersion: 20,
		CompressedSize64: uint64(len(otherXML)), UncompressedSize64: uint64(len(otherXML)), CRC32: crc32.ChecksumIEEE(otherXML),
	}, body: otherXML, raw: true}
	archive := buildJUnitZIP(t, bystander, selected)
	review := zipFixtureReview(archive, zipTestName, zipTestXML)
	report, err := ParseReviewedJUnitZIP(archive, review)
	if err != nil || report.Binding().ArtifactID != fixtureBinding.ArtifactID || report.Binding().Job != nil ||
		report.Summary().Timed != 1 || report.Summary().P95 != 1250*time.Millisecond || report.digest != review.EntrySHA256 ||
		len(report.Cases()) != 1 || report.Cases()[0].Outcome != Pass {
		t.Fatalf("reviewed projection failed: %+v %v", report, err)
	}
	for _, prohibited := range []string{zipTestName, "private_fixture_case", "unselected.xml", "<note/>", fmt.Sprintf("%x", review.EntrySHA256)} {
		if strings.Contains(fmt.Sprintf("%#v", report), prohibited) {
			t.Fatal("private report evidence unexpectedly exposed")
		}
	}
}

func TestReviewedJUnitZIPConnectsExactEntryDigestToComparator(t *testing.T) {
	// Exact normalized content and in-memory case proof, independently
	// supplied by the fixture. The archive does not invent this binding.
	raw := []byte(`<testsuite><testcase name="synthetic-private"/><testcase name="synthetic-private" time="1"></testcase></testsuite>`)
	reviewed := reviewedTestCase(t, 3, 1, 0, 707, Pass, "1")
	archive := buildJUnitZIP(t, fixtureZIPEntry(zipTestName, raw))
	review := zipFixtureReview(archive, zipTestName, raw)
	review.Binding = reviewed.Report.Binding()
	report, err := ParseReviewedJUnitZIP(archive, review)
	if err != nil {
		t.Fatal(err)
	}
	reviewed.Report = report
	if c, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{reviewed}, 0); err != nil || c.First.Passed != 1 {
		t.Fatalf("reviewed report proof lost before comparison: %+v %v", c, err)
	}
	reviewed.Review.ExpectedReportSHA256 = review.ArchiveSHA256
	if _, err := CompareReviewedCase(compareCohort, []ReviewedCaseObservation{reviewed}, 0); err != ErrCaseComparison {
		t.Fatal("archive-level digest substituted for exact XML digest")
	}
}

func TestReviewedJUnitZIPDoesNotInferMissingOrMismatchedReview(t *testing.T) {
	archive := buildJUnitZIP(t, fixtureZIPEntry(zipTestName, zipTestXML))
	for name, edit := range map[string]func(*ReviewedJUnitZIP){
		"archive length absent": func(r *ReviewedJUnitZIP) { r.ArchiveBytes = 0 },
		"archive length wrong":  func(r *ReviewedJUnitZIP) { r.ArchiveBytes-- },
		"archive digest absent": func(r *ReviewedJUnitZIP) { r.ArchiveSHA256 = [sha256.Size]byte{} },
		"archive digest wrong":  func(r *ReviewedJUnitZIP) { r.ArchiveSHA256[0] ^= 1 },
		"entry name absent":     func(r *ReviewedJUnitZIP) { r.EntryName = "" },
		"entry case differs":    func(r *ReviewedJUnitZIP) { r.EntryName = "reports/Private-fixture.xml" },
		"entry path absent":     func(r *ReviewedJUnitZIP) { r.EntryName = "private-fixture.xml" },
		"entry size absent":     func(r *ReviewedJUnitZIP) { r.EntryBytes = 0 },
		"entry size wrong":      func(r *ReviewedJUnitZIP) { r.EntryBytes++ },
		"entry hash absent":     func(r *ReviewedJUnitZIP) { r.EntrySHA256 = [sha256.Size]byte{} },
		"entry hash wrong":      func(r *ReviewedJUnitZIP) { r.EntrySHA256[0] ^= 1 },
		"archive hash as entry": func(r *ReviewedJUnitZIP) { r.EntrySHA256 = r.ArchiveSHA256 },
		"artifact absent":       func(r *ReviewedJUnitZIP) { r.Binding.ArtifactID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			review := zipFixtureReview(archive, zipTestName, zipTestXML)
			edit(&review)
			expectJUnitZIPRefusal(t, archive, review)
		})
	}
}

func TestReviewedJUnitZIPRefusesAmbiguousAndUnqualifiedZIPMembers(t *testing.T) {
	f := fixtureZIPEntry(zipTestName, zipTestXML)
	symlink := fixtureZIPEntry("reports/link.xml", []byte(zipTestName))
	symlink.header.SetMode(fs.ModeSymlink | 0o777)
	customExtra := fixtureZIPEntry("reports/extension.xml", zipTestXML)
	customExtra.header.Extra = []byte{0x0d, 0, 4, 0, 0, 0, 0, 0}
	encrypted := fixtureZIPEntry("reports/encrypted.xml", zipTestXML)
	encrypted.header.Flags = 1
	fakeMethod := zipFixtureMember{header: zip.FileHeader{
		Name: "reports/method.xml", Method: 99, ReaderVersion: 20, CompressedSize64: 5,
		UncompressedSize64: 5, CRC32: crc32.ChecksumIEEE([]byte("bytes")),
	}, body: []byte("bytes"), raw: true}
	badCRC := zipFixtureMember{header: zip.FileHeader{
		Name: zipTestName, Method: zip.Store, ReaderVersion: 20, CompressedSize64: uint64(len(zipTestXML)),
		UncompressedSize64: uint64(len(zipTestXML)), CRC32: crc32.ChecksumIEEE(zipTestXML) ^ 1,
	}, body: zipTestXML, raw: true}
	badXML := []byte(`<!DOCTYPE testsuite [<!ENTITY bad "irrelevant">]><testsuite/>`)
	for name, items := range map[string][]zipFixtureMember{
		"duplicate":        {f, f},
		"duplicate casing": {f, fixtureZIPEntry("REPORTS/private-fixture.xml", zipTestXML)},
		"traversal":        {f, fixtureZIPEntry("reports/../danger.xml", zipTestXML)},
		"absolute":         {f, fixtureZIPEntry("/danger.xml", zipTestXML)},
		"backslash":        {f, fixtureZIPEntry(`reports\danger.xml`, zipTestXML)},
		"drive":            {f, fixtureZIPEntry(`C:danger.xml`, zipTestXML)},
		"special device":   {f, fixtureZIPEntry("con.xml", zipTestXML)},
		"directory alias":  {f, fixtureZIPEntry(zipTestName+"/nested.xml", zipTestXML)},
		"foreign type":     {f, fixtureZIPEntry("report.zip", []byte("ZIP"))},
		"empty sibling":    {f, fixtureZIPEntry("empty.xml", nil)},
		"symlink":          {f, symlink},
		"custom extra":     {f, customExtra},
		"encrypted":        {f, encrypted},
		"custom method":    {f, fakeMethod},
		"checksum":         {badCRC},
	} {
		t.Run(name, func(t *testing.T) {
			archive := buildJUnitZIP(t, items...)
			expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, zipTestXML))
		})
	}
	archive := buildJUnitZIP(t, fixtureZIPEntry(zipTestName, badXML))
	expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, badXML))
	truncated := archive[:len(archive)-7]
	expectJUnitZIPRefusal(t, truncated, zipFixtureReview(truncated, zipTestName, badXML))
}

func TestReviewedJUnitZIPEnforcesCompressedDeclaredAndExpansionLimits(t *testing.T) {
	f := fixtureZIPEntry(zipTestName, zipTestXML)
	big := make([]byte, MaxJUnitZIPBytes+1)
	expectJUnitZIPRefusal(t, big, zipFixtureReview(big, zipTestName, zipTestXML))

	bigXML := bytes.Repeat([]byte{' '}, MaxReportBytes+1)
	archive := buildJUnitZIP(t, f, fixtureZIPEntry("oversize.xml", bigXML))
	expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, zipTestXML))
	archive = buildJUnitZIP(t, fixtureZIPEntry(zipTestName, bigXML))
	expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, bigXML))

	members := []zipFixtureMember{f}
	for i := range MaxJUnitZIPEntries {
		members = append(members, fixtureZIPEntry(fmt.Sprintf("entry-%d.xml", i), zipTestXML))
	}
	archive = buildJUnitZIP(t, members...)
	expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, zipTestXML))

	members = []zipFixtureMember{f}
	for i := range MaxJUnitZIPExpandedBytes / MaxReportBytes {
		members = append(members, fixtureZIPEntry(fmt.Sprintf("expanded-%d.xml", i), bigXML[:MaxReportBytes]))
	}
	archive = buildJUnitZIP(t, members...)
	expectJUnitZIPRefusal(t, archive, zipFixtureReview(archive, zipTestName, zipTestXML))
}

func expectJUnitZIPRefusal(t *testing.T, raw []byte, review ReviewedJUnitZIP) {
	t.Helper()
	result, err := ParseReviewedJUnitZIP(raw, review)
	if err != ErrJUnitZIP || len(result.Cases()) != 0 || result.Binding().ArtifactID != 0 || result.digest != ([sha256.Size]byte{}) {
		t.Fatalf("invalid ZIP returned data or a non-generic error: %+v %v", result, err)
	}
}
