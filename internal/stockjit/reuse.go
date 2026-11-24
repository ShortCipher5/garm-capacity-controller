package stockjit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ShortCipher5/garm-capacity-controller/internal/reuse"
)

// ReuseBinding binds this authenticated registration record into the verified
// reuse contract. The digest covers controller, pool, name, runner ID,
// repository URL and metadata origin, so a registration observed through any
// other source or scope never validates against it.
func (r Record) ReuseBinding() (reuse.StockBinding, error) {
	if !r.valid() {
		return reuse.StockBinding{}, reuse.ErrTeardownEvidence
	}
	encoding, err := json.Marshal(struct {
		ControllerID, PoolID, Name, GitHubURL, MetadataOriginDigest string
		RunnerID                                                    int64
	}{r.ControllerID, r.PoolID, r.Name, r.GitHubURL, r.MetadataOriginDigest, r.RunnerID})
	if err != nil {
		return reuse.StockBinding{}, reuse.ErrTeardownEvidence
	}
	sum := sha256.Sum256(encoding)
	return reuse.StockBinding{
		RegistrationDigest: hex.EncodeToString(sum[:]),
		ControllerID:       r.ControllerID,
		PoolID:             r.PoolID,
		GitHubURL:          r.GitHubURL,
		RunnerName:         r.Name,
		RunnerID:           r.RunnerID,
	}, nil
}

func (r Record) valid() bool {
	if r.ControllerID == "" || r.PoolID == "" || r.Name == "" || r.GitHubURL == "" || r.MetadataOriginDigest == "" || r.RunnerID <= 0 {
		return false
	}
	for _, value := range []string{r.ControllerID, r.PoolID, r.Name, r.GitHubURL, r.MetadataOriginDigest} {
		if len(value) > 256 {
			return false
		}
		for _, b := range []byte(value) {
			if b < 33 || b == 127 {
				return false
			}
		}
	}
	decoded, err := hex.DecodeString(r.MetadataOriginDigest)
	return err == nil && len(decoded) == 32
}
