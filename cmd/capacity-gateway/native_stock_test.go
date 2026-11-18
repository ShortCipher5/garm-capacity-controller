package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
)

func TestNativeStockRuntimeRefusesBeforeCredentialReads(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	budget := admission.Resources{CPU: 1000, RAM: 1024, Disk: 4096, Runners: 1}
	policy := admission.Config{MaxObservationAge: time.Hour, Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}}
	db, id := filepath.Join(dir, "ledger.db"), filepath.Join(dir, "identity")
	if e := admission.InitializeSQLite(db, id, policy); e != nil {
		t.Fatal(e)
	}
	store, e := admission.OpenRuntimeSQLite(db, id)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	c := configuration{EnableStockProvider: true, NativeStock: map[string]nativeStockPoolConfiguration{"pool": {MetadataURL: "https://must-not-contact.invalid", Hosts: map[string]nativeStockHostConfiguration{"host": {OwnerEndpoint: "https://must-not-contact.invalid"}}}}}
	before, e := store.Snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	f, close, e := buildNativeStock(context.Background(), store, c, "must-not-transmit", nil)
	defer close()
	if e != gatewayrpc.ErrConfiguration || f != nil {
		t.Fatal("runtime activation was not refused", e)
	}
	after, e := store.Snapshot(context.Background())
	if e != nil || len(before.Audit) != len(after.Audit) {
		t.Fatal("blocked configuration changed ledger", e)
	}
}
func TestNativeStockSelectionHasNoMissingBridgeFallback(t *testing.T) {
	c := configuration{NativeStock: map[string]nativeStockPoolConfiguration{"pool": {}}}
	if h, e := stockProviderHandler(c, nil); e == nil || h != nil {
		t.Fatal("disabled native configuration silently ignored")
	}
	if f, close, e := buildNativeStock(context.Background(), nil, c, "", nil); e == nil || f != nil {
		close()
		t.Fatal("missing native store ignored")
	}
	if _, e := gatewayrpc.NewNativeStockHandler(nil, nil, nil); e == nil {
		t.Fatal("nil native facade accepted")
	}
}
