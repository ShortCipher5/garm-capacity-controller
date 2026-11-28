package demandrepair

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstallationCatalogPinsTokenBoundsPagesAndFiltersPrivateData(t *testing.T) {
	var calls, loads int
	fault := ""
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/installation/repositories" || r.URL.Query().Get("per_page") != "100" || r.Header.Get("Authorization") != "Bearer mock-installation" {
			t.Error("unexpected request")
			return
		}
		rows := []map[string]any{}
		total := 101
		start, end := 1, 100
		if r.URL.Query().Get("page") == "2" {
			start, end = 101, 101
			if fault == "shrink" {
				total = 100
			}
			if fault == "duplicate" {
				start, end = 1, 1
			}
		}
		for i := start; i <= end; i++ {
			name := fmt.Sprintf("repo-%d", i)
			rows = append(rows, map[string]any{"id": i, "owner": map[string]any{"login": "owner"}, "name": name, "full_name": "owner/" + name, "private": true})
		}
		if fault == "oversized" {
			total = 1001
		}
		if fault == "short" {
			rows = rows[:1]
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": total, "repositories": rows})
	}))
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	ids := []int64{101, 3}
	c, e := NewInstallationCatalog(s.URL, "example.invalid", "private", func(context.Context, string) (string, error) { loads++; return "mock-installation", nil }, &tls.Config{RootCAs: roots}, ids)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ids[0] = 99
	rows, e := c.List(context.Background())
	if e != nil || len(rows) != 2 || rows[0].ID != 3 || rows[1].ID != 101 || calls != 2 || loads != 1 {
		t.Fatal(rows, e, calls, loads)
	}
	for _, failure := range []string{"short", "shrink", "duplicate", "oversized"} {
		fault = failure
		if rows, e := c.List(context.Background()); e == nil || rows != nil {
			t.Fatal(fault, rows, e)
		}
	}
}
