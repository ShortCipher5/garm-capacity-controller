package managedread

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// PrivateStockConfig is read exclusively by the inventory process. The
// dashboard must not mount this configuration, bearer or private TLS key.
type PrivateStockConfig struct {
	Listen                 string               `json:"listen"`
	StockEndpoint          string               `json:"stock_endpoint"`
	StockController        string               `json:"stock_controller"`
	StockServerKeyPin      string               `json:"stock_server_key_pin"`
	StockTLSName           string               `json:"stock_tls_name"`
	StockClientCertificate string               `json:"stock_client_certificate,omitempty"`
	StockClientKey         string               `json:"stock_client_key,omitempty"`
	StockCAFile            string               `json:"stock_ca_file"`
	StockBearerFile        string               `json:"stock_bearer_file,omitempty"`
	StockLoginUsername     string               `json:"stock_login_username,omitempty"`
	StockLoginPasswordFile string               `json:"stock_login_password_file,omitempty"`
	InventoryTLS           securefiles.TLSFiles `json:"inventory_tls"`
	GatewayLeaf            string               `json:"gateway_leaf"`
	Pools                  []string             `json:"pools"`
	Providers              []string             `json:"providers"`
	Repositories           []string             `json:"repositories"`
}

func privateServer(source Source, c PrivateStockConfig, inbound *tls.Config) (*http.Server, error) {
	addr, err := netip.ParseAddrPort(c.Listen)
	if err != nil || addr.Addr().Zone() != "" || !addr.Addr().IsLoopback() || addr.Port() == 0 ||
		len(c.Pools) == 0 || len(c.Repositories) == 0 || inbound == nil || inbound.RootCAs == nil || len(inbound.Certificates) != 1 {
		return nil, ErrConfiguration
	}
	h, err := New(source, c.GatewayLeaf, c.Providers, c.Pools, c.Repositories)
	if err != nil {
		return nil, err
	}
	// Accept only the credentials actually loaded for the service. Normal
	// chain and clientAuth verification runs before the Handler's exact leaf check.
	mtls := &tls.Config{Certificates: inbound.Certificates, ClientCAs: inbound.RootCAs.Clone(),
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true}
	slots := make(chan struct{}, 4)
	return &http.Server{Addr: addr.String(), TLSConfig: mtls,
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 4 * time.Second,
		WriteTimeout: 16 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				h.ServeHTTP(w, r)
			default:
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}),
	}, nil
}

// ServePrivateStock runs the isolated GET-only service on loopback. The
// caller must run it as its own OS identity, never in the gateway process.
func ServePrivateStock(ctx context.Context, c PrivateStockConfig) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrConfiguration
	}
	listen, err := netip.ParseAddrPort(c.Listen)
	if err != nil || !listen.Addr().IsLoopback() || listen.Addr().Zone() != "" || listen.Port() == 0 {
		return ErrConfiguration
	}
	trust := &tls.Config{ServerName: c.StockTLSName}
	if c.StockClientCertificate != "" || c.StockClientKey != "" {
		clientTLS, err := securefiles.LoadTLS(securefiles.TLSFiles{
			CA: c.StockCAFile, Certificate: c.StockClientCertificate, Key: c.StockClientKey,
		})
		if err != nil {
			return err
		}
		clientTLS.ServerName = c.StockTLSName
		trust = clientTLS
	} else {
		ca, err := securefiles.Read(c.StockCAFile, 1<<20)
		if err != nil {
			return err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return ErrConfiguration
		}
		trust.RootCAs = roots
	}
	var bearer string
	if c.StockBearerFile != "" {
		token, err := securefiles.Read(c.StockBearerFile, 16384)
		if err != nil {
			return err
		}
		bearer = strings.TrimSuffix(strings.TrimSuffix(string(token), "\n"), "\r")
		clear(token)
		if bearer == "" || c.StockLoginUsername != "" || c.StockLoginPasswordFile != "" {
			return ErrConfiguration
		}
	}
	inbound, err := securefiles.LoadTLS(c.InventoryTLS)
	if err != nil {
		return err
	}
	stock, err := newStockReads(ctx, c.StockEndpoint, c.StockController, c.StockServerKeyPin, bearer, c.StockLoginUsername, c.StockLoginPasswordFile, trust)
	if err != nil {
		return err
	}
	srv, err := privateServer(stock, c, inbound)
	if err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return ErrConfiguration
	}
	defer listener.Close()
	run, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-run.Done()
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if srv.Shutdown(stop) != nil {
			_ = srv.Close()
		}
	}()
	err = srv.ServeTLS(listener, "", "")
	cancelRun()
	<-stopped
	if !errors.Is(err, http.ErrServerClosed) {
		return ErrConfiguration
	}
	return nil
}
