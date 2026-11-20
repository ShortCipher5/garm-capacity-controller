package adminview

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"reflect"
	"strings"
	"testing"
	"time"
)

type planFixture struct {
	reads   int
	fail    bool
	preview *brokerinput.StockMaximumPreview
}

func (p *planFixture) Snapshot() brokerinput.LoopStatus { return brokerinput.LoopStatus{} }
func (p *planFixture) ReadPlan(ctx context.Context) (brokerinput.ReadPlan, error) {
	p.reads++
	if p.fail {
		return brokerinput.ReadPlan{}, errors.New("private failure")
	}
	return brokerinput.ReadPlan{Source: "scoped-stock-and-coherent-ledger", StockMaximumPreview: p.preview}, nil
}
func TestAllocatorPlanReadOnlyAuthAndFailure(t *testing.T) {
	h, store, token := fixture(t)
	before, _ := store.ReadSnapshot(context.Background())
	p := &planFixture{}
	h.SetAllocatorStatusSource(p)
	path := "/admin/api/v1/allocator-plan"
	for _, v := range []struct {
		method, token string
		status        int
	}{{"GET", "", 401}, {"POST", token, 405}, {"DELETE", token, 405}} {
		if w := request(t, h, v.method, path, v.token); w.Code != v.status {
			t.Fatal(w.Code)
		}
	}
	if p.reads != 0 {
		t.Fatal("unauthorized source read")
	}
	if w := request(t, h, "GET", path, token); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p.fail = true
	if w := request(t, h, "GET", path, token); w.Code != 503 || !strings.Contains(w.Body.String(), `"reason":"preview_unavailable"`) || strings.Contains(w.Body.String(), "private failure") {
		t.Fatal("failure leaked data", w.Code, w.Body.String())
	}
	after, _ := store.ReadSnapshot(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read mutated ledger")
	}
	h.SetAllocatorStatusSource(nil)
	if w := request(t, h, "GET", path, token); w.Code != 503 {
		t.Fatal("missing source zero plan", w.Code)
	}
}

func TestAllocatorPlanIndependentOfGrantLoop(t *testing.T) {
	h, _, token := fixture(t)
	p := &planFixture{}
	h.SetAllocatorPlanSource(p)
	if w := request(t, h, "GET", "/admin/api/v1/allocator-plan", token); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if h.allocatorStatus != nil || p.reads != 1 {
		t.Fatal("advisory configured grant lifecycle")
	}
	h.SetAllocatorPlanSource(nil)
	if w := request(t, h, "GET", "/admin/api/v1/allocator-plan", token); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestAllocatorStockMaximumPreviewOnExistingAuthenticatedRoute(t *testing.T) {
	h, _, token := fixture(t)
	p := &planFixture{}
	h.SetAllocatorPlanSource(p)
	path := "/admin/api/v1/allocator-plan"
	base := request(t, h, "GET", path, token)
	if base.Code != 200 || strings.Contains(base.Body.String(), "stock_maximum_preview") {
		t.Fatal(base.Code, base.Body.String())
	}
	expiry := time.Now().UTC().Add(time.Minute)
	p.preview = &brokerinput.StockMaximumPreview{State: "proposed", ExpiresAt: expiry, Proposals: []brokerinput.StockMaximumProposal{{PoolID: "p", RepositoryID: "r", Observed: 1, Suggested: 2}}}
	if unauth := request(t, h, "GET", path, ""); unauth.Code != 401 || p.reads != 1 || strings.Contains(unauth.Body.String(), "stock_maximum_preview") {
		t.Fatal("unauthorized preview", unauth.Code)
	}
	authorized := request(t, h, "GET", path, token)
	var plan brokerinput.ReadPlan
	if authorized.Code != 200 || json.Unmarshal(authorized.Body.Bytes(), &plan) != nil ||
		!reflect.DeepEqual(plan.StockMaximumPreview, p.preview) || p.reads != 2 {
		t.Fatal(authorized.Code, authorized.Body.String())
	}
}
