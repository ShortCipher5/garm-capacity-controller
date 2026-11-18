// Package stockidentity observes repository/pool ownership through a real
// verified stock API read. It never creates activation or native authority.
package stockidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrObservation = errors.New("scoped stock ownership unavailable")

type Facts struct{ Pool, Repository, Provider, Flavor, Image, OSType, OSArch string }
type Observation struct {
	facts  Facts
	source string
	at     time.Time
	valid  bool
}

func (o Observation) Valid() bool            { return o.valid && !o.at.IsZero() }
func (o Observation) Facts() Facts           { return o.facts }
func (o Observation) SourceIdentity() string { return o.source }
func (o Observation) ObservedAt() time.Time  { return o.at }
func (f Facts) Fingerprint() string {
	raw, _ := json.Marshal(f)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type Configuration struct {
	Endpoint, Token, Provider, Pool string
	TLS                             *tls.Config
}

func uuid(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || strings.ToLower(s) != s {
		return false
	}
	v, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil && len(v) == 16
}
func text(m map[string]json.RawMessage, k string, required bool) (string, error) {
	raw, ok := m[k]
	if !ok || bytes.Equal(raw, []byte("null")) {
		if required {
			return "", ErrObservation
		}
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || len(s) > 512 || strings.ContainsAny(s, "\x00\r\n") || (required && s == "") {
		return "", ErrObservation
	}
	return s, nil
}

// ParseFacts allows read adapters to calculate the identical stable projection.
// It cannot mint an Observation. Volatile pool limits/state do not enter it.
func ParseFacts(raw []byte) (Facts, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return Facts{}, ErrObservation
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		key, e := d.Token()
		s, ok := key.(string)
		if e != nil || !ok {
			return Facts{}, ErrObservation
		}
		if _, ok = m[s]; ok {
			return Facts{}, ErrObservation
		}
		for _, k := range []string{"id", "repo_id", "org_id", "enterprise_id", "provider_name", "flavor", "image", "os_type", "os_arch"} {
			if strings.EqualFold(k, s) && k != s {
				return Facts{}, ErrObservation
			}
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return Facts{}, ErrObservation
		}
		m[s] = v
	}
	if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
		return Facts{}, ErrObservation
	}
	f := Facts{}
	for _, pair := range []struct {
		k string
		v *string
	}{{"id", &f.Pool}, {"repo_id", &f.Repository}, {"provider_name", &f.Provider}, {"flavor", &f.Flavor}, {"image", &f.Image}, {"os_type", &f.OSType}, {"os_arch", &f.OSArch}} {
		*pair.v, e = text(m, pair.k, true)
		if e != nil {
			return Facts{}, e
		}
	}
	for _, k := range []string{"org_id", "enterprise_id"} {
		v, e := text(m, k, false)
		if e != nil || v != "" {
			return Facts{}, ErrObservation
		}
	}
	if !uuid(f.Pool) || !uuid(f.Repository) || f.OSType != "linux" || (f.OSArch != "amd64" && f.OSArch != "arm64") {
		return Facts{}, ErrObservation
	}
	return f, nil
}

// ValidateConfiguration checks transport syntax without network access or minting provenance.
func ValidateConfiguration(c Configuration) error {
	if !uuid(c.Pool) || c.Provider == "" || c.Token == "" || strings.ContainsAny(c.Token, "\r\n\x00") || c.TLS == nil || c.TLS.InsecureSkipVerify || c.TLS.RootCAs == nil || c.TLS.VerifyConnection != nil || c.TLS.VerifyPeerCertificate != nil {
		return ErrObservation
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ErrObservation
	}
	if c.TLS.MaxVersion != 0 && (c.TLS.MaxVersion < tls.VersionTLS12 || c.TLS.MaxVersion < c.TLS.MinVersion) {
		return ErrObservation
	}
	return nil
}
func Observe(ctx context.Context, c Configuration) (Observation, error) {
	fail := func() (Observation, error) { return Observation{}, ErrObservation }
	if ctx == nil || ctx.Err() != nil || ValidateConfiguration(c) != nil {
		return fail()
	}
	u, _ := url.Parse(c.Endpoint)
	u.Path = "/api/v1"
	base := u.String()
	trust := c.TLS.Clone()
	trust.RootCAs = c.TLS.RootCAs.Clone()
	trust.ServerName = u.Hostname()
	if trust.MinVersion < tls.VersionTLS12 {
		trust.MinVersion = tls.VersionTLS12
	}
	trust.KeyLogWriter = nil
	if trust.MaxVersion != 0 && trust.MaxVersion < trust.MinVersion {
		return fail()
	}
	tr := &http.Transport{TLSClientConfig: trust, Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"/pools/"+c.Pool, nil)
	if e != nil {
		return fail()
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	at := time.Now().UTC()
	res, e := client.Do(req)
	if e != nil {
		return fail()
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fail()
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 || ctx.Err() != nil {
		return fail()
	}
	f, e := ParseFacts(raw)
	if e != nil || f.Pool != c.Pool || f.Provider != c.Provider {
		return fail()
	}
	h := sha256.Sum256([]byte("garm-v0.2.1-management\x00" + base))
	return Observation{facts: f, source: hex.EncodeToString(h[:]), at: at, valid: true}, nil
}
