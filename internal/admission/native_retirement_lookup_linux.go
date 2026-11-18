//go:build linux

package admission

import (
	"context"
	"database/sql"
)

// LookupNativeRetirementClaim reads historical committed state for recovery after
// an uncertain HTTP response. It never grants fresh execution authority: owners
// must still obtain their own first-only ClaimNativeRetirement exchange.
func (s *SQLiteStore) LookupNativeRetirementClaim(ctx context.Context, permit, step string) (NativeRetirementClaim, error) {
	var out NativeRetirementClaim
	if step != NativeRetireStop && step != NativeRetireDelete {
		return out, ErrRetirementAuthority
	}
	e := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; out, e = nativeRetirementClaimByStep(tx, permit, step); return e })
	if e != nil {
		return NativeRetirementClaim{}, e
	}
	return out, nil
}

// LookupNativeRetirementReceipt returns immutable operation history, not current
// physical absence and not permission to replay STOP, DELETE or release capacity.
func (s *SQLiteStore) LookupNativeRetirementReceipt(ctx context.Context, claimID, kind string) (NativeRetirementReceipt, error) {
	var out NativeRetirementReceipt
	if kind != "accepted" && kind != "succeeded" && kind != "failed" && kind != "unknown" {
		return out, ErrRetirementAuthority
	}
	e := s.readTransaction(ctx, func(tx *sql.Tx) error { var e error; out, e = nativeRetirementReceipt(tx, claimID, kind); return e })
	if e != nil {
		return NativeRetirementReceipt{}, e
	}
	return out, nil
}
