package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/brokerinput"
	"github.com/ShortCipher5/garm-capacity-controller/internal/firstboot"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostbootstrap"
	"github.com/ShortCipher5/garm-capacity-controller/internal/onboarding"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type adminConfiguration struct {
	NativeEnrollmentRecipesFile        string `json:"native_enrollment_recipes_file,omitempty"`
	BrokerProfileRecipesFile           string `json:"broker_profile_recipes_file,omitempty"`
	allocatorRuntime                   *brokerinput.Service
	Rotation                           *rotationConfiguration     `json:"inventory_key_rotation,omitempty"`
	EnableRotation                     bool                       `json:"-"`
	Firstboot                          *firstbootConfiguration    `json:"isolated_inventory_firstboot,omitempty"`
	EnableFirstboot                    bool                       `json:"-"`
	Bootstrap                          *bootstrapConfiguration    `json:"prepared_host_bootstrap,omitempty"`
	EnableBootstrap                    bool                       `json:"-"`
	SSHInventory                       *sshInventoryConfiguration `json:"ssh_inventory,omitempty"`
	EnableSSHTargetManagement          bool                       `json:"-"`
	EnableSSHInventory                 bool                       `json:"-"`
	IncusOnboarding                    *onboardingConfiguration   `json:"incus_onboarding,omitempty"`
	EnableExperimentalOnboardingProbes bool                       `json:"-"`
	onboardingRuntime                  *onboarding.Manager
	GarmManagement                     *garmManagementConfiguration `json:"garm_management,omitempty"`
	EnableExperimentalGarmManagement   bool                         `json:"-"`
	garmRuntime                        adminview.GarmManager
	EnableExperimentalManagement       bool                 `json:"-"`
	Listen                             string               `json:"listen"`
	TLS                                securefiles.TLSFiles `json:"tls"`
	ManagementTokenFile                string               `json:"management_token_file,omitempty"`
	TokenFile                          string               `json:"token_file"`
	Assets                             string               `json:"assets"`
}

