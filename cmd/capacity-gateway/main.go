// capacity-gateway is an experimental credential-owning service. It is
// read-only by default; provisioning requires an explicit startup opt-in.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type hostConfiguration struct {
	Endpoint string               `json:"endpoint"`
	Project  string               `json:"project"`
	TLS      securefiles.TLSFiles `json:"tls"`
}
type garmConfiguration struct {
	Endpoint  string               `json:"endpoint"`
	TokenFile string               `json:"token_file"`
	TLS       securefiles.TLSFiles `json:"tls"`
	Providers []string             `json:"providers"`
	ScaleSets map[uint64]string    `json:"scalesets"`
}
type configuration struct {
	Admin                *adminConfiguration           `json:"admin,omitempty"`
	Listen               string                        `json:"listen"`
	Controller           string                        `json:"controller"`
	Database             string                        `json:"database"`
	IdentityFile         string                        `json:"identity_file"`
	InitialPolicy        admission.Config              `json:"initial_policy"`
	Pools                map[string]gateway.PoolPolicy `json:"pools"`
	Hosts                map[string]hostConfiguration  `json:"hosts"`
	Garm                 garmConfiguration             `json:"garm"`
	ServerTLS            securefiles.TLSFiles          `json:"server_tls"`
	ProviderFingerprints []string                      `json:"provider_fingerprints"`
}

type redactedConnectionLog struct{}

func (redactedConnectionLog) Write(message []byte) (int, error) {
	log.Print("gateway TLS/HTTP connection error")
	return len(message), nil
}

func serve(ctx context.Context, config configuration, provision bool) error {
	// Initial listener is deliberately local-only even though it also requires
	// verified mTLS and a leaf allowlist. Use a private reverse tunnel if needed;
	// never terminate client verification in an unauthenticated proxy.
	host, _, err := net.SplitHostPort(config.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return gatewayrpc.ErrConfiguration
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return err
	}
	return serveOnListener(ctx, config, provision, listener)
}

// A supplied listener is used by the offline integration tests; production calls
// still validate and create an explicit loopback listener above.
func serveOnListener(ctx context.Context, config configuration, provision bool, listener net.Listener) error {
	return serveOnListeners(ctx, config, provision, listener, nil)
}

