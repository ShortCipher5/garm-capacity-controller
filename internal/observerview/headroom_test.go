package observerview

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/sshinventory"
	"net/http/httptest"
	"testing"
	"time"
)

type osRead func(context.Context) (sshinventory.Headroom, error)

func (f osRead) ReadHeadroom(ctx context.Context) (sshinventory.Headroom, error) { return f(ctx) }
func TestAuthenticatedDashboardSeparatesFreshPartialStaleAndDownHosts(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	ram := int64(100)
	cache, e := NewHeadroomCache(map[string]HeadroomSource{
		"alpha": osRead(func(context.Context) (sshinventory.Headroom, error) {
			return sshinventory.Headroom{ObservedAt: at, AvailableRAMMiB: &ram}, nil
		}),
		"beta": osRead(func(context.Context) (sshinventory.Headroom, error) {
			return sshinventory.Headroom{}, errors.New("private host error")
		}),
	})
	if e != nil {
		t.Fatal(e)
	}
	if cache.snapshot(at)[0].Status != "pending" {
		t.Fatal("invented first sample")
	}
	cache.poll(context.Background())
	if got := cache.snapshot(at.Add(2 * time.Minute))[0]; got.Status != "stale" || got.Sample != nil || got.LastObservedAt == nil || !got.LastObservedAt.Equal(at) {
		t.Fatal(got)
	}
	const token = "00000000000000000000000000000000000000000000000000000000000000aa"
	h, e := New(token, stockFunc(func(context.Context) (garmapi.DemandObservation, error) { return garmapi.DemandObservation{}, nil }), nil)
	if e != nil {
		t.Fatal(e)
	}
	h.SetHeadroomCache(cache)
	r := httptest.NewRequest("GET", Path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var v Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Hosts) != 0 || len(v.WholeHosts) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, b := v.WholeHosts[0], v.WholeHosts[1]
	if a.Host != "alpha" || a.Status != "partial" || a.Sample.CPUIdleBasisPoints != nil || *a.Sample.AvailableRAMMiB != ram || !a.Sample.ObservedAt.Equal(at) || a.RunnerStorageStatus != "unavailable_unbound_pool" || b.Status != "unavailable" || b.Sample != nil {
		t.Fatal(a, b)
	}
}
func TestSampleWorkerStopsOnCancellation(t *testing.T) {
	entered := make(chan struct{})
	c, _ := NewHeadroomCache(map[string]HeadroomSource{"blocked": osRead(func(ctx context.Context) (sshinventory.Headroom, error) {
		close(entered)
		<-ctx.Done()
		return sshinventory.Headroom{}, ctx.Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no immediate sampling")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker leaked")
	}
}
