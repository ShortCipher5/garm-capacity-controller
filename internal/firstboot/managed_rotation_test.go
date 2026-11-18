package firstboot

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"golang.org/x/crypto/ssh"
	"sync"
	"testing"
	"time"
)

type managedMemory struct {
	mu        sync.Mutex
	op        *ManagedRotationOperation
	failBegin bool
	failMark  string
}

func (m *managedMemory) ManagedRotations(context.Context) (map[string]ManagedRotationOperation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := map[string]ManagedRotationOperation{}
	if m.op != nil {
		v[m.op.TargetID] = *m.op
	}
	return v, nil
}
func (m *managedMemory) BeginManagedRotation(_ context.Context, id, digest, request string, retry bool) (ManagedRotationOperation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failBegin {
		return ManagedRotationOperation{}, false, ErrState
	}
	if m.op != nil {
		if m.op.Digest != digest || m.op.RequestID != request {
			return *m.op, false, ErrState
		}
		if m.op.State == "complete" || !retry {
			return *m.op, false, nil
		}
		m.op.Attempts++
		return *m.op, true, nil
	}
	m.op = &ManagedRotationOperation{TargetID: id, Digest: digest, RequestID: request, State: "unknown", Phase: "approved", Attempts: 1}
	return *m.op, true, nil
}
func (m *managedMemory) MarkManagedRotation(_ context.Context, _, _, _, phase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failMark == phase {
		return ErrState
	}
	r := map[string]int{"approved": 0, "staged": 1, "proved": 2, "registry_switched": 3, "committed": 4}
	if r[phase] > r[m.op.Phase] {
		m.op.Phase = phase
	}
	return nil
}
func (m *managedMemory) CompleteManagedRotation(_ context.Context, _, _, _ string, v RotationReceipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.op.State = "complete"
	m.op.Phase = "verified"
	m.op.Receipt = &v
	return nil
}

type managedRegistry struct{ replace func() error }

