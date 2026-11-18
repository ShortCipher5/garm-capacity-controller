package adminview

import (
	"context"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"reflect"
	"testing"
	"time"
)

func TestDemandObserverReadOnly(t *testing.T) {
	h, store, token := fixture(t)
	before, _ := store.ReadSnapshot(context.Background())
	path := "/admin/api/v1/demand-observer-status"
	for _, v := range []struct {
		method, token string
		code          int
	}{{"GET", "", 401}, {"POST", token, 405}, {"DELETE", token, 405}} {
		if w := request(t, h, v.method, path, v.token); w.Code != v.code {
			t.Fatal(w.Code)
		}
	}
	w := request(t, h, "GET", path, token)
	var status demandobserver.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "unconfigured" || status.AllocationAuthorized {
		t.Fatal(w.Code, w.Body.String())
	}

	service, err := demandobserver.NewService(demandobserver.Config{Enabled: true, IntervalSeconds: 60, MaxBackoffSeconds: 120, CheckpointPath: "unused", InitialFrom: time.Now().Add(-time.Hour), Repositories: []demandrepair.Repository{{GarmID: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	h.SetDemandObserver(service)
	now := time.Now().UTC()
	for _, v := range []struct {
		event demandobserver.Event
		state string
	}{
		{demandobserver.Event{Status: "observed", StartedAt: now, FinishedAt: now, Counts: &demandobserver.Counts{StockQueued: 2, ObservedDemand: 3, ReaderOnly: 1}}, "observed"},
		{demandobserver.Event{Status: "garm_unavailable", StartedAt: now, FinishedAt: now}, "unavailable"},
		{demandobserver.Event{Status: "observed", StartedAt: now.Add(-time.Hour), FinishedAt: now, Counts: &demandobserver.Counts{ObservedDemand: 7}}, "stale"},
	} {
		raw, _ := json.Marshal(v.event)
		if _, err := service.Write(raw); err != nil {
			t.Fatal(err)
		}
		w = request(t, h, "GET", path, token)
		status = demandobserver.Status{}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != v.state || status.AllocationAuthorized {
			t.Fatal(w.Code, w.Body.String())
		}
		if v.state != "observed" && status.Event.Counts != nil {
			t.Fatal("historical counts presented current")
		}
	}
	after, _ := store.ReadSnapshot(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("ledger mutation")
	}
}
