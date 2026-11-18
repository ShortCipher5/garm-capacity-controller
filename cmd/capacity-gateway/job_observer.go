package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/demandrepair"
	"github.com/ShortCipher5/garm-capacity-controller/internal/jobobserver"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"strings"
	"time"
)

type jobObserverConfiguration struct {
	GitHubEndpoint  string                         `json:"github_endpoint"`
	GitHubAuthority string                         `json:"github_authority"`
	TokenFile       string                         `json:"token_file"`
	CAFile          string                         `json:"ca_file"`
	Repositories    []demandrepair.Repository      `json:"repositories"`
	Owners          []nativeStockHostConfiguration `json:"owners"`
	IntervalSeconds int                            `json:"interval_seconds"`
	LookbackSeconds int                            `json:"lookback_seconds"`
}

func validateJobObserver(c configuration) error {
	if c.JobObserver == nil {
		if c.EnableJobObserver {
			return jobobserver.ErrConfiguration
		}
		return nil
	}
	v := c.JobObserver
	if !c.EnableJobObserver || c.Admin == nil || v.IntervalSeconds < 60 || v.IntervalSeconds > 3600 || v.LookbackSeconds < 60 || v.LookbackSeconds > 31*86400 || len(v.Repositories) == 0 || len(v.Repositories) > 100 || len(v.Owners) == 0 || len(v.Owners) > 100 {
		return jobobserver.ErrConfiguration
	}
	return nil
}

// Configuring positive read correlation does not enable any provider/native
// mutation path, revoke credentials, delete instances or release capacity.
func buildJobObserver(c configuration, store *admission.SQLiteStore) (*jobobserver.Service, func(), error) {
	noop := func() {}
	if e := validateJobObserver(c); e != nil {
		return nil, noop, e
	}
	if c.JobObserver == nil {
		return nil, noop, nil
	}
	if store == nil {
		return nil, noop, jobobserver.ErrConfiguration
	}
	v := *c.JobObserver
	pem, e := securefiles.Read(v.CAFile, 1<<20)
	if e != nil {
		return nil, noop, jobobserver.ErrConfiguration
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, noop, jobobserver.ErrConfiguration
	}
	scope := sha256.Sum256([]byte(v.GitHubEndpoint + "\x00" + v.GitHubAuthority + "\x00" + v.TokenFile))
	github, e := demandrepair.NewGitHub(v.GitHubEndpoint, v.GitHubAuthority, "job-observer-"+hex.EncodeToString(scope[:]), func(ctx context.Context, _ string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		raw, e := securefiles.Read(v.TokenFile, 16384)
		if e != nil {
			return "", jobobserver.ErrConfiguration
		}
		token := strings.TrimSpace(string(raw))
		if token == "" || strings.ContainsAny(token, "\r\n\x00") {
			return "", jobobserver.ErrConfiguration
		}
		return token, nil
	}, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, v.Repositories)
	if e != nil {
		return nil, noop, e
	}
	fail := func() (*jobobserver.Service, func(), error) {
		github.Close()
		return nil, noop, jobobserver.ErrConfiguration
	}
	owners := []jobobserver.Owner{}
	for _, h := range v.Owners {
		policy, e := nativeapi.NewPolicy(h.Policy)
		if e != nil || policy.Controller() != c.Controller {
			return fail()
		}
		trust, e := securefiles.LoadTLS(h.OwnerTLS)
		if e != nil {
			return fail()
		}
		owners = append(owners, jobobserver.Owner{Policy: policy, Connection: nativeidentity.Configuration{Endpoint: h.OwnerEndpoint, TLS: trust, OwnerServerLeaf: h.OwnerServerLeaf, OwnerClientLeaf: h.OwnerClientLeaf, DomainID: policy.DomainID(), PolicyDigest: policy.Digest()}})
	}
	service, e := jobobserver.New(jobobserver.Configuration{Store: store, GitHub: github, Repositories: v.Repositories, Owners: owners, Lookback: time.Duration(v.LookbackSeconds) * time.Second, Interval: time.Duration(v.IntervalSeconds) * time.Second})
	if e != nil {
		return fail()
	}
	return service, github.Close, nil
}
