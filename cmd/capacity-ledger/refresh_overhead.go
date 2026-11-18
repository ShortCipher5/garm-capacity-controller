package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/overheadrefresh"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

type overheadRefreshConfig struct {
	Targets []struct {
		BindingSequence int64                `json:"domain_binding_sequence"`
		Endpoint        string               `json:"endpoint"`
		TLS             securefiles.TLSFiles `json:"tls"`
		Kind            string               `json:"kind"`
	} `json:"targets"`
}

func refreshOverhead(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("refresh-overhead", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "existing runtime ledger")
	identity := flags.String("identity-file", "", "independent private identity")
	config := flags.String("config", "", "private owner configuration")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(out, usage)
		return 0
	} else if err != nil || flags.NArg() != 0 || *source == "" || *identity == "" || *config == "" {
		fmt.Fprintln(diagnostic, "invalid refresh-overhead arguments")
		return 2
	}
	fail := func() int {
		fmt.Fprintln(diagnostic, "overhead refresh incomplete; preserve all prior charges and any reported committed sequences; no capacity release or activation occurred")
		return 1
	}
	raw, err := securefiles.Read(*config, 1<<20)
	if err != nil {
		return fail()
	}
	var c overheadRefreshConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || len(c.Targets) == 0 || len(c.Targets) > 100 {
		return fail()
	}
	targets := make([]overheadrefresh.Target, 0, len(c.Targets))
	for _, t := range c.Targets {
		trust, e := securefiles.LoadTLS(t.TLS)
		if e != nil {
			return fail()
		}
		targets = append(targets, overheadrefresh.Target{BindingSequence: t.BindingSequence, Endpoint: t.Endpoint, TLS: trust, Kind: t.Kind})
	}
	store, err := admission.OpenRuntimeSQLite(*source, *identity)
	if err != nil {
		return fail()
	}
	defer store.Close()
	results, err := overheadrefresh.Refresh(ctx, store, targets)
	if json.NewEncoder(out).Encode(struct {
		Complete bool                     `json:"complete"`
		Results  []overheadrefresh.Result `json:"results"`
	}{err == nil, results}) != nil {
		return fail()
	}
	if err != nil {
		return fail()
	}
	return 0
}
