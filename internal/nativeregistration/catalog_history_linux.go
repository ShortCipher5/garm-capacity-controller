//go:build linux

package nativeregistration

import (
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativebundle"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeplanning"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"strings"
)

const maxCatalogRevisions = 16
const catalogVersion = "native-target-catalog/v1"

type catalogRecord struct {
	Version  string
	Sequence int
	Previous string
	Snapshot nativebundle.RegistrationCatalogSnapshot
}

func catalogFileLimit(name string) int64 {
	if strings.HasPrefix(name, "catalog-") {
		return 2 << 20
	}
	return 65536
}
func catalogName(n int) string { return fmt.Sprintf("catalog-%03d.json", n) }

// Called only under the registration mutex and cross-process journal flock.
// Every prior catalog must remain an exact subset of the current frozen inputs.
func (s *Service) readCatalogHistory(names []string) error {
	s.catalogs = map[string]*nativebundle.RegistrationCompiler{}
	s.catalogCount = 0
	s.catalogHead = ""
	found := map[string]bool{}
	for _, name := range names {
		if strings.HasPrefix(name, "catalog-") {
			found[name] = true
		}
	}
	if len(found) > maxCatalogRevisions {
		return ErrRegistration
	}
	var previous *nativebundle.RegistrationCompiler
	for n := 1; n <= len(found); n++ {
		name := catalogName(n)
		if !found[name] {
			return ErrRegistration
		}
		var record catalogRecord
		if s.read(name, &record) != nil || record.Version != catalogVersion || record.Sequence != n || record.Previous != s.catalogHead {
			return ErrRegistration
		}
		historical, e := s.compiler.Historical(record.Snapshot)
		if e != nil {
			return ErrRegistration
		}
		revision := historical.Revision()
		if s.catalogs[revision] != nil {
			return ErrRegistration
		}
		if previous != nil {
			if _, e = historical.Historical(previous.CatalogSnapshot()); e != nil {
				return ErrRegistration
			}
		}
		s.catalogs[revision] = historical
		s.catalogCount = n
		s.catalogHead = nativeplanning.Digest(record)
		previous = historical
	}
	return nil
}
func (s *Service) compileHistoricalPinned(r Request, t sshinventory.Target) (nativebundle.Registration, error) {
	if nativeplanning.Digest(t) != r.InventoryTargetDigest || t.ID != r.InventoryTargetID {
		return nativebundle.Registration{}, ErrRegistration
	}
	c := s.catalogs[r.Revision]
	if c == nil {
		// No guessing a historical catalog during legacy migration. Addition-first
		// requires restoring the original inputs before an exact checkpoint.
		if s.catalogCount != 0 || r.Revision != s.compiler.Revision() {
			return nativebundle.Registration{}, ErrRegistration
		}
		c = s.compiler
	}
	return c.Compile(nativebundle.RegistrationRequest{Revision: r.Revision, Template: r.Template, Identity: r.Identity, Administrator: r.Administrator, ArtifactReference: r.ArtifactReference, StorageReference: r.StorageReference, NetworkReference: r.NetworkReference, PreparationID: r.PreparationID, Host: r.Host, NativeTarget: r.NativeTarget, InventoryTarget: t})
}
func (s *Service) checkpointCatalog() error {
	if s.catalogs[s.compiler.Revision()] != nil {
		return nil
	}
	if s.catalogCount >= maxCatalogRevisions {
		return ErrRegistration
	}
	record := catalogRecord{catalogVersion, s.catalogCount + 1, s.catalogHead, s.compiler.CatalogSnapshot()}
	if record.Snapshot.Revision() != s.compiler.Revision() || s.write(catalogName(record.Sequence), record) != nil {
		return ErrRegistration
	}
	s.catalogs[s.compiler.Revision()] = s.compiler
	s.catalogCount = record.Sequence
	s.catalogHead = nativeplanning.Digest(record)
	return nil
}