func (m *managedRegistry) Review(id string, c sshinventory.Config) (sshinventory.Target, error) {
	return sshinventory.Target{ID: id, ReviewDigest: sshinventory.ReviewDigest(id, c)}, nil
}
func (m *managedRegistry) ReplaceForRotation(context.Context, string, string, string, string, sshinventory.Config, string) (sshinventory.Target, error) {
	return sshinventory.Target{}, m.replace()
}
func managedFixture(t *testing.T) (ManagedRotationTarget, ssh.Signer, ssh.Signer) {
	r, admin, next := rotationFixture(t)
	old := sshinventory.Config{Address: r.Plan.Installation.InventoryAddress, Port: r.Plan.Installation.InventoryPort, User: "garm-inventory-listener", HostKey: r.Plan.ListenerHostKey, CredentialRef: "old-key"}
	new := old
	new.CredentialRef = "new-key"
	return ManagedRotationTarget{Plan: ManagedRotationPlan{Remote: r.Plan, AdminAlias: "admin", OldID: "old", OldConfig: old, NewID: "new", NewConfig: new}, Artifact: r.Artifact}, admin, next
}
func waitManaged(t *testing.T, s *ManagedRotationService) ManagedRotationOperation {
	t.Helper()
	for i := 0; i < 200; i++ {
		v, e := s.Targets(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !v[0].Running && v[0].Operation != nil {
			return *v[0].Operation
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker did not finish")
	return ManagedRotationOperation{}
}
func TestManagedRotationRegistrySwitchBeforeRemoteCommitAndNoCommitOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "registry-denied"}[fail], func(t *testing.T) {
			target, admin, next := managedFixture(t)
			store := &managedMemory{}
			steps := []string{}
			reg := &managedRegistry{replace: func() error {
				steps = append(steps, "registry")
				if fail {
					return ErrState
				}
				return nil
			}}
			resolver := func(k ssh.Signer) Resolver {
				return func(context.Context, string) (ssh.Signer, error) {
					ops, _ := store.ManagedRotations(context.Background())
					if ops["one"].State != "unknown" {
						t.Error("key before durable approval")
					}
					return k, nil
				}
			}
			s, e := newManagedRotationService(map[string]ManagedRotationTarget{"one": target}, store, reg, resolver(admin), resolver(next), func(_ context.Context, r RotationRequest, _ ssh.Signer) (RotationReceipt, error) {
				steps = append(steps, r.Phase)
				if r.Phase == "commit" && (len(steps) < 4 || steps[len(steps)-2] != "registry") {
					t.Error("removal before registry switch")
				}
				v := RotationReceipt{State: "staged", Digest: r.ReviewDigest, HostKey: r.Plan.ListenerHostKey}
				if r.Phase == "commit" {
					v.State = "revoked"
					v.OldKeyRemoved = true
					v.SessionsTerminated = true
				}
				return v, nil
			}, func(context.Context, sshinventory.Config, ssh.Signer) error {
				steps = append(steps, "probe")
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if _, e = s.Start(context.Background(), "one", ManagedRotationDigest(target.Plan), target.Plan.Remote.RequestID, false); e != nil {
				t.Fatal(e)
			}
			op := waitManaged(t, s)
			if fail {
				if op.State != "unknown" || op.Phase != "proved" || len(steps) != 3 {
					t.Fatal(op, steps)
				}
			} else {
				if op.State != "complete" || len(steps) != 6 {
					t.Fatal(op, steps)
				}
			}
		})
	}
}
func TestManagedRotationNoEffectWithoutDurableApproval(t *testing.T) {
	target, _, _ := managedFixture(t)
	store := &managedMemory{failBegin: true}
	never := func(context.Context, string) (ssh.Signer, error) {
		t.Fatal("credentials resolved")
		return nil, ErrState
	}
	s, e := newManagedRotationService(map[string]ManagedRotationTarget{"one": target}, store, &managedRegistry{}, never, never, func(context.Context, RotationRequest, ssh.Signer) (RotationReceipt, error) {
		t.Fatal("remote effect")
		return RotationReceipt{}, nil
	}, func(context.Context, sshinventory.Config, ssh.Signer) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Start(context.Background(), "one", ManagedRotationDigest(target.Plan), target.Plan.Remote.RequestID, false); e == nil {
		t.Fatal("approval failure ignored")
	}
}

func (m *managedRegistry) CheckRotationReplacement(context.Context, string, string, string, string, sshinventory.Config, string) error {
	return nil
}

func TestManagedRotationLostRegistryPhaseRetainsOldRemoteKeyUntilExplicitRetry(t *testing.T) {
	target, admin, next := managedFixture(t)
	store := &managedMemory{failMark: "registry_switched"}
	stages, commits, switches := 0, 0, 0
	switched := false
	reg := &managedRegistry{replace: func() error { switches++; switched = true; return nil }}
	s, e := newManagedRotationService(map[string]ManagedRotationTarget{"one": target}, store, reg, func(context.Context, string) (ssh.Signer, error) { return admin, nil }, func(context.Context, string) (ssh.Signer, error) { return next, nil }, func(_ context.Context, r RotationRequest, _ ssh.Signer) (RotationReceipt, error) {
		v := RotationReceipt{State: "staged", Digest: r.ReviewDigest, HostKey: r.Plan.ListenerHostKey}
		if r.Phase == "stage" {
			stages++
		} else {
			commits++
			if !switched {
				t.Error("revoked before switch")
			}
			v.State = "revoked"
			v.OldKeyRemoved = true
			v.SessionsTerminated = true
		}
		return v, nil
	}, func(context.Context, sshinventory.Config, ssh.Signer) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	digest := ManagedRotationDigest(target.Plan)
	request := target.Plan.Remote.RequestID
	if _, e = s.Start(context.Background(), "one", digest, request, false); e != nil {
		t.Fatal(e)
	}
	op := waitManaged(t, s)
	if op.Phase != "proved" || commits != 0 || switches != 1 {
		t.Fatal(op, commits, switches)
	}
	if _, e = s.Start(context.Background(), "one", digest, request, false); e != nil {
		t.Fatal(e)
	}
	if stages != 1 {
		t.Fatal("automatic retry")
	}
	store.mu.Lock()
	store.failMark = ""
	store.mu.Unlock()
	if _, e = s.Start(context.Background(), "one", digest, request, true); e != nil {
		t.Fatal(e)
	}
	op = waitManaged(t, s)
	if op.State != "complete" || commits != 1 || stages != 2 {
		t.Fatal(op, stages, commits)
	}
}
