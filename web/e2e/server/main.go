// Disposable localhost-only browser test fixture. Never contacts infrastructure.
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func main() {
	dir, err := os.MkdirTemp("", "admin-browser-test-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	budget := admission.Resources{CPU: 4000, RAM: 8192, Runners: 4}
	now := time.Now()
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host-a": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: now}}, Pools: map[string]admission.Pool{"pool-a": {Budget: budget, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Hour}
	store, err := admission.OpenSQLite(filepath.Join(dir, "ledger.db"), config)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	fence, err := store.BeginReconciliation(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	now = time.Now()
	err = store.CompleteReconciliation(context.Background(), fence, admission.InventorySnapshot{Hosts: []admission.HostObservation{{Host: "host-a", ObservedAt: now, Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true}}, Resources: []admission.InventoryResource{{ID: "test-only-instance", Host: "host-a", Pool: "pool-a", Resources: admission.Resources{CPU: 1000, RAM: 1024, Runners: 1}, State: "unknown"}}})
	if err != nil {
		log.Fatal(err)
	}
	root, err := os.OpenRoot("dist")
	if err != nil {
		log.Fatal(err)
	}
	defer root.Close()
	handler, err := adminview.New(store, strings.Repeat("a", 64), root, false)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:4173")
	if err != nil {
		log.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	log.Print("Disposable browser fixture ready on https://127.0.0.1:4173")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()
}
