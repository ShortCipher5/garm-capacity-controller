package cihealthreport

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"io/fs"
	"strings"
)

const (
	MaxJUnitZIPBytes         = 2 << 20
	MaxJUnitZIPEntries       = 32
	MaxJUnitZIPExpandedBytes = 4 * MaxReportBytes
	maxJUnitZIPEntryName     = 192
)

var ErrJUnitZIP = errors.New("invalid or unverified JUnit ZIP report")

// ReviewedJUnitZIP is a caller-reviewed binding, not an artifact discovery or
// authentication API. Review must establish that the exact stated archive
// size/digest and exact selected entry name/size/digest belong to this artifact
// in the already authorized scope. EntrySHA256 is for decompressed XML bytes,
// never the enclosing archive or a reserialized report. A missing digest or
// size does not default to values advertised by the ZIP payload.
type ReviewedJUnitZIP struct {
	Binding       ArtifactBinding
	ArchiveBytes  int64
	ArchiveSHA256 [sha256.Size]byte
	EntryName     string
	EntryBytes    int64
	EntrySHA256   [sha256.Size]byte
}

// ParseReviewedJUnitZIP accepts one explicitly reviewed XML entry from a small
// plain ZIP. It looks up only the exact entry and checks the archive's directory
// for ambiguity and unsupported formats; it never opens unselected members.
// No files are created and no path or ZIP diagnostic is returned. The result
// has the normalizer's private exact-entry digest for case comparison.
func ParseReviewedJUnitZIP(archive []byte, review ReviewedJUnitZIP) (Report, error) {
	fail := func() (Report, error) { return Report{}, ErrJUnitZIP }
	// Use the same reviewed artifact/attempt throughout, without inferring
	// a repository, job, shard or retry from the archive or its entry name.
	review.Binding = cloneBinding(review.Binding)
	zero := [sha256.Size]byte{}
	if len(archive) == 0 || len(archive) > MaxJUnitZIPBytes || review.ArchiveBytes != int64(len(archive)) ||
		review.EntryBytes <= 0 || review.EntryBytes > MaxReportBytes ||
		review.ArchiveSHA256 == zero || review.EntrySHA256 == zero ||
		!validJUnitZIPName(review.EntryName) || !validBinding(review.Binding) {
		return fail()
	}
	// Hash and inspect the same bounded byte snapshot. The caller must not
	// mutate the input while it is being copied.
	archive = bytes.Clone(archive)
	digest := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(digest[:], review.ArchiveSHA256[:]) != 1 {
		return fail()
	}
	frames, valid := reviewedZIPFraming(archive)
	if !valid {
		return fail()
	}
	// Only the already bounded, structurally verified byte buffer reaches
	// the standard parser. Its decompressor and CRC validation stay in use.
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || z.Comment != "" || len(z.File) != len(frames) {
		return fail()
	}
	entry := selectReviewedZIPEntry(z.File, frames, review)
	if entry == nil {
		return fail()
	}
	reader, err := entry.Open()
	if err != nil {
		return fail()
	}
	// One extra byte forces EOF/checksum validation for an exact-length
	// entry and prevents an unexpectedly large decoded body from escaping.
	raw, readErr := io.ReadAll(io.LimitReader(reader, review.EntryBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) != review.EntryBytes {
		return fail()
	}
	digest = sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(digest[:], review.EntrySHA256[:]) != 1 {
		return fail()
	}
	report, err := ParseJUnit(raw, review.Binding)
	if err != nil {
		return fail()
	}
	return report, nil
}

func selectReviewedZIPEntry(files []*zip.File, frames []reviewedZIPMember, review ReviewedJUnitZIP) *zip.File {
	var selected *zip.File
	for i, file := range files {
		f := frames[i]
		h := &f.header
		offset, err := file.DataOffset()
		if err != nil || offset != int64(f.data) || file.NonUTF8 || file.Name != h.Name ||
			file.CreatorVersion != h.CreatorVersion || file.ReaderVersion != h.ReaderVersion ||
			file.ExternalAttrs != h.ExternalAttrs || file.Flags != h.Flags || file.Method != h.Method ||
			file.CRC32 != h.CRC32 || file.UncompressedSize64 != h.UncompressedSize64 ||
			file.CompressedSize64 != h.CompressedSize64 || !bytes.Equal(file.Extra, h.Extra) {
			return nil
		}
		if file.Name == review.EntryName { // no guessing, prefix lookup, or case folding
			if file.UncompressedSize64 != uint64(review.EntryBytes) {
				return nil
			}
			selected = file
		}
	}
	return selected
}

func plainJUnitZIPMember(f *zip.FileHeader) bool {
	// Plain Store/Deflate only. Permit compression-level bits (Deflate),
	// a data descriptor and UTF-8; refuse encryption and other extensions.
	const flags = 0x080e
	mode := f.Mode()
	if f.Flags & ^uint16(flags) != 0 || f.NonUTF8 || f.Comment != "" ||
		!mode.IsRegular() || mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || !plainJUnitZIPExtra(f.Extra) ||
		f.UncompressedSize64 == 0 || f.UncompressedSize64 > MaxReportBytes || f.CompressedSize64 == 0 {
		return false
	}
	switch f.CreatorVersion >> 8 {
	case 0: // MS-DOS, no Unix type or other extension attributes
		if f.ExternalAttrs & ^uint32(0x27) != 0 {
			return false
		}
	case 3, 19: // Unix or macOS regular type with plain DOS attributes
		if f.ExternalAttrs>>16&0xf000 != 0x8000 || f.ExternalAttrs&0xffd8 != 0 {
			return false
		}
	default:
		return false
	}
	switch f.Method {
	case zip.Store:
		return (f.ReaderVersion == 20 || f.ReaderVersion == 10 && f.Flags&0x08 == 0) &&
			f.Flags&0x06 == 0 && f.CompressedSize64 == f.UncompressedSize64
	case zip.Deflate:
		return f.ReaderVersion == 20
	}
	return false
}

func plainJUnitZIPExtra(extra []byte) bool {
	// Only the standard single extended modification timestamp the Go
	// writer emits is accepted. Link/privilege/ZIP64 metadata are unsupported.
	return len(extra) == 0 ||
		(len(extra) == 9 && extra[0] == 0x55 && extra[1] == 0x54 && extra[2] == 5 && extra[3] == 0 && extra[4] == 1)
}

func validJUnitZIPName(name string) bool {
	if len(name) == 0 || len(name) > maxJUnitZIPEntryName || !strings.HasSuffix(name, ".xml") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if len(segment) == 0 || len(segment) > 64 || !junitZIPAlnum(segment[0]) || segment[len(segment)-1] == '.' {
			return false
		}
		for i := 1; i < len(segment); i++ {
			b := segment[i]
			if !junitZIPAlnum(b) && b != '.' && b != '_' && b != '-' {
				return false
			}
		}
		base, _, _ := strings.Cut(strings.ToUpper(segment), ".")
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	return true
}

func junitZIPAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
