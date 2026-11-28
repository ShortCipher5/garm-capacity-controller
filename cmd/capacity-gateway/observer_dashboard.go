package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/observerview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type observerDashboardConfig struct {
	Listen    string                           `json:"listen"`
	ServerTLS securefiles.TLSFiles             `json:"server_tls"`
	TokenFile string                           `json:"token_file"`
	Stock     observerStockConfig              `json:"stock"`
	Metrics   map[string]observerMetricsConfig `json:"metrics,omitempty"`
}

type observerStockConfig struct {
	Endpoint     string               `json:"endpoint"`
	Controller   string               `json:"controller"`
	ServerKeyPin string               `json:"server_key_pin"`
	ServerName   string               `json:"server_name,omitempty"`
	ClientTLS    securefiles.TLSFiles `json:"client_tls"`
	Providers    []string             `json:"providers"`
	Repositories []string             `json:"repositories"`
}

type observerMetricsConfig struct {
	Endpoint     string               `json:"endpoint"`
	Project      string               `json:"project"`
	ServerKeyPin string               `json:"server_key_pin"`
	ServerName   string               `json:"server_name,omitempty"`
	ClientTLS    securefiles.TLSFiles `json:"client_tls"`
}

// Reject selection of both modes even if interpolation or a repeated flag
// leaves the explicitly selected observer path empty.
func observerModeSelection(flags *flag.FlagSet, path string) (selected, valid bool) {
	mixed := flags.NArg() != 0
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "observer-config" {
			selected = true
		} else {
			mixed = true
		}
	})
	return selected, selected && !mixed && strings.TrimSpace(path) != ""
}

func runObserverDashboard(ctx context.Context, configPath string) error {
	raw, err := securefiles.Read(configPath, 1<<20)
	if err != nil {
		return err
	}
	var c observerDashboardConfig
	if gatewayrpc.Decode(bytes.NewReader(raw), &c) != nil {
		return gatewayrpc.ErrConfiguration
	}
	return serveObserverDashboard(ctx, c, nil)
}

func serveObserverDashboard(ctx context.Context, c observerDashboardConfig, listener net.Listener) error {
	host, _, err := net.SplitHostPort(c.Listen)
	if ip := net.ParseIP(host); err != nil || !ip.IsLoopback() || len(c.Metrics) > 8 {
		return gatewayrpc.ErrConfiguration
	}
	serverTLS, err := securefiles.LoadTLS(c.ServerTLS)
	if err != nil {
		return err
	}
	token, err := securefiles.Read(c.TokenFile, 256)
	if err != nil {
		return err
	}
	stockTLS, err := securefiles.LoadTLS(c.Stock.ClientTLS)
	if err != nil {
		return err
	}
	stock, err := garmapi.NewManagedDemandWithServerName(c.Stock.Endpoint, c.Stock.ServerKeyPin, c.Stock.Controller, c.Stock.ServerName, stockTLS, c.Stock.Providers, c.Stock.Repositories)
	if err != nil {
		return err
	}
	defer stock.Close()
	used := map[[32]byte]bool{}
	reserveKey := func(c *tls.Config) bool {
		if len(c.Certificates) != 1 || len(c.Certificates[0].Certificate) == 0 {
			return false
		}
		leaf, err := x509.ParseCertificate(c.Certificates[0].Certificate[0])
		if err != nil {
			return false
		}
		key := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
		if used[key] {
			return false
		}
		used[key] = true
		return true
	}
	if !reserveKey(serverTLS) || !reserveKey(stockTLS) {
		return gatewayrpc.ErrConfiguration
	}
	metrics := map[string]observerview.MetricsSource{}
	for id, cfg := range c.Metrics {
		trust, err := securefiles.LoadTLS(cfg.ClientTLS)
		if err != nil {
			return err
		}
		if !reserveKey(trust) {
			return gatewayrpc.ErrConfiguration
		}
		m, err := incusapi.NewMetricsObserverWithServerName(cfg.Endpoint, cfg.Project, cfg.ServerKeyPin, cfg.ServerName, trust)
		if err != nil {
			return err
		}
		defer m.Close()
		metrics[id] = m
	}
	view, err := observerview.New(strings.TrimSpace(string(token)), stock, metrics)
	if err != nil {
		return err
	}
	if listener == nil {
		listener, err = net.Listen("tcp", c.Listen)
		if err != nil {
			return err
		}
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		return gatewayrpc.ErrConfiguration
	}
	server := &http.Server{
		Addr: c.Listen, Handler: view, TLSConfig: serverTLS, ReadHeaderTimeout: 4 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 16 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 8 << 10, ErrorLog: log.New(redactedConnectionLog{}, "", 0),
	}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stopCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := server.Shutdown(stopCtx); err != nil {
			return err
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
