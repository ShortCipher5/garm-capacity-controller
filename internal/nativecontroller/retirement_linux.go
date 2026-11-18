//go:build linux

package nativecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

// RetirementResult is native operation history, never physical absence or a
// capacity refund. Successful DELETE still leaves the original reservation charged.
type RetirementResult struct {
	PermitID          string
	StopState         string
	DeleteState       string
	ReleaseAuthorized bool
}

// Retire joins an existing terminal-job/root-fence receipt to one-use native
// STOP and DELETE. Restart recovery reads canonical claims and owner history; it
// never resubmits a step after a claim exists, even if its HTTP response was lost.
// Production runtime authority remains refused independently by the ledger.
func (c *Coordinator) Retire(ctx context.Context, permit string) (RetirementResult, error) {
	result := RetirementResult{PermitID: permit}
	if c == nil || c.store == nil {
		return result, ErrConfiguration
	}
	if c.store.RuntimeLineageBound() {
		return result, ErrUnqualified
	}
	intent, _, e := c.store.LookupNativeRetirement(ctx, permit)
	if errors.Is(e, sql.ErrNoRows) {
		observation, oe := nativeidentity.ObserveProtectedJournal(ctx, c.owner, permit)
		if oe != nil {
			return result, ErrUnknown
		}
		intent, e = c.store.RequestNativeRetirement(ctx, permit, observation, c.policy)
	}
	if e != nil || intent.DomainID != c.owner.DomainID || intent.PolicyDigest != c.policy.Digest() || intent.PermitID != permit {
		return result, ErrUnknown
	}
	result.StopState, e = c.retireStep(ctx, intent, admission.NativeRetireStop)
	if e != nil {
		return result, e
	}
	result.DeleteState, e = c.retireStep(ctx, intent, admission.NativeRetireDelete)
	return result, e
}

func (c *Coordinator) retireStep(ctx context.Context, intent admission.NativeRetirementIntent, step string) (string, error) {
	claim, e := c.store.LookupNativeRetirementClaim(ctx, intent.PermitID, step)
	if errors.Is(e, sql.ErrNoRows) {
		body, be := json.Marshal(intent)
		if be != nil {
			return "unknown", ErrUnknown
		}
		// A lost response is not permission to POST again. The canonical owner claim
		// may already exist; read it even when the HTTP exchange failed.
		id, postErr := c.post(ctx, "/v1/native/executions/"+intent.PermitID+"/retire-"+step, body, "retirement_claim_id")
		claim, e = c.store.LookupNativeRetirementClaim(ctx, intent.PermitID, step)
		if e != nil || (postErr == nil && id != claim.ID) {
			return "unknown", ErrUnknown
		}
	}
	if e != nil {
		return "unknown", ErrUnknown
	}
	for _, kind := range []string{"succeeded", "failed", "unknown"} {
		_, e := c.store.LookupNativeRetirementReceipt(ctx, claim.ID, kind)
		if e == nil {
			if kind == "succeeded" {
				return kind, nil
			}
			return kind, ErrUnknown
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return "unknown", ErrUnknown
		}
	}
	savedAccepted, acceptedErr := c.store.LookupNativeRetirementReceipt(ctx, claim.ID, "accepted")
	haveAccepted := acceptedErr == nil
	if acceptedErr != nil && !errors.Is(acceptedErr, sql.ErrNoRows) {
		return "unknown", ErrUnknown
	}
	wait, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	for {
		// Preserve the immutable accepted operation before any successful terminal
		// receipt. An actual unknown terminal may exist without accepted ACK.
		accepted, ae := nativeidentity.ObserveRetirementOperation(wait, c.owner, claim.ID)
		if ae == nil {
			if haveAccepted {
				if accepted.Record() != savedAccepted.Record {
					return "unknown", ErrUnknown
				}
			} else {
				if e := c.store.RecordNativeRetirementOperation(wait, accepted); e != nil {
					return "unknown", ErrUnknown
				}
				savedAccepted.Record = accepted.Record()
				haveAccepted = true
			}
		}
		terminal, te := nativeidentity.ObserveRetirementTerminal(wait, c.owner, claim.ID)
		if te == nil {
			if e := c.store.RecordNativeRetirementOperation(wait, terminal); e != nil {
				return "unknown", ErrUnknown
			}
			state := terminal.Record().Outcome
			if state == "succeeded" {
				return state, nil
			}
			return state, ErrUnknown
		}
		// Polling is read-only. Transport failure, pending, malformed or absent
		// observations cannot authorize another mutation or a capacity release.
		timer := time.NewTimer(c.poll)
		select {
		case <-wait.Done():
			timer.Stop()
			return "unknown", ErrUnknown
		case <-timer.C:
		}
	}
}
