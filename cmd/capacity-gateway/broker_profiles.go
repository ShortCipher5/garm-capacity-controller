package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockidentity"
)

// Browser users select only ID + saved size. Endpoints, credentials, provider
// roles, exact execution and native policies stay in this private deployment file.
func loadBrokerProfileRecipes(path string) ([]adminview.BrokerProfileRecipe, error) {
	raw, err := securefiles.Read(path, 1<<20)
	if err != nil {
		return nil, adminview.ErrConfiguration
	}
	var config []struct {
		ID        string                             `json:"id"`
		Endpoint  string                             `json:"endpoint"`
		CAFile    string                             `json:"ca_file"`
		TokenFile string                             `json:"token_file"`
		Request   admission.BrokerPoolProfileRequest `json:"request"`
		Domains   []struct {
			Sequence int64                `json:"sequence"`
			Policy   nativeapi.PolicySpec `json:"policy"`
		} `json:"domains"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF || len(config) == 0 || len(config) > 1000 {
		return nil, adminview.ErrConfiguration
	}
	out := []adminview.BrokerProfileRecipe{}
	for _, c := range config {
		ca, e := securefiles.Read(c.CAFile, 1<<20)
		if e != nil {
			return nil, adminview.ErrConfiguration
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return nil, adminview.ErrConfiguration
		}
		secret, e := securefiles.Read(c.TokenFile, 16384)
		if e != nil {
			return nil, adminview.ErrConfiguration
		}
		token := strings.TrimSpace(string(secret))
		if token == "" || strings.ContainsAny(token, "\r\n\x00") {
			return nil, adminview.ErrConfiguration
		}
		policies := map[int64]nativeapi.Policy{}
		if len(c.Domains) == 0 || len(c.Domains) > 1000 {
			return nil, adminview.ErrConfiguration
		}
		for _, domain := range c.Domains {
			if domain.Sequence <= 0 || policies[domain.Sequence].Digest() != "" {
				return nil, adminview.ErrConfiguration
			}
			p, e := nativeapi.NewPolicy(domain.Policy)
			if e != nil {
				return nil, adminview.ErrConfiguration
			}
			policies[domain.Sequence] = p
		}
		trustHash := sha256.Sum256(ca)
		out = append(out, adminview.BrokerProfileRecipe{ID: c.ID, TrustIdentity: hex.EncodeToString(trustHash[:]), Request: c.Request, Policies: policies, Transport: stockidentity.Configuration{Endpoint: c.Endpoint, Token: token, Provider: c.Request.Authority.ProviderName, Pool: c.Request.Authority.PoolID, TLS: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}})
	}
	if err := adminview.ValidateBrokerProfileRecipes(out); err != nil {
		return nil, err
	}
	return out, nil
}
