package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontrol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativecontroller"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

var errNativeClaimsRuntime = nativecontroller.ErrUnqualified

type nativeClaimConfiguration struct {
	Listen                 string               `json:"listen"`
	TLS                    securefiles.TLSFiles `json:"tls"`
	DomainBindingSequences []int64              `json:"domain_binding_sequences"`
}

type nativeClaimService struct {
	address string
	server  *http.Server
}

func validateNativeClaimConfiguration(c *nativeClaimConfiguration, main, admin string) error {
	if c == nil {
		return nil
	}
	host, port, e := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	n, pe := strconv.Atoi(port)
	if e != nil || ip == nil || !ip.IsLoopback() || pe != nil || n < 1 || n > 65535 || c.Listen == main || c.Listen == admin || len(c.DomainBindingSequences) == 0 || len(c.DomainBindingSequences) > 256 {
		return nativecontrol.ErrControl
	}
	seen := map[int64]bool{}
	for _, seq := range c.DomainBindingSequences {
		if seq <= 0 || seen[seq] {
			return nativecontrol.ErrControl
		}
		seen[seq] = true
	}
	return nil
}

// buildNativeClaims is separate from provider/admin handlers. A configured
// production store refuses BEFORE reading keys, loading enrollment or binding
// any owner listener. There is no boolean or configuration bypass.
func buildNativeClaims(ctx context.Context, config configuration, store *admission.SQLiteStore) (*nativeClaimService, error) {
	c := config.NativeClaims
	if c == nil {
		return nil, nil
	}
	if ctx == nil || ctx.Err() != nil || store == nil {
		return nil, nativecontrol.ErrControl
	}
	if store.RuntimeLineageBound() {
		return nil, errNativeClaimsRuntime
	}
	admin := ""
	if config.Admin != nil {
		admin = config.Admin.Listen
	}
	if e := validateNativeClaimConfiguration(c, config.Listen, admin); e != nil {
		return nil, e
	}
	forbidden := map[string]bool{}
	if config.ProviderInspectionFingerprint != "" {
		forbidden[config.ProviderInspectionFingerprint] = true
	}
	for _, leaf := range config.ProviderFingerprints {
		forbidden[leaf] = true
	}
	for _, leaf := range config.SourceFingerprints {
		forbidden[leaf] = true
	}
	for _, scope := range config.StockProviderScopes {
		forbidden[scope.ClientFingerprint] = true
	}
	credentials, e := securefiles.LoadTLS(c.TLS)
	if e != nil {
		return nil, e
	}
	claimLeaf := nativeidentity.Fingerprint(credentials.Certificates[0].Certificate[0])
	// A dedicated server leaf cannot also be a provider/admin server or client.
	if forbidden[claimLeaf] {
		return nil, nativecontrol.ErrControl
	}
	for _, files := range []securefiles.TLSFiles{config.ServerTLS} {
		v, e := securefiles.LoadTLS(files)
		if e != nil {
			return nil, e
		}
		leaf := nativeidentity.Fingerprint(v.Certificates[0].Certificate[0])
		if leaf == claimLeaf {
			return nil, nativecontrol.ErrControl
		}
		forbidden[leaf] = true
	}
	if config.Admin != nil {
		v, e := securefiles.LoadTLS(config.Admin.TLS)
		if e != nil {
			return nil, e
		}
		leaf := nativeidentity.Fingerprint(v.Certificates[0].Certificate[0])
		if leaf == claimLeaf {
			return nil, nativecontrol.ErrControl
		}
		forbidden[leaf] = true
	}
	// Outbound controller/observer clients are different purposes from an
	// owner consuming a claim, even when their certificates share a CA.
	var outgoing []securefiles.TLSFiles
	for _, pool := range config.NativeStock {
		for _, host := range pool.Hosts {
			outgoing = append(outgoing, host.OwnerTLS)
		}
	}
	if config.NativeRetirement != nil {
		for _, host := range config.NativeRetirement.Owners {
			outgoing = append(outgoing, host.OwnerTLS)
		}
	}
	if config.JobObserver != nil {
		for _, host := range config.JobObserver.Owners {
			outgoing = append(outgoing, host.OwnerTLS)
		}
	}
	for _, host := range config.Hosts {
		outgoing = append(outgoing, host.TLS)
	}
	if config.Garm.TLS.Certificate != "" || config.Garm.TLS.Key != "" {
		outgoing = append(outgoing, config.Garm.TLS)
	}
	if config.Admin != nil {
		if config.Admin.GarmManagement != nil {
			v := config.Admin.GarmManagement.TLS
			if v.Certificate != "" || v.Key != "" {
				outgoing = append(outgoing, v)
			}
		}
		if config.Admin.IncusOnboarding != nil {
			for _, v := range config.Admin.IncusOnboarding.Profiles {
				outgoing = append(outgoing, v.TLS)
			}
			for _, v := range config.Admin.IncusOnboarding.Targets {
				outgoing = append(outgoing, v.TLS)
			}
		}
	}
	for _, files := range outgoing {
		v, e := securefiles.LoadTLS(files)
		if e != nil {
			return nil, e
		}
		leaf := nativeidentity.Fingerprint(v.Certificates[0].Certificate[0])
		if leaf == claimLeaf {
			return nil, nativecontrol.ErrControl
		}
		forbidden[leaf] = true
	}
	if config.Admin != nil && config.Admin.BrokerProfileRecipesFile != "" {
		recipes, e := loadBrokerProfileRecipes(config.Admin.BrokerProfileRecipesFile)
		if e != nil {
			return nil, e
		}
		for _, recipe := range recipes {
			if recipe.Transport.TLS == nil {
				continue
			}
			for _, cert := range recipe.Transport.TLS.Certificates {
				if len(cert.Certificate) == 0 {
					return nil, nativecontrol.ErrControl
				}
				leaf := nativeidentity.Fingerprint(cert.Certificate[0])
				if leaf == claimLeaf {
					return nil, nativecontrol.ErrControl
				}
				forbidden[leaf] = true
			}
		}
	}
	if config.Admin != nil && config.Admin.NativeEnrollmentRecipesFile != "" {
		recipes, e := loadNativeEnrollmentRecipes(config.Admin.NativeEnrollmentRecipesFile)
		if e != nil {
			return nil, e
		}
		for _, recipe := range recipes {
			for _, owner := range recipe.Owners {
				if owner.Transport.TLS == nil || len(owner.Transport.TLS.Certificates) != 1 {
					return nil, nativecontrol.ErrControl
				}
				cert := owner.Transport.TLS.Certificates[0]
				if len(cert.Certificate) == 0 {
					return nil, nativecontrol.ErrControl
				}
				leaf := nativeidentity.Fingerprint(cert.Certificate[0])
				if leaf == claimLeaf {
					return nil, nativecontrol.ErrControl
				}
				forbidden[leaf] = true
			}
		}
	}
	allowed := map[string]bool{}
	for _, seq := range c.DomainBindingSequences {
		binding, e := store.LookupNativeDomainBinding(ctx, seq)
		if e != nil || binding.Spec.Controller != config.Controller || binding.Spec.OwnerClientLeaf == claimLeaf || binding.Spec.OwnerServerLeaf == claimLeaf || forbidden[binding.Spec.OwnerClientLeaf] || forbidden[binding.Spec.OwnerServerLeaf] {
			return nil, nativecontrol.ErrControl
		}
		// Canonical enrollment makes owner client/server leaves globally unique.
		// A configured leaf therefore cannot silently cover a second domain.
		allowed[binding.Spec.OwnerClientLeaf] = true
	}
	credentials.ClientCAs = credentials.RootCAs.Clone()
	server, e := newNativeClaimServer(store, credentials, allowed)
	if e != nil {
		return nil, e
	}
	return &nativeClaimService{c.Listen, server}, nil
}