func newAdminServer(config *adminConfiguration, store *admission.SQLiteStore, provision bool) (*http.Server, func(), error) {
	noop := func() {}
	if config == nil {
		return nil, noop, nil
	}
	host, _, err := net.SplitHostPort(config.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return nil, noop, adminview.ErrConfiguration
	}
	token, err := securefiles.Read(config.TokenFile, 256)
	if err != nil {
		return nil, noop, err
	}
	credentials, err := securefiles.LoadTLS(config.TLS)
	if err != nil {
		return nil, noop, err
	}
	var root *os.Root
	cleanup := noop
	if config.Assets != "" {
		root, err = os.OpenRoot(config.Assets)
		if err != nil {
			return nil, noop, adminview.ErrConfiguration
		}
		cleanup = func() { root.Close() }
	}
	handler, err := adminview.New(store, strings.TrimSpace(string(token)), root, provision)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	handler.SetAllocatorStatusSource(config.allocatorRuntime)
	needsCredential := config.EnableExperimentalManagement || config.EnableExperimentalOnboardingProbes
	if needsCredential != (config.ManagementTokenFile != "") {
		cleanup()
		return nil, noop, adminview.ErrConfiguration
	}
	var managementToken string
	if needsCredential {
		writeToken, err := securefiles.Read(config.ManagementTokenFile, 256)
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		managementToken = strings.TrimSpace(string(writeToken))
	}
	if config.EnableExperimentalManagement {
		if err := handler.EnableManagement(managementToken); err != nil {
			cleanup()
			return nil, noop, err
		}
	}
	if config.NativeEnrollmentRecipesFile != "" {
		recipes, e := loadNativeEnrollmentRecipes(config.NativeEnrollmentRecipesFile)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetNativeEnrollmentRecipes(recipes); e != nil {
			cleanup()
			return nil, noop, e
		}
	}
	if config.BrokerProfileRecipesFile != "" {
		recipes, e := loadBrokerProfileRecipes(config.BrokerProfileRecipesFile)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetBrokerProfileRecipes(recipes); e != nil {
			cleanup()
			return nil, noop, e
		}
	}
	if config.GarmManagement != nil {
		if config.garmRuntime == nil {
			cleanup()
			return nil, noop, adminview.ErrConfiguration
		}
		if err := handler.SetGarmManager(config.garmRuntime, config.EnableExperimentalGarmManagement); err != nil {
			cleanup()
			return nil, noop, err
		}
	} else if config.EnableExperimentalGarmManagement {
		cleanup()
		return nil, noop, adminview.ErrConfiguration
	}
	if config.IncusOnboarding != nil {
		if config.onboardingRuntime == nil {
			cleanup()
			return nil, noop, adminview.ErrConfiguration
		}
		probeToken := ""
		if config.EnableExperimentalOnboardingProbes {
			if managementToken == "" {
				cleanup()
				return nil, noop, adminview.ErrConfiguration
			}
			probeToken = managementToken
		}
		if err := handler.SetOnboarding(config.onboardingRuntime, probeToken); err != nil {
			cleanup()
			return nil, noop, err
		}
	} else if config.EnableExperimentalOnboardingProbes {
		cleanup()
		return nil, noop, adminview.ErrConfiguration
	}
	if config.EnableSSHInventory {
		service, sshToken, e := newSSHInventory(config.SSHInventory, store)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetSSHInventory(service, sshToken); e != nil {
			cleanup()
			return nil, noop, e
		}
	}
	if config.EnableSSHTargetManagement {
		if !config.EnableSSHInventory {
			cleanup()
			return nil, noop, adminview.ErrConfiguration
		}
		registry, token, e := newSSHTargetRegistry(config.SSHInventory, store)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetSSHTargetManagement(registry, token); e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = store.InitializeSSHTargets(context.Background()); e != nil {
			cleanup()
			return nil, noop, e
		}
		// Both services share the same immutable deployment policy and durable store.
		service, sshToken, e := newSSHInventory(config.SSHInventory, store)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		service.SetRegistry(registry)
		if e = handler.SetSSHInventory(service, sshToken); e != nil {
			cleanup()
			return nil, noop, e
		}
	}
	if config.EnableBootstrap {
		targets, resolve, token, e := loadBootstrap(config)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = store.InitializeBootstrap(context.Background()); e != nil {
			cleanup()
			return nil, noop, e
		}
		service, e := hostbootstrap.NewService(targets, store, resolve)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetBootstrap(service, token); e != nil {
			service.Close()
			cleanup()
			return nil, noop, e
		}
		oldCleanup := cleanup
		cleanup = func() { service.Close(); oldCleanup() }
	}
	if config.EnableFirstboot {
		targets, resolve, token, e := loadFirstboot(config)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = store.InitializeFirstboot(context.Background()); e != nil {
			cleanup()
			return nil, noop, e
		}
		service, e := firstboot.NewService(targets, store, resolve)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetFirstboot(service, token); e != nil {
			service.Close()
			cleanup()
			return nil, noop, e
		}
		oldCleanup := cleanup
		cleanup = func() { service.Close(); oldCleanup() }
	}
	if config.EnableRotation {
		targets, admin, inventory, token, e := loadManagedRotation(config)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		registry, _, e := newSSHTargetRegistry(config.SSHInventory, store)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = store.InitializeManagedRotation(context.Background()); e != nil {
			cleanup()
			return nil, noop, e
		}
		service, e := firstboot.NewManagedRotationService(targets, store, registry, admin, inventory)
		if e != nil {
			cleanup()
			return nil, noop, e
		}
		if e = handler.SetManagedRotation(service, token); e != nil {
			service.Close()
			cleanup()
			return nil, noop, e
		}
		old := cleanup
		cleanup = func() { service.Close(); old() }
	}
	handler.Audit = func(event string) { log.Print(event) }
	return &http.Server{Addr: config.Listen, Handler: handler, TLSConfig: credentials, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(redactedConnectionLog{}, "", 0)}, cleanup, nil
}
