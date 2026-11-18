package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

type profileRegistrationConfig struct {
	Endpoint  string                             `json:"endpoint"`
	CAFile    string                             `json:"ca_file"`
	TokenFile string                             `json:"token_file"`
	Request   admission.BrokerPoolProfileRequest `json:"request"`
	Domains   []struct {
		Sequence int64                `json:"sequence"`
		Policy   nativeapi.PolicySpec `json:"policy"`
	} `json:"domains"`
}

func registerProfile(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("register-pool-profile", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing private runtime ledger")
	identity := flags.String("identity-file", "", "independent private ledger identity")
	configPath := flags.String("config", "", "private registration configuration")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(out, usage)
		return 0
	} else if err != nil || flags.NArg() != 0 || *source == "" || *identity == "" || *configPath == "" {
		fmt.Fprintln(diagnostic, "invalid register-pool-profile arguments; existing source, independent identity and private config required")
		return 2
	}
	fail := func() int {
		fmt.Fprintln(diagnostic, "profile registration unavailable; verify private configuration, runtime identity, observed stock ownership, existing native enrollment and saved size; no activation requested")
		return 1
	}
	if ctx == nil || ctx.Err() != nil {
		return fail()
	}
	raw, err := securefiles.Read(*configPath, 1<<20)
	if err != nil {
		return fail()
	}
	var c profileRegistrationConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || len(c.Domains) == 0 || len(c.Domains) > 1000 {
		return fail()
	}
	policies := map[int64]nativeapi.Policy{}
	for _, domain := range c.Domains {
		if domain.Sequence <= 0 {
			return fail()
		}
		if _, exists := policies[domain.Sequence]; exists {
			return fail()
		}
		p, err := nativeapi.NewPolicy(domain.Policy)
		if err != nil {
			return fail()
		}
		policies[domain.Sequence] = p
	}
	ca, err := securefiles.Read(c.CAFile, 1<<20)
	if err != nil {
		return fail()
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return fail()
	}
	secret, err := securefiles.Read(c.TokenFile, 16384)
	if err != nil {
		return fail()
	}
	token := strings.TrimSpace(string(secret))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return fail()
	}
	store, err := admission.OpenRuntimeSQLite(*source, *identity)
	if err != nil {
		return fail()
	}
	defer store.Close()
	observation, err := stockidentity.Observe(ctx, stockidentity.Configuration{Endpoint: c.Endpoint, Token: token, Provider: c.Request.Authority.ProviderName, Pool: c.Request.Authority.PoolID, TLS: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}})
	if err != nil {
		return fail()
	}
	// Empty expected source is derived from the authenticated configured
	// transport, never from the upstream response or caller-provided hash.
	if c.Request.SourceIdentity == "" {
		c.Request.SourceIdentity = observation.SourceIdentity()
	}
	profile, err := store.RegisterBrokerPoolProfile(ctx, c.Request, observation, policies)
	if err != nil {
		return fail()
	}
	fmt.Fprintf(out, "Recorded immutable pool profile revision %d. Ownership observed; no grants, pool changes, native activation or capacity release.\n", profile.Sequence)
	return 0
}
