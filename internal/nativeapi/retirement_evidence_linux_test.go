//go:build linux

package nativeapi

import (
	"context"
	"testing"
	"time"
)

func TestRetirementEvidenceCannotBeMintedFromEmptyHandles(t *testing.T) {
	var c *Client
	if _, e := c.ObserveRetirementEvidence(context.Background(), Prepared{}, RetirementOperation{}, GuestIncarnation{}, StorageReclamationTracker{}); e == nil {
		t.Fatal("nil accepted")
	}
	c = &Client{}
	if _, e := c.ObserveRetirementEvidence(context.Background(), Prepared{}, RetirementOperation{}, GuestIncarnation{}, StorageReclamationTracker{}); e == nil {
		t.Fatal("unconfined accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.ObserveRetirementEvidence(ctx, Prepared{}, RetirementOperation{}, GuestIncarnation{}, StorageReclamationTracker{}); e == nil {
		t.Fatal("cancel accepted")
	}
}
func TestRetirementEvidenceFreshnessCannotReplaceActualProofs(t *testing.T) {
	for _, at := range []time.Time{{}, time.Now(), time.Now().Add(-6 * time.Second), time.Now().Add(time.Second)} {
		v := RetirementEvidence{began: at}
		if v.Fresh() || !v.ObservedAt().IsZero() || v.Namespace().Terminated() || v.Storage().Accounting().Fresh() || v.Deletion().Valid() {
			t.Fatal("timestamp supplied missing proof")
		}
	}
}
