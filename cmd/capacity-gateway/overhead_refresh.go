package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/overheadrefresh"
	"github.com/ShortCipher5/garm-capacity-controller/internal/securefiles"
	"io"
)

type overheadRefreshConfiguration struct {
	RecipesFile      string `json:"recipes_file"`
	JournalDirectory string `json:"journal_directory"`
}

func loadOverheadRecipes(path string) ([]overheadrefresh.Recipe, error) {
	raw, e := securefiles.Read(path, 1<<20)
	if e != nil {
		return nil, overheadrefresh.ErrService
	}
	var input []struct {
		ID      string `json:"id"`
		Targets []struct {
			Binding  int64                `json:"binding_sequence"`
			Endpoint string               `json:"endpoint"`
			Kind     string               `json:"kind"`
			TLS      securefiles.TLSFiles `json:"tls"`
		} `json:"targets"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input) == 0 || len(input) > 100 {
		return nil, overheadrefresh.ErrService
	}
	out := []overheadrefresh.Recipe{}
	for _, in := range input {
		if len(in.Targets) == 0 || len(in.Targets) > 10 {
			return nil, overheadrefresh.ErrService
		}
		r := overheadrefresh.Recipe{ID: in.ID}
		hash := sha256.New()
		for _, t := range in.Targets {
			ca, e := securefiles.Read(t.TLS.CA, 1<<20)
			if e != nil {
				return nil, overheadrefresh.ErrService
			}
			trust, e := securefiles.LoadTLS(t.TLS)
			if e != nil {
				return nil, overheadrefresh.ErrService
			}
			// Bind exactly the CA bytes used, not a raced second read.
			roots, e := rootsFromPEM(ca)
			if e != nil {
				return nil, overheadrefresh.ErrService
			}
			trust.RootCAs = roots
			hash.Write(ca)
			hash.Write([]byte{0})
			cert, _ := json.Marshal(trust.Certificates[0].Certificate)
			hash.Write(cert)
			hash.Write([]byte{0})
			r.Targets = append(r.Targets, overheadrefresh.Target{BindingSequence: t.Binding, Endpoint: t.Endpoint, Kind: t.Kind, TLS: trust})
		}
		r.TrustDigest = hex.EncodeToString(hash.Sum(nil))
		out = append(out, r)
	}
	return out, nil
}

func rootsFromPEM(raw []byte) (*x509.CertPool, error) {
	r := x509.NewCertPool()
	if !r.AppendCertsFromPEM(raw) {
		return nil, overheadrefresh.ErrService
	}
	return r, nil
}
