// Package observerview serves a bounded dashboard without opening admission
// state or constructing any provider, operation or management client.
package observerview

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

const Path = "/admin/api/v1/observer"

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed dashboard.js
var dashboardJS []byte

type StockSource interface {
	Observe(context.Context) (garmapi.DemandObservation, error)
}
type MetricsSource interface {
	ReadGuestTelemetry(context.Context) ([]incusapi.GuestTelemetry, error)
}

type HostTelemetry struct {
	Host   string                    `json:"host"`
	Status string                    `json:"status"`
	Guests []incusapi.GuestTelemetry `json:"guests,omitempty"`
}

type Snapshot struct {
	WholeHosts []HeadroomRow              `json:"whole_hosts,omitempty"`
	Mode       string                     `json:"mode"`
	ObservedAt time.Time                  `json:"observed_at"`
	Stock      *garmapi.DemandObservation `json:"stock,omitempty"`
	Hosts      []HostTelemetry            `json:"hosts"`
}

type source struct {
	host string
	MetricsSource
}

type Handler struct {
	token [32]byte
	stock StockSource
	hosts []source
	busy  chan struct{}
	whole *HeadroomCache
}

var hostID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

func New(token string, stock StockSource, metrics map[string]MetricsSource) (*Handler, error) {
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != token || stock == nil || len(metrics) > 8 {
		return nil, errors.New("invalid observation dashboard configuration")
	}
	h := &Handler{token: sha256.Sum256([]byte(token)), stock: stock, busy: make(chan struct{}, 1)}
	for id, src := range metrics {
		if !hostID.MatchString(id) || src == nil {
			return nil, errors.New("invalid observation dashboard host")
		}
		h.hosts = append(h.hosts, source{id, src})
	}
	sort.Slice(h.hosts, func(i, j int) bool { return h.hosts[i].host < h.hosts[j].host })
	return h, nil
}

// SetHeadroomCache is called during startup, before the HTTP server is started.
func (h *Handler) SetHeadroomCache(c *HeadroomCache) { h.whole = c }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if r.Method == http.MethodGet {
		switch r.URL.RequestURI() {
		case "/admin/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(dashboardHTML)
			return
		case "/admin/observer.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(dashboardJS)
			return
		}
	}
	if r.URL.RequestURI() != Path {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	token := sha256.Sum256([]byte(strings.TrimPrefix(auth[0], "Bearer ")))
	if subtle.ConstantTimeCompare(token[:], h.token[:]) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	select {
	case h.busy <- struct{}{}:
		defer func() { <-h.busy }()
	default:
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	s := Snapshot{Mode: "observation_only", Hosts: make([]HostTelemetry, len(h.hosts))}
	var work sync.WaitGroup
	work.Go(func() {
		o, err := h.stock.Observe(ctx)
		if err == nil && ctx.Err() == nil {
			s.Stock = &o
		}
	})
	for i, src := range h.hosts {
		work.Go(func() {
			row := HostTelemetry{Host: src.host, Status: "unavailable"}
			if data, err := src.ReadGuestTelemetry(ctx); err == nil && ctx.Err() == nil {
				row.Guests = data
				row.Status = "running_guest_telemetry_only"
			}
			s.Hosts[i] = row
		})
	}
	work.Wait()
	s.ObservedAt = time.Now().UTC()
	s.WholeHosts = h.whole.snapshot(s.ObservedAt)
	w.Header().Set("Content-Type", "application/json")
	if s.Stock == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(s)
}
