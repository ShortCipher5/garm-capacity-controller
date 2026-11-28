package observerview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

type stockFunc func(context.Context) (garmapi.DemandObservation, error)

func (f stockFunc) Observe(ctx context.Context) (garmapi.DemandObservation, error) { return f(ctx) }

type metricsFunc func(context.Context) ([]incusapi.GuestTelemetry, error)

func (f metricsFunc) ReadGuestTelemetry(ctx context.Context) ([]incusapi.GuestTelemetry, error) {
	return f(ctx)
}

func TestObservationBoundary(t *testing.T) {
	const token = "00000000000000000000000000000000000000000000000000000000000000aa"
	var called atomic.Int32
	stock := stockFunc(func(context.Context) (garmapi.DemandObservation, error) {
		called.Add(1)
		return garmapi.DemandObservation{Supply: []garmapi.RunnerSupply{{PoolID: "fixture", Idle: 1}}}, nil
	})
	h, err := New(token, stock, map[string]MetricsSource{
		"mumbai": metricsFunc(func(context.Context) ([]incusapi.GuestTelemetry, error) {
			return []incusapi.GuestTelemetry{{Name: "fixture-runner"}}, nil
		}),
		"netcup": metricsFunc(func(context.Context) ([]incusapi.GuestTelemetry, error) {
			return nil, errors.New("sensitive failure sentinel")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if do(http.MethodPost, Path, token).Code != http.StatusMethodNotAllowed ||
		do(http.MethodGet, Path+"?host=other", token).Code != http.StatusNotFound ||
		do(http.MethodGet, Path, "").Code != http.StatusUnauthorized || called.Load() != 0 {
		t.Fatal("out-of-scope request reached an upstream")
	}
	res := do(http.MethodGet, Path, token)
	var snapshot Snapshot
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &snapshot) != nil ||
		snapshot.Mode != "observation_only" || snapshot.Stock == nil || len(snapshot.Hosts) != 2 ||
		snapshot.Hosts[0].Status != "running_guest_telemetry_only" || snapshot.Hosts[1].Status != "unavailable" ||
		strings.Contains(res.Body.String(), "sentinel") || strings.Contains(res.Body.String(), "capacity") {
		t.Fatalf("unexpected protected observation: %d %s", res.Code, res.Body.String())
	}
}
