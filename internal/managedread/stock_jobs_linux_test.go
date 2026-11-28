package managedread

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStockJobsPagination(t *testing.T) {
	const controller = "00000000-0000-4000-8000-000000000091"
	for _, tc := range []struct {
		name string
		want int
	}{
		{"legacy", 1}, {"complete", 101}, {"empty", 0},
		{"changed totals", -1}, {"duplicate record", -1}, {"bad next page", -1}, {"over page cap", -1}, {"denied later page", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-only" {
					t.Error("unexpected method or auth")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch r.URL.RequestURI() {
				case "/api/v1/controller":
					fmt.Fprintf(w, `{"controller_id":%q}`, controller)
				case "/api/v1/jobs":
					requests.Add(1)
					if tc.name == "legacy" {
						fmt.Fprint(w, `[{"id":111}]`)
					} else {
						fmt.Fprint(w, `{"total_count":1,"pages":1,"current_page":1,"results":[{"id":107}]}`)
					}
				case "/api/v1/jobs?all=true&page=1&pageSize=100":
					requests.Add(1)
					if tc.name == "empty" {
						fmt.Fprint(w, `{"total_count":0,"pages":0,"current_page":1,"results":[]}`)
						return
					}
					results := make([]map[string]any, 0, 100)
					for i := 101; i >= 2; i-- {
						results = append(results, map[string]any{"id": i, "status": "queued"})
					}
					total, pages, next := 101, 2, 2
					if tc.name == "bad next page" {
						next = 3
					}
					if tc.name == "over page cap" {
						total, pages = 1601, 17
					}
					json.NewEncoder(w).Encode(map[string]any{"total_count": total, "pages": pages, "current_page": 1, "next_page": next, "results": results})
				case "/api/v1/jobs?all=true&page=2&pageSize=100":
					requests.Add(1)
					if tc.name == "denied later page" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					total, id := 101, 1
					if tc.name == "changed totals" {
						total = 102
					}
					if tc.name == "duplicate record" {
						id = 2
					}
					fmt.Fprintf(w, `{"total_count":%d,"pages":2,"current_page":2,"previous_page":1,"results":[{"id":%d,"status":"completed"}]}`, total, id)
				default:
					t.Errorf("unplanned request: %s", r.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			pin := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
			reader, err := newIsolatedStockReads(context.Background(), server.URL, controller, hex.EncodeToString(pin[:]), "fixture-only", &tls.Config{RootCAs: roots})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := reader.Jobs(context.Background())
			if tc.want < 0 {
				if raw != nil || err == nil {
					t.Fatalf("partial or invalid inventory accepted: %q, %v", raw, err)
				}
				return
			}
			var records []struct{ Status string }
			if err != nil || json.Unmarshal(raw, &records) != nil || len(records) != tc.want {
				t.Fatalf("jobs wanted %d, got %d, %v", tc.want, len(records), err)
			}
			if tc.name == "complete" && (records[100].Status != "completed" || requests.Load() != 3) {
				t.Fatal("the second page, including completed jobs, was not checked")
			}
		})
	}
}
