package adminview

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

func usageRequest(t *testing.T, h *Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://127.0.0.1"+target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.serveUsage(w, r)
	return w
}
func decodeUsage(t *testing.T, w *httptest.ResponseRecorder) []admission.UsageSample {
	t.Helper()
	var history []admission.UsageSample
	if json.Unmarshal(w.Body.Bytes(), &history) != nil {
		t.Fatalf("invalid usage history body %q", w.Body.String())
	}
	return history
}

func TestUsageHistoryEndpointAuthenticatesAndServesEmptyHistory(t *testing.T) {
	h, _, token := fixture(t)
	if response := usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a", ""); response.Code != 401 {
		t.Fatalf("unauthenticated usage read allowed %d", response.Code)
	}
	if response := usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a", strings.Repeat("b", 64)); response.Code != 401 {
		t.Fatalf("wrong token usage read allowed %d", response.Code)
	}
	response := usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a", token)
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("empty history not served %d", response.Code)
	}
	if strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("absence must be an empty array, not fabricated zeros: %q", response.Body.String())
	}
	// The coordinator-owned dispatch reaches the same handler through ServeHTTP.
	response = request(t, h, "GET", "/admin/api/v1/usage/host-a", token)
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("dispatch path failed %d %q", response.Code, response.Body.String())
	}
	if response := request(t, h, "GET", "/admin/api/v1/usage/host-a", ""); response.Code != 401 {
		t.Fatalf("dispatch path lacked bearer auth %d", response.Code)
	}
	if response := request(t, h, "POST", "/admin/api/v1/usage/host-a", token); response.Code != 405 {
		t.Fatalf("non-GET usage allowed %d", response.Code)
	}
}

func TestUsageHistoryReturnsRecordedSamplesInOrder(t *testing.T) {
	h, store, token := fixture(t)
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		at             time.Time
		cpu, ram, disk int64
	}{
		{base, 250, 1024, 2048},
		{base.Add(time.Minute), 500, 2048, 2048},
		{base.Add(2 * time.Minute), 125, 512, 1024},
	} {
		if err := store.RecordUsageSample(context.Background(), "host-a", sample.at, sample.cpu, sample.ram, sample.disk, "guest-state"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordUsageSample(context.Background(), "host-b", base, 9, 9, 9, "other-source"); err != nil {
		t.Fatal(err)
	}
	response := usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a", token)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	history := decodeUsage(t, response)
	if len(history) != 3 || history[0].SampledAt != base || history[1].CPUUsedMillis != 500 || history[2].RAMUsedMiB != 512 {
		t.Fatalf("history order/content wrong %+v", history)
	}
	if history[0].Host != "host-a" || history[0].Source != "guest-state" || history[0].CPUUsedMillis != 250 || history[0].DiskUsedMiB != 2048 {
		t.Fatalf("sample fields not surfaced verbatim %+v", history[0])
	}
	if strings.Contains(response.Body.String(), "other-source") || strings.Contains(response.Body.String(), "host-b") {
		t.Fatal("usage history leaked another host's samples")
	}
}

func TestUsageHistoryFiltersBySinceAndLimit(t *testing.T) {
	h, store, token := fixture(t)
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := store.RecordUsageSample(context.Background(), "host-a", base.Add(time.Duration(i)*time.Minute), int64(i), int64(i), int64(i), "guest-state"); err != nil {
			t.Fatal(err)
		}
	}
	response := usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a?since="+base.Add(2*time.Minute).Format(time.RFC3339), token)
	history := decodeUsage(t, response)
	if response.Code != 200 || len(history) != 3 || history[0].CPUUsedMillis != 2 {
		t.Fatalf("since filter wrong %d %+v", response.Code, history)
	}
	response = usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a?limit=2", token)
	history = decodeUsage(t, response)
	if response.Code != 200 || len(history) != 2 || history[0].CPUUsedMillis != 0 || history[1].CPUUsedMillis != 1 {
		t.Fatalf("limit wrong %d %+v", response.Code, history)
	}
	response = usageRequest(t, h, "GET", "/admin/api/v1/usage/host-a?since="+base.Add(3*time.Minute).Format(time.RFC3339Nano)+"&limit=1", token)
	history = decodeUsage(t, response)
	if response.Code != 200 || len(history) != 1 || history[0].CPUUsedMillis != 3 {
		t.Fatalf("since+limit wrong %d %+v", response.Code, history)
	}
}

