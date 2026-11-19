// capacity-gateway is an experimental credential-owning service. It is
// read-only by default; provisioning requires an explicit startup opt-in.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandobserver"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gateway"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostmetrics"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type hostConfiguration struct {
	Endpoint string               `json:"endpoint"`
	Project  string               `json:"project"`
	TLS      securefiles.TLSFiles `json:"tls"`
}
type garmConfiguration struct {
	ManagedProxy      bool                 `json:"managed_proxy,omitempty"`
	ProxyServerKeyPin string               `json:"proxy_server_key_pin,omitempty"`
	Endpoint          string               `json:"endpoint"`
	TokenFile         string               `json:"token_file"`
	TLS               securefiles.TLSFiles `json:"tls"`
	Providers         []string             `json:"providers"`
	ScaleSets         map[uint64]string    `json:"scalesets"`
}
type configuration struct {
	DemandObserverConfigFile      string                                   `json:"demand_observer_config_file,omitempty"`
	NativeRetirement              *nativeRetirementConfiguration           `json:"native_retirement,omitempty"`
	EnableNativeRetirement        bool                                     `json:"-"`
	NativeClaims                  *nativeClaimConfiguration                `json:"native_claims,omitempty"`
	JobObserver                   *jobObserverConfiguration                `json:"job_observer,omitempty"`
	EnableJobObserver             bool                                     `json:"-"`
	NativeStock                   map[string]nativeStockPoolConfiguration  `json:"native_stock,omitempty"`
	AllocatorAdvisory             *allocatorAdvisoryConfiguration          `json:"allocator_advisory,omitempty"`
	AllocatorLoop                 *allocatorLoopConfiguration              `json:"allocator_loop,omitempty"`
	StockProviderScopes           []gatewayrpc.StockScope                  `json:"stock_provider_scopes,omitempty"`
	EnableStockProvider           bool                                     `json:"-"`
	PreparationPools              map[string]gateway.PreparationPoolPolicy `json:"preparation_pools,omitempty"`
	SourceFingerprints            []string                                 `json:"source_fingerprints,omitempty"`
	BootstrapMode                 string                                   `json:"bootstrap_mode,omitempty"`
	Admin                         *adminConfiguration                      `json:"admin,omitempty"`
	Listen                        string                                   `json:"listen"`
	Controller                    string                                   `json:"controller"`
	Database                      string                                   `json:"database"`
	IdentityFile                  string                                   `json:"identity_file"`
	InitialPolicy                 admission.Config                         `json:"initial_policy"`
	Pools                         map[string]gateway.PoolPolicy            `json:"pools"`
	Hosts                         map[string]hostConfiguration             `json:"hosts"`
	Garm                          garmConfiguration                        `json:"garm"`
	ServerTLS                     securefiles.TLSFiles                     `json:"server_tls"`
	ProviderFingerprints          []string                                 `json:"provider_fingerprints"`
	ProviderInspectionFingerprint string                                   `json:"provider_inspection_fingerprint,omitempty"`
}

type redactedConnectionLog struct{}

func (redactedConnectionLog) Write(message []byte) (int, error) {
	log.Print("gateway TLS/HTTP connection error")
	return len(message), nil
}

func serve(ctx context.Context, config configuration, provision, monitoring bool) error {
	if config.NativeClaims != nil || config.NativeRetirement != nil {
		return errNativeClaimsRuntime
	} // Refuse before any listener or key read.
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
	return serveOnListener(ctx, config, provision, monitoring, listener)
}

// A supplied listener is used by the offline integration tests; production calls
// still validate and create an explicit loopback listener above.
func serveOnListener(ctx context.Context, config configuration, provision, monitoring bool, listener net.Listener) error {
	return serveOnListeners(ctx, config, provision, monitoring, listener, nil)
}

