package incusapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListedIncarnationRequiresPinnedExactInventory(t *testing.T) {
	var server *httptest.Server
	var mode atomic.Value
	mode.Store("complete")
	server, credentials := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("project") != "runners" {
			t.Error("unexpected list request")
		}
		switch r.URL.Path {
		case "/1.0":
			writeSync(w, physicalRoot(server))
		case "/1.0/instances":
			if r.URL.Query().Get("recursion") != "1" {
				t.Error("incomplete list")
			}
			body := instanceEvidenceBody()
			conf := body["expanded_config"].(map[string]any)
			switch mode.Load() {
			case "project":
				body["project"] = "other"
			case "future":
				body["created_at"] = time.Now().Add(time.Hour)
			case "missing":
				delete(conf, "volatile.uuid")
			case "folded":
				conf["Volatile.UUID.Generation"] = evidenceOtherUUID
			case "nil":
				conf["volatile.uuid.generation"] = "00000000-0000-0000-0000-000000000000"
			}
			writeSync(w, []any{body})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}))
	client, err := New(server.URL, "runners", credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := t.Context()
	physical, err := client.PhysicalIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"unpinned", "complete", "unfenced", "project", "future", "missing", "folded", "nil"} {
		mode.Store(test)
		if test == "complete" {
			if err := client.PinPhysicalIdentity(physical); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		collectionCtx := WithInventoryFence(ctx, strings.Repeat("c", 32))
		if test == "unfenced" {
			collectionCtx = ctx
		}
		list, err := client.Instances(collectionCtx)
		if err != nil || len(list) != 1 || list[0].Name != "runner-1" {
			t.Fatalf("list (%s): %+v %v", test, list, err)
		}
		obs := list[0].ListedIdentity.Observation()
		subject, began, completed := obs.Subject, obs.StartedAt, obs.CompletedAt
		if test == "complete" {
			if subject.UUID != evidenceUUID || subject.Generation != evidenceGeneration || subject.ScopeFingerprint != physical.ScopeFingerprint() ||
				subject.BackendKeyFingerprint != physical.ServerKeyFingerprint() || began.Before(start) || completed.Before(began) {
				t.Fatalf("lost authenticated list identity: %+v", subject)
			}
		} else if !began.IsZero() || subject.UUID != "" {
			t.Fatal("unqualified list minted identity", test)
		}
	}
	var forged ListedInstanceIdentity
	if err := json.Unmarshal([]byte(`{"subject":{"uuid":"forged"},"started":"2026-01-01T00:00:00Z"}`), &forged); err != nil || forged != (ListedInstanceIdentity{}) {
		t.Fatal("JSON supplied list identity", err)
	}
}
