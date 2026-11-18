package nativecontrol

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

func (o *executionOwner) protectedJournal(ctx context.Context, permit string) (nativeidentity.ProtectedJournalRecord, error) {
	o.mu.Lock()
	execution := o.permits[permit]
	o.mu.Unlock()
	if execution == nil {
		return nativeidentity.ProtectedJournalRecord{}, ErrControl
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	if execution.start == nil || execution.start.current.Outcome != "succeeded" || execution.bootstrapRecord.Outcome != "verified" {
		return nativeidentity.ProtectedJournalRecord{}, ErrControl
	}
	env := execution.parent.Envelope
	resource := execution.start.current.ResourceID
	journal, e := o.api.ObserveProtectedJournal(ctx, execution.prepared, resource)
	if e != nil || !journal.Valid() {
		return nativeidentity.ProtectedJournalRecord{}, ErrControl
	}
	r := journal.Record()
	return nativeidentity.ProtectedJournalRecord{PermitID: permit, EnvelopeDigest: execution.parent.EnvelopeDigest, DomainID: env.DomainID, PolicyDigest: env.PolicyDigest, NativeName: env.NativeName, ResourceID: resource, ImageFingerprint: env.Execution.Fingerprint, InitSHA256: r.LauncherSHA256, State: r.State, RecordAt: r.At, ExitCode: r.ExitCode}, nil
}
