//go:build linux

package nativeassettransfer

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

const catalogVersion = "native-asset-catalog/v1"
const catalogPrefix = "asset-catalog-"

type assetCatalog struct {
	Version    string            `json:"version"`
	Identities map[string]string `json:"identities"`
}

func catalogDigest(entries map[string]string) string {
	raw, _ := json.Marshal(entries)
	return digest(raw)
}
func subset(old, current map[string]string) bool {
	for name, hash := range old {
		if current[name] != hash {
			return false
		}
	}
	return true
}
func catalogName(revision string) string { return catalogPrefix + revision + ".json" }

// catalogs verifies a bounded, strictly append-only chain. It stores only
// commitments, not private file paths, file contents, credentials or authority.
func (r *Registry) catalogs(names []string) (map[string]assetCatalog, error) {
	result := map[string]assetCatalog{}
	for _, name := range names {
		if !strings.HasPrefix(name, catalogPrefix) {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, ErrUnknown
		}
		revision := strings.TrimSuffix(strings.TrimPrefix(name, catalogPrefix), ".json")
		if !validHash(revision) {
			return nil, ErrUnknown
		}
		raw, e := r.journal.read(name)
		if e != nil {
			return nil, ErrUnknown
		}
		var snapshot assetCatalog
		if json.Unmarshal(raw, &snapshot) != nil {
			return nil, ErrUnknown
		}
		canon, _ := json.Marshal(snapshot)
		if !bytes.Equal(canon, raw) || snapshot.Version != catalogVersion || len(snapshot.Identities) < 1 || len(snapshot.Identities) > 8 || catalogDigest(snapshot.Identities) != revision || !subset(snapshot.Identities, r.identities) {
			return nil, ErrUnknown
		}
		for slot, identity := range snapshot.Identities {
			if !admission.ValidOnboardingProfileID(slot) || !validHash(identity) {
				return nil, ErrUnknown
			}
		}
		result[revision] = snapshot
	}
	if len(result) > 8 {
		return nil, ErrUnknown
	}
	ordered := make([]assetCatalog, 0, len(result))
	for _, s := range result {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i].Identities) < len(ordered[j].Identities) })
	for i := 1; i < len(ordered); i++ {
		if len(ordered[i-1].Identities) >= len(ordered[i].Identities) || !subset(ordered[i-1].Identities, ordered[i].Identities) {
			return nil, ErrUnknown
		}
	}
	return result, nil
}

// AnchorCatalog preserves an already deployment-approved catalog locally. It
// never registers a target, resumes an attempt, or changes a prior reservation.
// Legacy records can be anchored only while their exact original catalog is
// still configured; adding entries first cannot invent the missing baseline.
func (r *Registry) AnchorCatalog() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, e := r.journal.lock()
	if e != nil {
		return e
	}
	defer lock.Close()
	return r.anchorCatalog()
}
func (r *Registry) anchorCatalog() error {
	if r.validateRecords() != nil {
		return ErrUnknown
	}
	raw, _ := json.Marshal(assetCatalog{catalogVersion, r.identities})
	existing, e := r.journal.read(catalogName(r.revision))
	if e == nil {
		if !bytes.Equal(existing, raw) {
			return ErrUnknown
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return ErrUnknown
	}
	if r.journal.write(catalogName(r.revision), raw) != nil {
		return ErrUnknown
	}
	return nil
}
