package gatewayrpc

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/profilesnapshot"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProfileSnapshotActualMTLSRoleNonceAndNoMutation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	budget := admission.Resources{CPU: 2000, RAM: 4096, Runners: 2}
	config := admission.Config{Global: budget, Hosts: map[string]admission.Host{"host": {Budget: budget, Mode: "enabled", ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool": {Budget: budget, Hosts: []string{"host"}}}, MaxObservationAge: time.Minute}
	db := filepath.Join(dir, "ledger.db")
	identity := filepath.Join(dir, "identity")
	if err := admission.InitializeSQLite(db, identity, config); err != nil {
		t.Fatal(err)
	}
	store, err := admission.OpenRuntimeSQLite(db, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var handler http.Handler
	server, credentials, other := rpcTLSRolesFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	sum := sha256.Sum256(credentials.Certificates[0].Certificate[0])
	controller := "11111111-1111-4111-8111-111111111111"
	handler, err = NewProfileSnapshotHandler(http.NotFoundHandler(), store, controller, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadBrokerGrantState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	call := func(trust *tls.Config, method, path, n string) (int, []byte, error) {
		transport := &http.Transport{TLSClientConfig: trust, Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: time.Second}
		r, _ := http.NewRequest(method, server.URL+path, nil)
		r.Header.Set(profilesnapshot.NonceHeader, n)
		res, e := client.Do(r)
		if e != nil {
			return 0, nil, e
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		return res.StatusCode, b, e
	}
	code, body, err := call(credentials, "GET", profilesnapshot.Path, nonce)
	if err != nil || code != 200 {
		t.Fatalf("%d %s %v", code, body, err)
	}
	snap, err := profilesnapshot.Decode(body, controller, nonce, time.Now())
	if err != nil || snap.Lineage != store.RuntimeLineageFingerprint() || snap.Sequence != before.Sequence {
		t.Fatal("actual snapshot", err)
	}
	if snap.Accounting.Sequence != before.Sequence || snap.Accounting.Policy.Revision != before.Policy.Revision || snap.Accounting.Budgets.Global != budget || snap.Accounting.Budgets.Pools["pool"] != budget || snap.Accounting.Allocated.Global != before.Allocated.Global || snap.Accounting.UnusedGrantCounts["pool"] != 0 {
		t.Fatal("accounting not from actual coherent ledger")
	}
	if _, err := profilesnapshot.Decode(body, controller, strings.Repeat("b", 64), time.Now()); err == nil {
		t.Fatal("replayed nonce")
	}
	for _, v := range []struct {
		trust               *tls.Config
		method, path, nonce string
		want                int
	}{
		{other(9), "GET", profilesnapshot.Path, nonce, 403},
		{credentials, "POST", profilesnapshot.Path, nonce, 403},
		{credentials, "GET", profilesnapshot.Path + "?other=1", nonce, 403},
		{credentials, "GET", profilesnapshot.Path, "bad", 400},
	} {
		code, _, err := call(v.trust, v.method, v.path, v.nonce)
		if err != nil || code != v.want {
			t.Fatalf("wrong role/request %d %v", code, err)
		}
	}
	noClient := credentials.Clone()
	noClient.Certificates = nil
	if _, _, err := call(noClient, "GET", profilesnapshot.Path, nonce); err == nil {
		t.Fatal("missing TLS client accepted")
	}
	after, err := store.ReadBrokerGrantState(context.Background())
	if err != nil || after.Sequence != before.Sequence {
		t.Fatal("read endpoint changed ledger", err)
	}
	rawIdentity, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), strings.TrimSpace(string(rawIdentity))) {
		t.Fatal("private lineage file leaked")
	}
}
