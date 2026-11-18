package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type nativeEnrollmentOwner struct {
	Endpoint        string               `json:"endpoint"`
	TLS             securefiles.TLSFiles `json:"tls"`
	OwnerClientLeaf string               `json:"owner_client_leaf"`
	OwnerServerLeaf string               `json:"owner_server_leaf"`
	Policy          nativeapi.PolicySpec `json:"policy"`
}
type nativeEnrollmentConfig struct {
	Owners []nativeEnrollmentOwner `json:"owners"`
}

// enrollNative is an explicit local operator workflow. It never promotes
// configuration to evidence, rebinds an existing host, completes reconciliation,
// invokes native operations, or grants execution/release authority.
func enrollNative(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("enroll-native", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing private runtime ledger")
	identity := flags.String("identity-file", "", "independent private runtime identity")
	config := flags.String("config", "", "private pinned owner and policy configuration")
	initial := flags.Bool("bind-initial-hosts", false, "bind all configured hosts only on a wholly unbound, uncharged, not-ready ledger")
	if e := flags.Parse(args); errors.Is(e, flag.ErrHelp) {
		fmt.Fprint(out, usage)
		return 0
	} else if e != nil || flags.NArg() != 0 || *source == "" || *identity == "" || *config == "" {
		fmt.Fprintln(diagnostic, "invalid enroll-native arguments; existing source, independent identity and private configuration required")
		return 2
	}
	failed := func(committed int) int {
		fmt.Fprintf(diagnostic, "native enrollment incomplete; %d domain records confirmed in this attempt. Initial identity/barrier writes may have committed. Preserve ledger and inspect existing bindings before retrying without initial-host mode; no readiness, rebind, native operation or release authorized.\n", committed)
		return 1
	}
	if ctx == nil || ctx.Err() != nil {
		return failed(0)
	}
	raw, e := securefiles.Read(*config, 1<<20)
	if e != nil {
		return failed(0)
	}
	var c nativeEnrollmentConfig
	if gatewayrpc.Decode(bytes.NewReader(raw), &c) != nil || len(c.Owners) == 0 || len(c.Owners) > 1000 {
		return failed(0)
	}
	policies := make([]nativeapi.Policy, len(c.Owners))
	trusts := make([]*tls.Config, len(c.Owners))
	seenHosts := map[string]bool{}
	seenDomains := map[string]bool{}
	controller := ""
	for i, owner := range c.Owners {
		p, err := nativeapi.NewPolicy(owner.Policy)
		if err != nil || !p.Protected() || seenHosts[p.Host()] || seenDomains[p.DomainID()] {
			return failed(0)
		}
		if controller != "" && controller != p.Controller() {
			return failed(0)
		}
		controller = p.Controller()
		validPin := func(v string) bool {
			b, e := hex.DecodeString(v)
			return e == nil && len(b) == 32 && hex.EncodeToString(b) == v
		}
		u, err := url.Parse(owner.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !validPin(owner.OwnerClientLeaf) || !validPin(owner.OwnerServerLeaf) || owner.OwnerClientLeaf == owner.OwnerServerLeaf {
			return failed(0)
		}
		trust, err := securefiles.LoadTLS(owner.TLS)
		if err != nil {
			return failed(0)
		}
		if leaf := nativeidentity.Fingerprint(trust.Certificates[0].Certificate[0]); leaf == owner.OwnerClientLeaf || leaf == owner.OwnerServerLeaf {
			return failed(0)
		}
		policies[i], trusts[i] = p, trust
		seenHosts[p.Host()], seenDomains[p.DomainID()] = true, true
	}
	s, e := admission.OpenRuntimeSQLite(*source, *identity)
	if e != nil {
		return failed(0)
	}
	defer s.Close()
	state, e := s.ReadBrokerGrantState(ctx)
	if e != nil {
		return failed(0)
	}
	for _, p := range policies {
		if _, ok := state.Config.Hosts[p.Host()]; !ok {
			return failed(0)
		}
	}
	var fence admission.Fence
	if *initial {
		if state.Ready || len(state.HostBindings) != 0 || len(state.Reservations) != 0 || len(state.Inventory) != 0 || len(state.Grants) != 0 || len(state.PoolProfiles) != 0 || len(state.DomainOverheads) != 0 || len(state.ArtifactOverheads) != 0 || len(c.Owners) != len(state.Config.Hosts) {
			return failed(0)
		}
		hosts := make([]string, 0, len(policies))
		for _, p := range policies {
			hosts = append(hosts, p.Host())
		}
		fence, e = s.BeginInitialNativeEnrollment(ctx, state.Sequence, hosts)
		if e != nil {
			return failed(0)
		}
	} else {
		for _, p := range policies {
			if _, ok := state.HostBindings[p.Host()]; !ok {
				return failed(0)
			}
		}
	}
	observations := make([]nativeidentity.Observation, len(c.Owners))
	bindings := make([]admission.HostBinding, len(c.Owners))
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for i, owner := range c.Owners {
		p := policies[i]
		observations[i], e = nativeidentity.Observe(readCtx, nativeidentity.Configuration{Endpoint: owner.Endpoint, TLS: trusts[i], OwnerServerLeaf: owner.OwnerServerLeaf, OwnerClientLeaf: owner.OwnerClientLeaf, PolicyDigest: p.Digest(), DomainID: p.DomainID()})
		if e != nil {
			return failed(0)
		}
		bindings[i], e = admission.ObservedNativeHostBinding(p.Host(), observations[i])
		if e != nil {
			return failed(0)
		}
		if !*initial && !state.HostBindings[p.Host()].Matches(bindings[i]) {
			return failed(0)
		}
	}
	if *initial {
		if e = s.BindHostIdentities(ctx, fence, bindings); e != nil {
			return failed(0)
		}
	}
	committed := 0
	for i, owner := range c.Owners {
		p := policies[i]
		host, err := s.LookupHostBinding(ctx, p.Host())
		if err != nil || !host.Matches(bindings[i]) {
			return failed(committed)
		}
		_, err = s.EnrollNativeDomain(ctx, admission.NativeDomainSpec{Controller: p.Controller(), Host: p.Host(), HostBindingSequence: host.Sequence, DomainID: p.DomainID(), PolicyDigest: p.Digest(), OwnerClientLeaf: owner.OwnerClientLeaf, OwnerServerLeaf: owner.OwnerServerLeaf}, observations[i])
		if err != nil {
			return failed(committed)
		}
		committed++
	}
	fmt.Fprintf(out, "Confirmed %d immutable native domain enrollment records from actual pinned owner observations. No readiness, host rebind, native operation, grant, capacity exemption or release authorized.\n", committed)
	return 0
}