func serveOnListeners(ctx context.Context, config configuration, provision, monitoring bool, listener, adminListener net.Listener) error {
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return gatewayrpc.ErrConfiguration
	}
	if provision && (config.BootstrapMode != "gateway-owned-default-cloudinit/v1" || len(config.PreparationPools) != len(config.Pools) || len(config.SourceFingerprints) == 0) {
		return gatewayrpc.ErrConfiguration
	}
	store, err := admission.OpenRuntimeSQLite(config.Database, config.IdentityFile)
	if err != nil {
		return err
	}
	defer store.Close()
	claims, err := buildNativeClaims(ctx, config, store)
	if err != nil {
		return err
	}
	retirement, err := buildNativeRetirement(ctx, config, store)
	if err != nil {
		return err
	}
	if config.Admin != nil {
		config.Admin.nativeRetirementRuntime = retirement
	}
	observer, closeObserver, err := buildJobObserver(config, store)
	if err != nil {
		return err
	}
	defer closeObserver()
	if config.Admin != nil {
		config.Admin.jobObserverRuntime = observer
	}
	advisory, closeAdvisory, err := configuredAllocatorAdvisory(ctx, config, store)
	if err != nil {
		return err
	}
	defer closeAdvisory()
	if config.Admin != nil {
		config.Admin.allocatorAdvisory = advisory
	}
	allocator, closeAllocator, err := startConfiguredAllocatorLoop(ctx, config, store)
	if err != nil {
		return err
	}
	defer closeAllocator()
	if config.Admin != nil {
		config.Admin.allocatorRuntime = allocator
	}

	var demand *demandobserver.Service
	if config.DemandObserverConfigFile != "" {
		if config.Admin == nil {
			return demandobserver.ErrObserver
		}
		dc, e := demandobserver.Load(config.DemandObserverConfigFile)
		if e != nil {
			return e
		}
		demand, e = demandobserver.NewService(dc)
		if e != nil {
			return e
		}
		config.Admin.demandObserverRuntime = demand
	}
	backends := map[string]gateway.Backend{}
	monitors := map[string]hostmetrics.Source{}
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
		monitors[id] = client
	}
	var collector *hostmetrics.Collector
	if monitoring {
		collector, err = hostmetrics.New(monitors, store, hostmetrics.Options{Log: func(event string) { log.Print(event) }})
		if err != nil {
			return err
		}
	}
	garm, token, err := inventoryClient(config, provision)
	if err != nil {
		return err
	}
	defer garm.Close()
	options := []gateway.PreparationSettings{}
	if len(config.PreparationPools) > 0 {
		options = append(options, gateway.PreparationSettings{Endpoint: config.Garm.Endpoint, Providers: config.Garm.Providers, Policies: config.PreparationPools, Source: garm})
	}
	f, err := gateway.New(ctx, store, config.Controller, config.Pools, backends, options...)
	if err != nil {
		return err
	}
	handler, err := gatewayrpc.NewHandler(f, config.ProviderFingerprints, provision, config.SourceFingerprints)
	if err != nil {
		return err
	}
	handler.Audit = func(event string) { log.Print(event) }
	nativeStock, closeNative, err := buildNativeStock(ctx, store, config, string(bytes.TrimSpace(token)), nil)
	if err != nil {
		return err
	}
	defer closeNative()
	rpcHandler, err := stockProviderHandler(config, handler, nativeStock)
	if err != nil {
		return err
	}
	serverCredentials, err := securefiles.LoadTLS(config.ServerTLS)
	if err != nil {
		return err
	}
	rpcHandler, err = providerProfileHandler(config, rpcHandler, store, serverCredentials.Certificates[0])
	if err != nil {
		return err
	}
	tlsConfig, err := gatewayrpc.ServerTLS(serverCredentials.Certificates[0], serverCredentials.RootCAs)
	if err != nil {
		return err
	}
	// No secrets, request bodies, URLs, panic values or provider output in logs.
	server := &http.Server{Addr: config.Listen, Handler: rpcHandler, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 12 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(redactedConnectionLog{}, "", 0)}
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
	onboardingManager, closeOnboarding, err := newOnboardingControl(config.Admin, store)
	if err != nil {
		return err
	}
	defer closeOnboarding()
	if config.Admin != nil {
		config.Admin.onboardingRuntime = onboardingManager
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
	// Start only after all local handlers/listeners are validated and the
	// initial inventory reconciliation has completed. The service still reads
	// canonical readiness itself; a failed inventory pass cannot authorize it.
	closePolicyCaps, err := startPolicyCapReconciler(runctx, config, store, remoteManager)
	if err != nil {
		return err
	}
	defer closePolicyCaps()
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
	monitorDone := make(chan struct{})
	if collector != nil {
		go func() {
			defer close(monitorDone)
			collector.Run(runctx)
		}()
	} else {
		close(monitorDone)
	}
	retirementDone := make(chan struct{})
	if retirement != nil {
		go func() { defer close(retirementDone); _ = retirement.Run(runctx) }()
	} else {
		close(retirementDone)
	}
	observerDone := make(chan struct{})
	if observer != nil {
		go func() { defer close(observerDone); _ = observer.Run(runctx) }()
	} else {
		close(observerDone)
	}
	demandDone := make(chan struct{})
	if demand != nil {
		go func() { defer close(demandDone); _ = demand.Run(runctx) }()
	} else {
		close(demandDone)
	}
	defer func() {
		cancel()
		<-loopDone
		<-monitorDone
		<-observerDone
		<-retirementDone
		<-demandDone
	}()
	var claimsDone chan error
	if claims != nil {
		claimsDone = make(chan error, 1)
		go func() { e := claims.Run(runctx); claimsDone <- e; cancel() }()
	}
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
	if claimsDone != nil {
		claimErr := <-claimsDone
		if !errors.Is(claimErr, context.Canceled) {
			return claimErr
		}
	}
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
	checkConfig := flag.Bool("check-config", false, "check local configuration only; does not establish runtime or deployment readiness")
	retirement := flag.Bool("allow-experimental-native-retirement", false, "configure canonical STOP/DELETE orchestration; active runtime still refused, no release")
	jobObserver := flag.Bool("allow-job-observer", false, "enable positive authenticated job/root journal correlation only; no retirement or release")
	stockProvider := flag.Bool("allow-experimental-stock-provider", false, "enable distinct post-JIT stock-provider admission; no original-preparation guarantee or physical release")
	provision := flag.Bool("allow-experimental-provisioning", false, "allow one-shot creates after successful reconciliation; requires qualified exclusive scopes and fenced old writers")
	management := flag.Bool("allow-experimental-management", false, "allow separately authorized ledger-policy edits; not Garm pool registration or runner drain")
	remoteManagement := flag.Bool("allow-experimental-garm-management", false, "allow separately journalled scoped Garm settings writes; requires qualified gateway routing, exclusive external settings writer, separate upstream token and management opt-in")
	onboardingProbes := flag.Bool("allow-experimental-onboarding-probes", false, "allow audited read-only probes of allowlisted Incus transport profiles; never enrolls or qualifies a backend")
	sshTargetManagement := flag.Bool("allow-ssh-target-management", false, "allow separately authorized durable SSH target registration within deployment scopes; no installation or activation")
	rotation := flag.Bool("allow-inventory-key-rotation", false, "allow separately approved remote inventory key/session revocation and atomic local target replacement")
	nativeRegistration := flag.Bool("allow-native-target-registration", false, "allow scoped new-target configuration, discovery, bundle assembly and separately approved disabled preparation; never activation")
	nativeAssembly := flag.Bool("allow-native-bundle-assembly", false, "allow explicit fixed-recipe local assembly and reviewed preparation registry reload; no SSH or activation")
	nativePlanning := flag.Bool("allow-native-discovery-planning", false, "allow separately approved SSH discovery and saved native resource configuration; no activation")
	nativeAssets := flag.Bool("allow-native-asset-staging", false, "allow separately approved runtime asset transfer for exact existing native preparation targets; never activation")
	nativePreparation := flag.Bool("allow-native-owner-preparation", false, "allow separately approved private configuration transfer and disabled owner preparation; never activation")
	firstboot := flag.Bool("allow-isolated-inventory-firstboot", false, "allow separately approved full-admin first-time isolated listener installation; changes host account, service and owned firewall")
	bootstrap := flag.Bool("allow-prepared-host-bootstrap", false, "allow separately authorized installation on deployment-approved prepared hosts; no account creation or sshd changes")
	sshInventory := flag.Bool("allow-read-only-ssh-inventory", false, "allow separately authorized pinned SSH inventory only; no installation or enrollment")
	monitoring := flag.Bool("allow-experimental-monitoring", false, "allow periodic host utilization sampling into the admission usage history; monitoring data never gates admission, fits or retirement")
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
	if *onboardingProbes {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "onboarding probes require separate admin authorization and explicit opt-in")
			os.Exit(1)
		}
		config.Admin.EnableExperimentalOnboardingProbes = true
	}
	if *sshInventory {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "SSH inventory requires separate admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableSSHInventory = true
	}
	if *sshTargetManagement {
		if config.Admin == nil || !*sshInventory {
			fmt.Fprintln(os.Stderr, "SSH target management requires read-only SSH inventory opt-in")
			os.Exit(1)
		}
		config.Admin.EnableSSHTargetManagement = true
	}
	if *rotation {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "rotation requires admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableRotation = true
	}
	if *nativeRegistration {
		if config.Admin == nil || !*sshInventory || *nativeAssembly || *nativePlanning || *nativePreparation {
			fmt.Fprintln(os.Stderr, "native target registration requires SSH inventory and separate dynamic configuration, without static native workflow flags")
			os.Exit(1)
		}
		config.Admin.EnableNativeRegistration = true
	}
	if *nativeAssembly {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "native assembly requires admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableNativeAssembly = true
	}
	if *nativePlanning {
		if config.Admin == nil || !*sshInventory {
			fmt.Fprintln(os.Stderr, "native planning requires SSH inventory and separate admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableNativePlanning = true
	}
	if *nativePreparation {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "native preparation requires separate admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableNativePreparation = true
	}
	if *nativeAssets {
		if config.Admin == nil || (!*nativePreparation && !*nativeRegistration) {
			fmt.Fprintln(os.Stderr, "native asset staging requires existing native preparation or registration configuration")
			os.Exit(1)
		}
		config.Admin.EnableNativeAssets = true
	}
	if *firstboot {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "firstboot requires separate admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableFirstboot = true
	}
	config.EnableStockProvider = *stockProvider
	config.EnableJobObserver = *jobObserver
	config.EnableNativeRetirement = *retirement
	if *bootstrap {
		if config.Admin == nil {
			fmt.Fprintln(os.Stderr, "bootstrap requires separate admin configuration")
			os.Exit(1)
		}
		config.Admin.EnableBootstrap = true
	}
	if *checkConfig {
		if checkStaticConfig(config, *provision) != nil {
			fmt.Fprintln(os.Stderr, "gateway static configuration check failed")
			os.Exit(1)
		}
		fmt.Println("gateway selected static checks passed; full configuration, runtime policy, connectivity, safety and deployment readiness not checked")
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := serve(ctx, config, *provision, *monitoring); err != nil {
		fmt.Fprintln(os.Stderr, "gateway unavailable; no automatic recovery or deployment attempted")
		os.Exit(1)
	}
}
