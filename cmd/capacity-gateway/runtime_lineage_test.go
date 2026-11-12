package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initializeRuntimeFixture(t *testing.T, config *configuration) {
	t.Helper()
	if err := os.Chmod(filepath.Dir(config.Database), 0700); err != nil {
		t.Fatal(err)
	}
	config.IdentityFile = config.Database + ".identity"
	if err := admission.InitializeSQLite(config.Database, config.IdentityFile, config.InitialPolicy); err != nil {
		t.Fatal(err)
	}
}

func TestServiceBothModesRejectMissingRuntimeStateWithoutCreatingIt(t *testing.T) {
	for _, provision := range []bool{false, true} {
		for _, mode := range []string{"missing", "empty", "offline", "wrong-identity", "archive"} {
			t.Run(fmt.Sprintf("%v/%s", provision, mode), func(t *testing.T) {
				directory := t.TempDir()
				if err := os.Chmod(directory, 0700); err != nil {
					t.Fatal(err)
				}
				database := filepath.Join(directory, "ledger.db")
				identity := filepath.Join(directory, "identity")
				policy := admission.Config{Global: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, Hosts: map[string]admission.Host{"a": {Budget: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"a": {Budget: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
				if mode == "missing" || mode == "empty" {
					if err := os.WriteFile(identity, []byte(strings.Repeat("a", 64)+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if mode == "empty" {
						if err := os.WriteFile(database, nil, 0600); err != nil {
							t.Fatal(err)
						}
					}
				} else if mode == "offline" {
					store, err := admission.OpenSQLite(database, policy)
					if err != nil {
						t.Fatal(err)
					}
					store.Close()
					if err := os.WriteFile(identity, []byte(strings.Repeat("a", 64)+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := admission.InitializeSQLite(database, identity, policy); err != nil {
						t.Fatal(err)
					}
					if mode == "wrong-identity" {
						if err := os.WriteFile(identity, []byte(strings.Repeat("f", 64)+"\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "archive" {
						copy := filepath.Join(directory, "backup.db")
						if err := admission.BackupSQLiteFile(context.Background(), database, copy); err != nil {
							t.Fatal(err)
						}
						database = copy
					}
				}
				before, readErr := os.ReadFile(database)
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				config := configuration{Database: database, IdentityFile: identity}
				if provision {
					config.BootstrapMode = "gateway-owned-default-cloudinit/v1"
					config.SourceFingerprints = []string{strings.Repeat("c", 64)}
				}
				err = serveOnListener(context.Background(), config, provision, listener)
				if !errors.Is(err, admission.ErrRuntimeLineage) {
					t.Fatalf("service did not reject lineage first: %v", err)
				}
				after, afterErr := os.ReadFile(database)
				if !bytes.Equal(before, after) || errors.Is(readErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
					t.Fatal("service changed/recreated rejected database")
				}
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Stat(database + suffix); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("service created rejection sidecar", suffix, err)
					}
				}
			})
		}
	}
}
