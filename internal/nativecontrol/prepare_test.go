package nativecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
)

func prepareFixture(t *testing.T) (nativeapi.Policy, admission.NativeExecutionEnvelope, string) {
	t.Helper()
	p, err := nativeapi.NewPolicy(nativeapi.PolicySpec{
		Version: nativeapi.PolicyVersion, Controller: "controller", Host: "host", DomainID: strings.Repeat("a", 64),
		NativeSHA256: strings.Repeat("b", 64), LauncherSHA256: strings.Repeat("c", 64), ServerVersion: nativeapi.NativeVersion,
		Pools: map[string]nativeapi.Pool{"pool": {Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", ExpectedProfiles: []string{"legacy"}, StoragePool: "storage", Bridge: "bridge"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("controller\x00pool\x00runner"))
	e := admission.NativeExecutionEnvelope{Version: "native-execution/v1", ReconciliationEpoch: 1, PermitID: strings.Repeat("1", 32), RequestKey: hex.EncodeToString(key[:]), DomainBindingSequence: 1, HostBindingSequence: 2,
		DomainID: p.DomainID(), PolicyDigest: p.Digest(), Controller: "controller", Pool: "pool", Host: "host", NativeName: "runner", PlanDigest: strings.Repeat("e", 64), Operation: "create_and_start",
		Resources: admission.Resources{CPU: 2000, RAM: 4096, Disk: 8192, Runners: 1},
		Execution: admission.ExecutionImage{Controller: "controller", Name: "runner", Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", Profiles: []string{"legacy"}}}
	data := "#cloud-config\nruncmd: []\n"
	body, err := p.Compile(nativeapi.Allocation{Controller: "controller", Host: "host", Pool: "pool", Name: "runner", RequestKey: e.RequestKey, CPU: 2000, RAM: 4096, Disk: 8192, Runners: 1, Flavor: "small", Image: "runner", Fingerprint: strings.Repeat("d", 64), Type: "container", Arch: "amd64", Profiles: []string{"legacy"}}, data)
	if err != nil {
		t.Fatal(err)
	}
	e.BodyDigest = body.Digest()
	return p, e, data
}
func TestPrepareExactImmutableEnvelopeProducesNoInheritedProfiles(t *testing.T) {
	p, e, data := prepareFixture(t)
	prepared, err := Prepare(p, e, data)
	if err != nil || prepared.Digest() != e.BodyDigest {
		t.Fatalf("prepare %v", err)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(prepared.Body(), &body) != nil || string(body["profiles"]) != "[]" {
		t.Fatal("mutable native inheritance")
	}
	// Editing caller profile storage after compilation cannot alter the body.
	before := string(prepared.Body())
	e.Execution.Profiles[0] = "changed"
	if string(prepared.Body()) != before {
		t.Fatal("caller mutated compiled body")
	}
}
func TestPrepareRejectsChangedCanonicalFieldsAndBody(t *testing.T) {
	changes := []func(*admission.NativeExecutionEnvelope){
		func(e *admission.NativeExecutionEnvelope) { e.Version = "other" },
		func(e *admission.NativeExecutionEnvelope) { e.Operation = "delete" },
		func(e *admission.NativeExecutionEnvelope) { e.PermitID = "bad" },
		func(e *admission.NativeExecutionEnvelope) { e.DomainBindingSequence = 0 },
		func(e *admission.NativeExecutionEnvelope) { e.ReconciliationEpoch = 0 },
		func(e *admission.NativeExecutionEnvelope) { e.HostBindingSequence = 0 },
		func(e *admission.NativeExecutionEnvelope) { e.DomainID = strings.Repeat("f", 64) },
		func(e *admission.NativeExecutionEnvelope) { e.PolicyDigest = strings.Repeat("f", 64) },
		func(e *admission.NativeExecutionEnvelope) { e.Host = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.Controller = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.Pool = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.NativeName = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.Execution.Name = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.Execution.Controller = "another" },
		func(e *admission.NativeExecutionEnvelope) { e.Execution.Profiles = []string{"default"} },
		func(e *admission.NativeExecutionEnvelope) { e.Execution.Flavor = "other" },
		func(e *admission.NativeExecutionEnvelope) { e.Execution.Fingerprint = strings.Repeat("f", 64) },
		func(e *admission.NativeExecutionEnvelope) { e.Resources.CPU = 4000 },
		func(e *admission.NativeExecutionEnvelope) { e.Resources.RAM = 8192 },
		func(e *admission.NativeExecutionEnvelope) { e.Resources.Disk = 16384 },
		func(e *admission.NativeExecutionEnvelope) { e.Resources.Runners = 2 },
		func(e *admission.NativeExecutionEnvelope) { e.RequestKey = strings.Repeat("f", 64) },
		func(e *admission.NativeExecutionEnvelope) { e.PlanDigest = "bad" },
		func(e *admission.NativeExecutionEnvelope) { e.BodyDigest = strings.Repeat("f", 64) },
	}
	for i, change := range changes {
		p, e, data := prepareFixture(t)
		change(&e)
		if _, err := Prepare(p, e, data); err != ErrControl {
			t.Fatalf("mutation %d accepted %v", i, err)
		}
	}
	p, e, data := prepareFixture(t)
	if _, err := Prepare(p, e, data+"changed"); err != ErrControl {
		t.Fatal("changed userdata accepted")
	}
}