func serveOnListeners(ctx context.Context, config configuration, provision bool, listener, adminListener net.Listener) error {
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return gatewayrpc.ErrConfiguration
	}
	store, err := admission.OpenRuntimeSQLite(config.Database, config.IdentityFile)
	if err != nil {
		return err
	}
	defer store.Close()
	backends := map[string]gateway.Backend{}
	for id, host := range config.Hosts {
		credentials, err := securefiles.LoadTLS(host.TLS)
		if err != nil {
			return err
		}
		client, err := incusapi.New(host.Endpoint, host.Project, credentials)
		if err != nil {
			return err
		}
		defer client.Close()
		backends[id] = client
	}
	f, err := gateway.New(ctx, store, config.Controller, config.Pools, backends)
	if err != nil {
		return err
	}
	token, err := securefiles.Read(config.Garm.TokenFile, 16384)
	if err != nil {
		return err
	}
	garmTLS, err := securefiles.LoadTLS(config.Garm.TLS)
	if err != nil {
		return err
	}
	garm, err := garmapi.New(config.Garm.Endpoint, string(bytes.TrimSpace(token)), garmTLS, config.Garm.Providers, config.Garm.ScaleSets)
	if err != nil {
		return err
	}
	defer garm.Close()
	handler, err := gatewayrpc.NewHandler(f, config.ProviderFingerprints, provision)
	if err != nil {
		return err
	}
	handler.Audit = func(event string) { log.Print(event) }
	serverCredentials, err := securefiles.LoadTLS(config.ServerTLS)
	if err != nil {
		return err
	}
	tlsConfig, err := gatewayrpc.ServerTLS(serverCredentials.Certificates[0], serverCredentials.RootCAs)
	if err != nil {
		return err
	}
	// No secrets, request bodies, URLs, panic values or provider output in logs.
	server := &http.Server{Addr: config.Listen, Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 12 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(redactedConnectionLog{}, "", 0)}
	if err := validateGarmManagementRouting(config); err != nil {
		return err
	}
	remoteManager, closeRemote, err := newGarmControl(config.Admin, string(bytes.TrimSpace(token)), config.Database)
	if err != nil {
		return err
	}
	defer closeRemote()
	if config.Admin != nil {
		config.Admin.garmRuntime = remoteManager
	}
	adminServer, closeAdmin, err := newAdminServer(config.Admin, store, provision)
	if err != nil {
		return err
	}
	defer closeAdmin()
	if adminServer != nil {
		if adminListener == nil {
			adminListener, err = net.Listen("tcp", adminServer.Addr)
			if err != nil {
				return err
			}
		}
		defer adminListener.Close()
		address, ok := adminListener.Addr().(*net.TCPAddr)
		if !ok || !address.IP.IsLoopback() {
			return gatewayrpc.ErrConfiguration
		}
	} else if adminListener != nil {
		adminListener.Close()
		return gatewayrpc.ErrConfiguration
	}
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval := time.Second
	// Existing persisted policy wins over initial policy, including freshness.
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	interval = snapshot.Config.MaxObservationAge / 3
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	reconcile := func() {
		check, cancel := context.WithTimeout(runctx, time.Minute)
		defer cancel()
		if f.Reconcile(check, garm) != nil {
			log.Print("inventory unavailable; admission remains blocked")
		}
	}
	reconcile()
	if err := runctx.Err(); err != nil {
		return err
	}
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		timer := time.NewTicker(interval)
		defer timer.Stop()
		for {
			select {
			case <-runctx.Done():
				return
			case <-timer.C:
				reconcile()
			}
		}
	}()
	defer func() { cancel(); <-loopDone }()
	var adminDone chan error
	if adminServer != nil {
		adminDone = make(chan error, 1)
		go func() {
			err := adminServer.ServeTLS(adminListener, "", "")
			adminDone <- err
			if !errors.Is(err, http.ErrServerClosed) {
				cancel()
			}
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-runctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		if adminServer != nil {
			adminStop, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if adminServer.Shutdown(adminStop) != nil {
				_ = adminServer.Close()
			}
		}
	}()
	err = server.ServeTLS(listener, "", "")
	cancel()
	<-shutdownDone
	if adminDone != nil {
		adminErr := <-adminDone
		if !errors.Is(adminErr, http.ErrServerClosed) {
			return adminErr
		}
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	configPath := flag.String("config", "", "private gateway configuration file")
	provision := flag.Bool("allow-experimental-provisioning", false, "allow one-shot creates after successful reconciliation; requires qualified exclusive scopes and fenced old writers")
	management := flag.Bool("allow-experimental-management", false, "allow separately authorized ledger-policy edits; not Garm pool registration or runner drain")
	remoteManagement := flag.Bool("allow-experimental-garm-management", false, "allow separately journalled scoped Garm settings writes; requires qualified gateway routing, exclusive external settings writer, separate upstream token and management opt-in")
	flag.Parse()
	raw, err := securefiles.Read(*configPath, 1<<20)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway configuration unavailable")
		os.Exit(1)
	}
	var config configuration
	if gatewayrpc.Decode(bytes.NewReader(raw), &config) != nil {
		fmt.Fprintln(os.Stderr, "gateway configuration unavailable")
		os.Exit(1)
	}
	if *management {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "management requires separate admin listener configuration")
			os.Exit(1)
		}
		config.Admin.EnableExperimentalManagement = true
	}
	if *remoteManagement {
		if !*management || config.Admin == nil {
			fmt.Fprintln(os.Stderr, "Garm management requires separate management authorization and explicit opt-in")
			os.Exit(1)
		}
		config.Admin.EnableExperimentalGarmManagement = true
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := serve(ctx, config, *provision); err != nil {
		fmt.Fprintln(os.Stderr, "gateway unavailable; no automatic recovery or deployment attempted")
		os.Exit(1)
	}
}
