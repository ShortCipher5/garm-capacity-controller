package garmapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagementInvalidUTF8ResponseRefused(t *testing.T) {
	const good = `{"id":"00000000-0000-4000-8000-000000000001","provider_name":"capacity","generation":1,"max_runners":4,"min_idle_runners":1,"tags":[{"name":"linux"}]}`
	for _, phase := range []string{"before", "put", "after"} {
		t.Run(phase, func(t *testing.T) {
			var puts atomic.Int32
			bad := strings.Replace(good, "linux", "bad\xff", 1)
			server, trust := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts.Add(1)
				}
				body := good
				if phase == "before" || (phase == "put" && r.Method == http.MethodPut) || (phase == "after" && puts.Load() > 0 && r.Method == http.MethodGet) {
					body = bad
				}
				_, _ = w.Write([]byte(body))
			}))
			c, err := NewManagement(ManagementConfig{Endpoint: server.URL, Token: "fixture", Trust: trust, Providers: []string{"capacity"}, PoolIDs: []string{managedPoolID}, AllowWrites: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			expected := "unused"
			if phase != "before" {
				p, e := c.Pool(context.Background(), managedPoolID)
				if e != nil {
					t.Fatal(e)
				}
				expected = p.Fingerprint
			}
			_, err = c.UpdatePool(context.Background(), managedPoolID, expected, PoolUpdate{MaxRunners: value(uint64(4))})
			want := ErrManagementOutcomeUnknown
			count := int32(1)
			if phase == "before" {
				want = ErrManagement
				count = 0
			}
			if !errors.Is(err, want) || puts.Load() != count {
				t.Fatalf("error=%v puts=%d", err, puts.Load())
			}
		})
	}
}

func TestManagementValidUnicodePreserved(t *testing.T) {
	m, f := managementServer(t, false)
	defer m.Close()
	f.mu.Lock()
	f.pool["tags"] = []any{map[string]any{"name": "日本語-\ufffd"}}
	f.mu.Unlock()
	p, err := m.Pool(context.Background(), managedPoolID)
	if err != nil || len(p.Tags) != 1 || p.Tags[0] != "日本語-\ufffd" {
		t.Fatalf("valid Unicode rejected: %v", err)
	}
}
