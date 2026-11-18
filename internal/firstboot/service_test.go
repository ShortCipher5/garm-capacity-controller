package firstboot

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu   sync.Mutex
	op   *Operation
	fail bool
}

func (m *memoryStore) FirstbootOperations(context.Context) (map[string]Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Operation{}
	if m.op != nil {
		out[m.op.TargetID] = *m.op
	}
	return out, nil
}
func (m *memoryStore) BeginFirstboot(_ context.Context, id, digest, request string, retry bool) (Operation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return Operation{}, false, ErrState
	}
	if m.op != nil {
		if m.op.Digest != digest || m.op.RequestID != request {
			return Operation{}, false, ErrState
		}
		if m.op.State == "installed" || !retry {
			return *m.op, false, nil
		}
		m.op.Attempts++
		return *m.op, true, nil
	}
	m.op = &Operation{TargetID: id, Digest: digest, RequestID: request, State: "unknown", Attempts: 1}
	return *m.op, true, nil
}
func (m *memoryStore) CompleteFirstboot(_ context.Context, _, _, _ string, receipt Receipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.op.State = "installed"
	m.op.Receipt = &receipt
	return nil
}
func TestServiceDurableUnknownAndExplicitRetry(t *testing.T) {
	r, key := fixture(t)
	store := &memoryStore{}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	calls := 0
	resolve := func(context.Context, string) (ssh.Signer, error) {
		ops, _ := store.FirstbootOperations(context.Background())
		if ops["one"].State != "unknown" {
			t.Error("credential before durable unknown")
		}
		return key, nil
	}
	s, e := newService(map[string]DeploymentTarget{"one": {Request: r, CredentialAlias: "admin"}}, store, resolve, func(context.Context, Request, ssh.Signer) (Receipt, error) {
		calls++
		entered <- struct{}{}
		<-release
		if calls == 1 {
			return Receipt{}, errors.New("lost response")
		}
		return Receipt{State: "installed", Digest: r.ReviewDigest, ListenAddress: r.Plan.ListenAddress, ListenPort: ListenerPort, InventoryAddress: r.Plan.InventoryAddress, InventoryPort: r.Plan.InventoryPort, BootEnabled: r.Plan.EnableAtBoot, HostKey: r.Plan.Base.HostKey}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	id := "11111111-1111-4111-8111-111111111111"
	op, e := s.Start(context.Background(), "one", r.ReviewDigest, id, false)
	if e != nil || op.State != "unknown" {
		t.Fatal(op, e)
	}
	<-entered
	if _, e = s.Start(context.Background(), "one", r.ReviewDigest, id, true); e == nil {
		t.Fatal("same-process concurrent retry")
	}
	release <- struct{}{}
	wait := func() {
		t.Helper()
		for i := 0; i < 100; i++ {
			v, _ := s.Targets(context.Background())
			if !v[0].Running {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("worker did not finish")
	}
	wait()
	if _, e = s.Start(context.Background(), "one", r.ReviewDigest, id, false); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("automatic retry")
	}
	if _, e = s.Start(context.Background(), "one", r.ReviewDigest, id, true); e != nil {
		t.Fatal(e)
	}
	<-entered
	release <- struct{}{}
	wait()
	v, _ := s.Targets(context.Background())
	if v[0].Operation.State != "installed" || calls != 2 {
		t.Fatal(v, calls)
	}
	if _, e = s.Start(context.Background(), "one", r.ReviewDigest, id, true); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("installed retried")
	}
}
func TestFailedDurableStartNeverResolvesCredential(t *testing.T) {
	r, _ := fixture(t)
	m := &memoryStore{fail: true}
	s, e := newService(map[string]DeploymentTarget{"one": {Request: r, CredentialAlias: "admin"}}, m, func(context.Context, string) (ssh.Signer, error) {
		t.Error("resolved without audit")
		return nil, ErrState
	}, func(context.Context, Request, ssh.Signer) (Receipt, error) {
		t.Error("executed")
		return Receipt{State: "installed", Digest: r.ReviewDigest, ListenAddress: r.Plan.ListenAddress, ListenPort: ListenerPort, InventoryAddress: r.Plan.InventoryAddress, InventoryPort: r.Plan.InventoryPort, BootEnabled: r.Plan.EnableAtBoot, HostKey: r.Plan.Base.HostKey}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Start(context.Background(), "one", r.ReviewDigest, "11111111-1111-4111-8111-111111111111", false); e == nil {
		t.Fatal("failed durable start accepted")
	}
}