func newNativeClaimServer(store *admission.SQLiteStore, credentials *tls.Config, owners map[string]bool) (*http.Server, error) {
	if store == nil || len(owners) == 0 {
		return nil, nativecontrol.ErrControl
	}
	if store.RuntimeLineageBound() {
		return nil, errNativeClaimsRuntime
	}
	server, e := nativecontrol.NewClaimServer(store, credentials)
	if e != nil {
		return nil, e
	}
	allowed := map[string]bool{}
	for leaf, enabled := range owners {
		if !enabled || len(leaf) != 64 {
			return nil, nativecontrol.ErrControl
		}
		allowed[leaf] = true
	}
	canonical := server.Handler
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, e := admission.AuthenticateNativeOwner(r.TLS); e != nil || !allowed[nativeidentity.Fingerprint(r.TLS.PeerCertificates[0].Raw)] {
			http.Error(w, "native owner scope required", http.StatusUnauthorized)
			return
		}
		canonical.ServeHTTP(w, r)
	})
	server.ErrorLog = log.New(redactedConnectionLog{}, "", 0)
	return server, nil
}

func (s *nativeClaimService) Run(ctx context.Context) error {
	if s == nil || s.server == nil || ctx == nil || ctx.Err() != nil {
		return nativecontrol.ErrControl
	}
	listener, e := net.Listen("tcp", s.address)
	if e != nil {
		return nativecontrol.ErrControl
	}
	return serveNativeClaimListener(ctx, s.server, listener)
}
func serveNativeClaimListener(ctx context.Context, server *http.Server, listener net.Listener) error {
	if ctx == nil || server == nil || listener == nil {
		return nativecontrol.ErrControl
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	select {
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nativecontrol.ErrControl
		}
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		<-done
		return ctx.Err()
	}
}
