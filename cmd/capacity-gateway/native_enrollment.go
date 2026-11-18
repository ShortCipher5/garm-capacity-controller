package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"

	"github.com/ShortCipher5/garm-capacity-controller/internal/adminview"
	"github.com/ShortCipher5/garm-capacity-controller/internal/gatewayrpc"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
)

// Only this private deployment file can select URLs, credentials, policies or
// initial host mode. Browser input contains no connection or authority material.
func loadNativeEnrollmentRecipes(path string) ([]adminview.NativeEnrollmentRecipe, error) {
	raw, e := securefiles.Read(path, 1<<20)
	if e != nil {
		return nil, adminview.ErrConfiguration
	}
	var input []struct {
		ID     string `json:"id"`
		Mode   string `json:"mode"`
		Owners []struct {
			Endpoint        string               `json:"endpoint"`
			TLS             securefiles.TLSFiles `json:"tls"`
			OwnerClientLeaf string               `json:"owner_client_leaf"`
			OwnerServerLeaf string               `json:"owner_server_leaf"`
			Policy          nativeapi.PolicySpec `json:"policy"`
		} `json:"owners"`
	}
	if gatewayrpc.Decode(bytes.NewReader(raw), &input) != nil || len(input) == 0 || len(input) > 1000 {
		return nil, adminview.ErrConfiguration
	}
	result := []adminview.NativeEnrollmentRecipe{}
	for _, v := range input {
		if (v.Mode != "initial" && v.Mode != "existing") || len(v.Owners) == 0 || len(v.Owners) > 1000 {
			return nil, adminview.ErrConfiguration
		}
		r := adminview.NativeEnrollmentRecipe{ID: v.ID, Initial: v.Mode == "initial"}
		for _, o := range v.Owners {
			p, e := nativeapi.NewPolicy(o.Policy)
			if e != nil {
				return nil, adminview.ErrConfiguration
			}
			trust, e := securefiles.LoadTLS(o.TLS)
			if e != nil {
				return nil, adminview.ErrConfiguration
			}
			ca, e := securefiles.Read(o.TLS.CA, 1<<20)
			if e != nil {
				return nil, adminview.ErrConfiguration
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(ca) {
				return nil, adminview.ErrConfiguration
			}
			trust.RootCAs = roots
			hash := sha256.Sum256(ca)
			r.Owners = append(r.Owners, adminview.NativeEnrollmentOwner{TrustIdentity: hex.EncodeToString(hash[:]), Policy: p, Transport: nativeidentity.Configuration{Endpoint: o.Endpoint, TLS: trust, OwnerClientLeaf: o.OwnerClientLeaf, OwnerServerLeaf: o.OwnerServerLeaf, DomainID: p.DomainID(), PolicyDigest: p.Digest()}})
		}
		result = append(result, r)
	}
	return result, nil
}
