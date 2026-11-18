// Package nativeartifacts observes a separately gated, live host artifact quota
// descriptor. The older native identity endpoint is never an enforcement proof.
package nativeartifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

const Path = "/v1/native/artifacts"
const Version = "native-artifacts/v1"

var ErrObservation = errors.New("fresh authenticated host artifact quota unavailable")

type Descriptor struct {
	Version      string                    `json:"version"`
	Nonce        string                    `json:"nonce"`
	IssuedAt     time.Time                 `json:"issued_at"`
	Owner        nativeidentity.Descriptor `json:"owner"`
	ArtifactID   string                    `json:"artifact_id"`
	PoolGUID     string                    `json:"pool_guid"`
	PoolLoadGUID string                    `json:"pool_load_guid"`
	RootGUID     string                    `json:"root_guid"`
	Dataset      string                    `json:"dataset"`
	QuotaBytes   int64                     `json:"quota_bytes"`
}

// Configuration is an expected identity pin set, never an enforcement claim.
// Expected must be the complete currently enrolled owner/native tuple.
type Configuration struct {
	Endpoint string
	TLS      *tls.Config
	Expected nativeidentity.Descriptor
}

// Observation can only be minted by an actual authenticated Observe exchange.
// It is short-lived and still not a management write or allocation capability:
// the caller must join exact enrolled profiles and mandatory provider mappings.
type Observation struct {
	record   Descriptor
	at       time.Time
	verified bool
}

