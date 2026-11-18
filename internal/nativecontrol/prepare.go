package nativecontrol

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
)

// Prepare maps a canonical envelope to the frozen local policy and verifies the
// exact native body before any owner claim. Success is only payload validation:
// the owner must still obtain a fresh canonical consume result before mutation.
// A PlanDigest is checked structurally here; only the shared ledger can validate
// it against the immutable complete execution plan (which includes creation time).
func Prepare(policy nativeapi.Policy, envelope admission.NativeExecutionEnvelope, userData string) (nativeapi.Prepared, error) {
	fail := func() (nativeapi.Prepared, error) { return nativeapi.Prepared{}, ErrControl }
	permit, err := hex.DecodeString(envelope.PermitID)
	if err != nil || len(permit) != 16 || hex.EncodeToString(permit) != envelope.PermitID ||
		envelope.Version != "native-execution/v1" || envelope.Operation != "create_and_start" ||
		envelope.ReconciliationEpoch <= 0 || envelope.DomainBindingSequence <= 0 || envelope.HostBindingSequence <= 0 ||
		!hash(envelope.RequestKey) || !hash(envelope.PlanDigest) || !hash(envelope.BodyDigest) ||
		!hash(policy.Digest()) || envelope.PolicyDigest != policy.Digest() ||
		envelope.DomainID != policy.DomainID() || envelope.Controller != policy.Controller() ||
		envelope.Host != policy.Host() || envelope.Execution.Controller != envelope.Controller ||
		envelope.Execution.Name != envelope.NativeName {
		return fail()
	}
	key := sha256.Sum256([]byte(envelope.Controller + "\x00" + envelope.Pool + "\x00" + envelope.NativeName))
	if envelope.RequestKey != hex.EncodeToString(key[:]) {
		return fail()
	}
	e, r := envelope.Execution, envelope.Resources
	allocation := nativeapi.Allocation{
		Controller: envelope.Controller, Host: envelope.Host, Pool: envelope.Pool, Name: envelope.NativeName, RequestKey: envelope.RequestKey,
		CPU: r.CPU, RAM: r.RAM, Disk: r.Disk, Runners: r.Runners,
		Flavor: e.Flavor, Image: e.Image, Fingerprint: e.Fingerprint, Type: e.Type, Arch: e.Arch,
		Profiles: append([]string(nil), e.Profiles...),
	}
	prepared, err := policy.Compile(allocation, userData)
	if err != nil || prepared.Digest() != envelope.BodyDigest {
		return fail()
	}
	return prepared, nil
}
