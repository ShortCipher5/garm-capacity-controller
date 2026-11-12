package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIInitializeIsNewOnlyAndRuntimeUsesIndependentIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	budget := admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"a": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dir, "ledger.db")
	identity := filepath.Join(dir, "identity")
	args := []string{"initialize", "--destination", database, "--identity-file", identity, "--policy", policyPath}
	var out, diagnostic bytes.Buffer
	if code := run(context.Background(), args, &out, &diagnostic); code != 0 {
		t.Fatalf("initialize %d: %s", code, diagnostic.String())
	}
	store, err := admission.OpenRuntimeSQLite(database, identity)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Ready {
		t.Fatal("initialization enabled admission")
	}
	store.Close()
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	oldIdentity, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostic.Reset()
	if code := run(context.Background(), args, &out, &diagnostic); code == 0 {
		t.Fatal("initialized over existing state")
	}
	after, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	newIdentity, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(oldIdentity, newIdentity) {
		t.Fatal("repeat initialization modified identity/database")
	}
}

func TestCLIInitializeRejectsAmbiguousPolicyBeforeCreatingOutputs(t *testing.T) {
	for _, raw := range []string{
		`{"global":{"cpu_millis":1,"ram_mib":1,"runners":1},"global":{"cpu_millis":2,"ram_mib":2,"runners":2}}`,
		`{"Global":{"cpu_millis":1,"ram_mib":1,"runners":1}}`,
		`{"global":{"cpu_millis":1,"CPU_MILLIS":2,"ram_mib":1,"runners":1}}`,
		`{"hosts":{"a":{},"a":{}}}`,
	} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		policy := filepath.Join(dir, "policy.json")
		if err := os.WriteFile(policy, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		database, identity := filepath.Join(dir, "ledger.db"), filepath.Join(dir, "identity")
		var out, diagnostic bytes.Buffer
		code := run(context.Background(), []string{"initialize", "--destination", database, "--identity-file", identity, "--policy", policy}, &out, &diagnostic)
		if code != 2 {
			t.Fatalf("ambiguous policy wrong result: %d %s", code, diagnostic.String())
		}
		for _, name := range []string{database, identity} {
			if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Fatal("invalid policy created output", name, err)
			}
		}
	}
}