func (o Observation) Valid() bool {
	age := time.Since(o.at)
	return o.verified && fresh(o.record.IssuedAt, time.Now()) && !o.at.IsZero() && age >= 0 && age <= 5*time.Second
}
func (o Observation) Record() Descriptor {
	if !o.Valid() {
		return Descriptor{}
	}
	return o.record
}
func (o Observation) ObservedAt() time.Time {
	if !o.Valid() {
		return time.Time{}
	}
	return o.at
}
func hash(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func hex64(v string) bool {
	raw, e := hex.DecodeString(v)
	return e == nil && len(raw) == 32 && hex.EncodeToString(raw) == v
}
func fresh(issued, now time.Time) bool {
	return !issued.IsZero() && !issued.After(now.Add(time.Second)) && now.Sub(issued) <= 5*time.Second
}
func ownerValid(o nativeidentity.Descriptor) bool {
	for _, v := range []string{o.DomainID, o.PolicyDigest, o.OwnerClientLeaf, o.OwnerServerLeaf, o.NativeServerKey} {
		if !hex64(v) {
			return false
		}
	}
	if o.OwnerClientLeaf == o.OwnerServerLeaf {
		return false
	}
	for _, v := range []string{o.NativeOrigin, o.NativeScope, o.NativeServerName, o.NativeServerVersion} {
		if v == "" || len(v) > 2048 || strings.ContainsAny(v, "\x00\n\r") {
			return false
		}
	}
	return true
}
func Observe(ctx context.Context, c Configuration) (Observation, error) {
	t := c.TLS
	if ctx == nil || ctx.Err() != nil || t == nil || t.RootCAs == nil || t.InsecureSkipVerify || t.VerifyConnection != nil || t.VerifyPeerCertificate != nil || t.GetClientCertificate != nil || t.GetConfigForClient != nil || t.KeyLogWriter != nil || len(t.Certificates) != 1 || len(t.Certificates[0].Certificate) == 0 || !ownerValid(c.Expected) || (t.MaxVersion != 0 && t.MaxVersion < tls.VersionTLS13) {
		return Observation{}, ErrObservation
	}
	clientCertificate, e := x509.ParseCertificate(t.Certificates[0].Certificate[0])
	if e != nil {
		return Observation{}, ErrObservation
	}
	own := hash(t.Certificates[0].Certificate[0])
	if own == c.Expected.OwnerClientLeaf || own == c.Expected.OwnerServerLeaf {
		return Observation{}, ErrObservation
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return Observation{}, ErrObservation
	}
	nonceBytes := make([]byte, 32)
	if _, e = rand.Read(nonceBytes); e != nil {
		return Observation{}, ErrObservation
	}
	nonce := hex.EncodeToString(nonceBytes)
	trust := t.Clone()
	trust.RootCAs = t.RootCAs.Clone()
	// Own all mutable certificate/key material for the lifetime of the request.
	certificate := tls.Certificate{SupportedSignatureAlgorithms: append([]tls.SignatureScheme(nil), t.Certificates[0].SupportedSignatureAlgorithms...), OCSPStaple: append([]byte(nil), t.Certificates[0].OCSPStaple...)}
	for _, der := range t.Certificates[0].Certificate {
		certificate.Certificate = append(certificate.Certificate, append([]byte(nil), der...))
	}
	for _, stamp := range t.Certificates[0].SignedCertificateTimestamps {
		certificate.SignedCertificateTimestamps = append(certificate.SignedCertificateTimestamps, append([]byte(nil), stamp...))
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(t.Certificates[0].PrivateKey)
	if err != nil {
		return Observation{}, ErrObservation
	}
	certificate.PrivateKey, err = x509.ParsePKCS8PrivateKey(keyBytes)
	clear(keyBytes)
	if err != nil {
		return Observation{}, ErrObservation
	}
	trust.Certificates = []tls.Certificate{certificate}
	clientCertificate, err = x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return Observation{}, ErrObservation
	}
	trust.MinVersion = tls.VersionTLS13
	trust.ServerName = u.Hostname()
	trust.KeyLogWriter = nil
	trust.ClientSessionCache = nil
	trust.VerifyConnection = func(s tls.ConnectionState) error {
		if s.Version < tls.VersionTLS13 || len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 || hash(s.PeerCertificates[0].Raw) != c.Expected.OwnerServerLeaf || bytes.Equal(clientCertificate.RawSubjectPublicKeyInfo, s.PeerCertificates[0].RawSubjectPublicKeyInfo) {
			return ErrObservation
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: trust, Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrObservation }}
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u.Path = Path
	u.RawQuery = "nonce=" + nonce
	request, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return Observation{}, ErrObservation
	}
	request.Header.Set("Accept", "application/json")
	response, e := client.Do(request)
	if e != nil {
		return Observation{}, ErrObservation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		return Observation{}, ErrObservation
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 16385))
	if e != nil || len(raw) > 16384 {
		return Observation{}, ErrObservation
	}
	var d Descriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF {
		return Observation{}, ErrObservation
	}
	canonical, e := json.Marshal(d)
	if e != nil || !bytes.Equal(bytes.TrimSpace(raw), canonical) {
		return Observation{}, ErrObservation
	}
	now := time.Now()
	if d.Version != Version || d.Nonce != nonce || d.Owner != c.Expected || !fresh(d.IssuedAt, now) || ctx.Err() != nil || time.Since(began) > 5*time.Second {
		return Observation{}, ErrObservation
	}
	if ValidateDescriptor(d) != nil {
		return Observation{}, ErrObservation
	}
	return Observation{record: d, at: began, verified: true}, nil
}

// ValidateDescriptor checks shape only. It cannot mint a verified Observation.
func ValidateDescriptor(d Descriptor) error {
	if d.Version != Version || !hex64(d.Nonce) || d.IssuedAt.IsZero() || !ownerValid(d.Owner) || !hex64(d.ArtifactID) || d.QuotaBytes <= 0 {
		return ErrObservation
	}
	for _, v := range []string{d.PoolGUID, d.PoolLoadGUID, d.RootGUID} {
		n, e := strconv.ParseUint(v, 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != v {
			return ErrObservation
		}
	}
	if len(d.Dataset) > 240 || !strings.Contains(d.Dataset, "/") || !strings.HasSuffix(d.Dataset, "/control-"+d.ArtifactID) {
		return ErrObservation
	}
	for _, part := range strings.Split(d.Dataset, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrObservation
		}
		for i, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-' || r == '.')) {
				return ErrObservation
			}
		}
	}
	return nil
}
