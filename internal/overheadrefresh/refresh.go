// Package overheadrefresh records only real, pinned owner observations into the
// existing canonical ledger. It never reconciles, releases, grants or activates.
package overheadrefresh

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeartifacts"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeenforcement"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

var ErrRefresh = errors.New("owner overhead refresh incomplete; retain existing charges")

type Target struct {
	BindingSequence int64
	Endpoint        string
	TLS             *tls.Config
	Kind            string // domain, artifact or both; selects reads, never asserts readiness.
}
type Result struct {
	BindingSequence  int64 `json:"binding_sequence"`
	DomainSequence   int64 `json:"domain_sequence,omitempty"`
	ArtifactSequence int64 `json:"artifact_sequence,omitempty"`
}

// Refresh is serial and bounded. On any failure it returns the durable records
// already committed and an error. A partial pass is not a coherent authority
// snapshot; callers must read the ledger afresh. Retrying performs new real
// observations and cannot lower retained debt or invent missing enrollment.
func Refresh(ctx context.Context, store *admission.SQLiteStore, targets []Target) ([]Result, error) {
	if ctx == nil || ctx.Err() != nil || store == nil || len(targets) == 0 || len(targets) > 100 {
		return nil, ErrRefresh
	}
	seen := map[int64]bool{}
	owned := append([]Target(nil), targets...)
	for _, t := range owned {
		if t.BindingSequence <= 0 || seen[t.BindingSequence] || t.Endpoint == "" || t.TLS == nil || (t.Kind != "domain" && t.Kind != "artifact" && t.Kind != "both") {
			return nil, ErrRefresh
		}
		seen[t.BindingSequence] = true
	}
	out := make([]Result, 0, len(owned))
	for _, t := range owned {
		result, err := refreshTarget(ctx, store, t)
		if result.DomainSequence != 0 || result.ArtifactSequence != 0 {
			out = append(out, result)
		}
		if err != nil {
			return out, ErrRefresh
		}
	}
	return out, nil
}
func refreshTarget(ctx context.Context, store *admission.SQLiteStore, t Target) (Result, error) {
	out := Result{BindingSequence: t.BindingSequence}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	binding, err := store.LookupNativeDomainBinding(ctx, t.BindingSequence)
	if err != nil {
		return out, ErrRefresh
	}
	host, err := store.LookupHostBinding(ctx, binding.Spec.Host)
	if err != nil || host.Sequence != binding.Spec.HostBindingSequence {
		return out, ErrRefresh
	}
	// Full native identity is obtained from the authenticated owner, not config
	// JSON. The recording transaction independently rechecks enrollment, roles,
	// current host binding and native source identity before accepting any debt.
	observation, err := nativeidentity.Observe(ctx, nativeidentity.Configuration{Endpoint: t.Endpoint, TLS: t.TLS, OwnerServerLeaf: binding.Spec.OwnerServerLeaf, OwnerClientLeaf: binding.Spec.OwnerClientLeaf, PolicyDigest: binding.Spec.PolicyDigest, DomainID: binding.Spec.DomainID})
	if err != nil {
		return out, ErrRefresh
	}
	expected := observation.Record()
	if t.Kind == "artifact" || t.Kind == "both" {
		observed, e := nativeartifacts.Observe(ctx, nativeartifacts.Configuration{Endpoint: t.Endpoint, TLS: t.TLS, Expected: expected})
		if e != nil {
			return out, ErrRefresh
		}
		record, e := store.RecordNativeArtifactOverhead(ctx, t.BindingSequence, observed)
		if e != nil {
			return out, ErrRefresh
		}
		out.ArtifactSequence = record.Sequence
	}
	if t.Kind == "domain" || t.Kind == "both" {
		observed, e := nativeenforcement.Observe(ctx, nativeenforcement.Configuration{Endpoint: t.Endpoint, TLS: t.TLS, Expected: expected})
		if e != nil {
			return out, ErrRefresh
		}
		record, e := store.RecordNativeDomainOverhead(ctx, t.BindingSequence, observed)
		if e != nil {
			return out, ErrRefresh
		}
		out.DomainSequence = record.Sequence
	}
	return out, nil
}
