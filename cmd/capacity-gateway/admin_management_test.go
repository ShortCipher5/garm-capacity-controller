package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func TestAdminManagementRequiresExplicitOptInAndIndependentPrivateToken(t *testing.T) {
	keys := credentials(t)
	dir := t.TempDir()
	budget := admission.Resources{CPU: 1, RAM: 1, Runners: 1}
	policy := admission.Config{Global: budget, Hosts: map[string]admission.Host{"a": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"a"}}}, MaxObservationAge: time.Minute}
	store, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), policy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	readPath, writePath := filepath.Join(dir, "read.token"), filepath.Join(dir, "write.token")
	for path, token := range map[string]string{readPath: strings.Repeat("a", 64), writePath: strings.Repeat("b", 64)} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := &adminConfiguration{Listen: "127.0.0.1:0", TLS: keys.serverFiles, TokenFile: readPath, ManagementTokenFile: writePath}
	if _, close, err := newAdminServer(config, store, false); err == nil {
		close()
		t.Fatal("token file implicitly enabled management")
	}
	config.EnableExperimentalManagement = true
	server, close, err := newAdminServer(config, store, false)
	if err != nil {
		t.Fatal(err)
	}
	close()
	server.Close()
	config.ManagementTokenFile = readPath
	if _, close, err := newAdminServer(config, store, false); err == nil {
		close()
		t.Fatal("read token gained management role")
	}
	config.ManagementTokenFile = ""
	if _, close, err := newAdminServer(config, store, false); err == nil {
		close()
		t.Fatal("flag without independent token accepted")
	}
	if _, err := store.ReadSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
}
