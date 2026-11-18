package managedwriter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmmanagement"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func historyFixture() garmmanagement.Intent {
	p := json.RawMessage(`{"id":"11111111-1111-4111-8111-111111111111","provider_name":"capacity","max_runners":3,"min_idle_runners":1,"fingerprint":"` + strings.Repeat("a", 64) + `","secret":"never export","tags":["linux"]}`)
	return garmmanagement.Intent{RequestID: "22222222-2222-4222-8222-222222222222", Kind: "pool", Target: "11111111-1111-4111-8111-111111111111", State: "confirmed", Reason: "reduce", CreatedAt: "2026-11-18T00:00:00Z", Before: p, Desired: p, Result: p}
}
func TestHistoryProjectionBoundedSanitized(t *testing.T) {
	v := historyFixture()
	pools := map[string]bool{v.Target: true}
	providers := map[string]bool{"capacity": true}
	h, e := projectHistory([]garmmanagement.Intent{v}, pools, providers)
	if e != nil || len(h.Events) != 1 {
		t.Fatal(h, e)
	}
	raw, _ := json.Marshal(h)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "never export") {
		t.Fatal(string(raw))
	}
	rows := []garmmanagement.Intent{}
	for n := 0; n < 26; n++ {
		x := v
		x.RequestID = fmt.Sprintf("%08d-2222-4222-8222-222222222222", n)
		rows = append(rows, x)
	}
	h, e = projectHistory(rows, pools, providers)
	if e != nil || len(h.Events) != 25 || !h.Truncated {
		t.Fatal(h, e)
	}
	v.Result = nil
	if _, e = projectHistory([]garmmanagement.Intent{v}, pools, providers); e == nil {
		t.Fatal("confirmed without result")
	}
	v = historyFixture()
	h, e = projectHistory([]garmmanagement.Intent{v}, pools, map[string]bool{"other": true})
	if e != nil || len(h.Events) != 0 || !h.Truncated {
		t.Fatal(h, e)
	}
	if _, e = projectHistory([]garmmanagement.Intent{v}, map[string]bool{}, providers); e == nil {
		t.Fatal("foreign scope")
	}
}
func TestHistoryRequiresControllerAuthentication(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("GET", "/v1/managed/history", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestHistoryProjectsActualDurableJournalAfterTLSWrite(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			v := historyFixture()
			var mu sync.Mutex
			writes := 0
			maximum := uint64(3)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path != "/api/v1/pools/"+v.Target {
					w.WriteHeader(404)
					return
				}
				if r.Method == "PUT" {
					writes++
					maximum = 2
					if unknown {
						w.WriteHeader(503)
						return
					}
				} else if r.Method != "GET" {
					t.Error("unexpected method")
				}
				json.NewEncoder(w).Encode(map[string]any{"id": v.Target, "generation": 1, "provider_name": "capacity", "max_runners": maximum, "min_idle_runners": 1, "enabled": true, "tags": []any{map[string]any{"name": "linux"}}})
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			remote, e := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "fixture-only", Trust: &tls.Config{RootCAs: roots}, Providers: []string{"capacity"}, PoolIDs: []string{v.Target}, AllowWrites: true})
			if e != nil {
				t.Fatal(e)
			}
			defer remote.Close()
			dir := t.TempDir()
			if e = os.Chmod(dir, 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, "journal.db")
			j, e := garmmanagement.InitializeJournal(path)
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			lineage := j.Lineage()
			m, e := garmmanagement.New(remote, j)
			if e != nil {
				t.Fatal(e)
			}
			before, e := remote.Pool(context.Background(), v.Target)
			if e != nil {
				t.Fatal(e)
			}
			target := uint64(2)
			_, e = m.UpdatePool(context.Background(), v.RequestID, v.Target, before.Fingerprint, garmapi.PoolUpdate{MaxRunners: &target}, "reduce")
			if (e != nil) != unknown {
				t.Fatal(e)
			}
			j.Close()
			j, e = garmmanagement.OpenJournal(path, lineage)
			if e != nil {
				t.Fatal(e)
			}
			defer j.Close()
			m, e = garmmanagement.New(remote, j)
			if e != nil {
				t.Fatal(e)
			}
			rows, e := m.RecentScopedIntents(context.Background(), 26, []string{v.Target}, nil)
			if e != nil {
				t.Fatal(e)
			}
			h, e := projectHistory(rows, map[string]bool{v.Target: true}, map[string]bool{"capacity": true})
			if e != nil || len(h.Events) != 1 {
				t.Fatal(h, e)
			}
			want := "confirmed"
			if unknown {
				want = "unknown"
			}
			if h.Events[0].State != want {
				t.Fatal(h)
			}
			status, e := m.Status(context.Background())
			if e != nil || status.WriteBlocked != unknown {
				t.Fatal(status, e)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != 1 {
				t.Fatal(writes)
			}
		})
	}
}