func TestUsageHistoryRecentSelectsNewestBoundedPage(t *testing.T) {
	h, store, token := fixture(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	record := func(host string, n int) {
		t.Helper()
		if err := store.RecordUsageSample(ctx, host, base.Add(time.Duration(n)*time.Millisecond), int64(n), 0, 0, "guest-state"); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n <= 500; n++ {
		record("host-a", n)
	}
	record("host-b", 800)
	const endpoint = "/admin/api/v1/usage/host-a"
	check := func(query string, count, first, last int) {
		t.Helper()
		res := request(t, h, "GET", endpoint+query, token)
		if res.Code != 200 {
			t.Fatalf("%s returned %d: %s", query, res.Code, res.Body)
		}
		samples := decodeUsage(t, res)
		if len(samples) != count || samples[0].CPUUsedMillis != int64(first) || samples[count-1].CPUUsedMillis != int64(last) {
			t.Fatalf("%s did not return %d samples spanning %d through %d", query, count, first, last)
		}
		for i, sample := range samples {
			if sample.Host != "host-a" || !sample.SampledAt.Equal(base.Add(time.Duration(first+i)*time.Millisecond)) {
				t.Fatalf("%s row %d not in exact host/time order: %+v", query, i, sample)
			}
		}
	}
	check("", 500, 0, 499)
	check("?limit=500&order=recent", 500, 1, 500)
	record("host-a", 501)
	check("?limit=500&order=recent", 500, 2, 501)
	check("", 500, 0, 499)
	cutoff := base.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
	check("?since="+cutoff+"&order=recent", 2, 500, 501)
	check("?since="+cutoff+"&limit=1&order=recent", 1, 501, 501)
	check("?since="+cutoff+"&limit=1", 1, 500, 500)
	empty := request(t, h, "GET", "/admin/api/v1/usage/unused?order=recent", token)
	if empty.Code != 200 || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty recent history: %d %s", empty.Code, empty.Body)
	}
	noAuth := request(t, h, "GET", endpoint+"?order=recent", "")
	if noAuth.Code != 401 {
		t.Fatalf("recent not authenticated: %d", noAuth.Code)
	}
}

func TestUsageQueryExceptionIsLimitedToAuthenticatedUsageReads(t *testing.T) {
	h, _, token := fixture(t)
	for _, tc := range []struct {
		method string
		target string
		token  string
	}{
		{"GET", "/admin/api/v1/overview?limit=1", token},
		{"GET", "/admin/api/v1/usage/host-a?token=" + token, token},
		{"GET", "/admin/api/v1/usage/host-a?order=recent&token=" + token, token},
		{"GET", "/admin/api/v1/usage/host-a?order=recent&order=recent", token},
		{"GET", "/admin/api/v1/usage/host-a?order=%ZZ", token},
		{"POST", "/admin/api/v1/usage/host-a?order=recent", token},
	} {
		if w := request(t, h, tc.method, tc.target, tc.token); w.Code != 400 {
			t.Errorf("unexpected response %d for %s %s", w.Code, tc.method, strings.SplitN(tc.target, "?", 2)[0])
		}
	}
	if w := request(t, h, "GET", "/admin/api/v1/usage/host-a?token="+token, ""); w.Code != 401 {
		t.Errorf("query-only credential did not remain unauthorized: %d", w.Code)
	}
}

func TestUsageHistoryRejectsMalformedInputFailClosed(t *testing.T) {
	h, _, token := fixture(t)
	for _, target := range []string{
		"/admin/api/v1/usage/",
		"/admin/api/v1/usage/a/b",
		"/admin/api/v1/usage/%20%20",
		"/admin/api/v1/usage/host-a?since=yesterday",
		"/admin/api/v1/usage/host-a?since=2026-11-17",
		"/admin/api/v1/usage/host-a?since=2026-11-17T10:00:00Z&since=2026-11-17T11:00:00Z",
		"/admin/api/v1/usage/host-a?limit=0",
		"/admin/api/v1/usage/host-a?limit=-1",
		"/admin/api/v1/usage/host-a?limit=1001",
		"/admin/api/v1/usage/host-a?limit=two",
		"/admin/api/v1/usage/host-a?limit=1.5",
		"/admin/api/v1/usage/host-a?limit=2&limit=3",
		"/admin/api/v1/usage/host-a?order=forward",
		"/admin/api/v1/usage/host-a?order=",
		"/admin/api/v1/usage/host-a?order=recent&order=recent",
		"/admin/api/v1/usage/host-a?order=%ZZ",
		"/admin/api/v1/usage/host-a?order=recent;limit=1",
		"/admin/api/v1/usage/host-a?host=other",
		"/admin/api/v1/usage/host-a?token=" + token,
	} {
		if response := usageRequest(t, h, "GET", target, token); response.Code != 400 {
			t.Fatalf("malformed usage request accepted %s -> %d", target, response.Code)
		}
	}
	if response := usageRequest(t, h, "DELETE", "/admin/api/v1/usage/host-a", token); response.Code != 405 {
		t.Fatalf("usage mutation allowed %d", response.Code)
	}
}

func TestUsageHistoryUnavailableStoreFailsWithoutCurrentData(t *testing.T) {
	store, err := admission.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"), admission.Config{Global: admission.Resources{CPU: 1, RAM: 1, Runners: 1}, Hosts: map[string]admission.Host{"host-a": {Budget: admission.Resources{CPU: 1, RAM: 1, Runners: 1}, Mode: "enabled", Healthy: true, ObservedAt: time.Now()}}, Pools: map[string]admission.Pool{"pool-a": {Budget: admission.Resources{CPU: 1, RAM: 1, Runners: 1}, Hosts: []string{"host-a"}}}, MaxObservationAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUsageSample(context.Background(), "host-a", time.Now().UTC(), 1, 1, 1, "guest-state"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := New(store, strings.Repeat("d", 64), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	response := usageRequest(t, handler, "GET", "/admin/api/v1/usage/host-a", strings.Repeat("d", 64))
	if response.Code != 503 || strings.Contains(response.Body.String(), "guest-state") {
		t.Fatalf("failed store leaked monitoring data %d %q", response.Code, response.Body.String())
	}
}
