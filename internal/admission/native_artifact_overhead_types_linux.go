package admission

import (
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"time"
)

// NativeArtifactOverhead is a detached projection of a permanent host-only
// artifact-root charge. This type is not evidence and cannot authorize a write.
// Only the forthcoming opaque authenticated artifact observation may originate
// a canonical record. No artifact or guest release is implied by this projection.
type NativeArtifactOverhead struct {
	ArtifactID            string                    `json:"artifact_id"`
	Nonce                 string                    `json:"nonce"`
	IssuedAt              time.Time                 `json:"issued_at"`
	Sequence              int64                     `json:"sequence"`
	DomainBindingSequence int64                     `json:"domain_binding_sequence"`
	Host                  string                    `json:"host"`
	Owner                 nativeidentity.Descriptor `json:"owner"`
	PoolGUID              string                    `json:"pool_guid"`
	PoolLoadGUID          string                    `json:"pool_load_guid"`
	RootGUID              string                    `json:"root_guid"`
	Dataset               string                    `json:"dataset"`
	QuotaBytes            uint64                    `json:"quota_bytes"`
	Resources             Resources                 `json:"resources"`
	ObservedAt            time.Time                 `json:"observed_at"`
	RecordedAt            time.Time                 `json:"recorded_at"`
}
