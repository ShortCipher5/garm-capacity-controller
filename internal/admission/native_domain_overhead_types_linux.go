package admission

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"time"
)

// NativeDomainOverhead is persistent conservative accounting, never permission
// to release a guest. Whole-capsule ceilings may double-count active guests;
// they must not be reduced using measured usage or reservation subtraction.
type NativeDomainOverhead struct {
	Sequence              int64                        `json:"sequence"`
	DomainBindingSequence int64                        `json:"domain_binding_sequence"`
	Host                  string                       `json:"host"`
	DomainID              string                       `json:"domain_id"`
	CapsuleBirthID        string                       `json:"capsule_birth_id"`
	Resources             Resources                    `json:"resources"`
	Evidence              nativeenforcement.Descriptor `json:"evidence"`
	RecordedAt            time.Time                    `json:"recorded_at"`
}

// NativeDomainOverheadDigest is a canonical integrity digest, not authentication.
func NativeDomainOverheadDigest(v NativeDomainOverhead) (string, error) {
	_, digest, e := nativeJSON(v)
	return digest, e
}
