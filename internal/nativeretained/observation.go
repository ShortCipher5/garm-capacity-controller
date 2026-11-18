// Package nativeretained observes a separately gated, live retained retirement evidence
// descriptor. The older native identity endpoint is never an enforcement proof.
package nativeretained

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

const PathPrefix = "/v1/native/retirement-operations/"
const Version = "native-retained-observations/v1"

var ErrObservation = errors.New("fresh authenticated retained retirement evidence unavailable")

type Descriptor struct {
	Version             string                                   `json:"version"`
	Nonce               string                                   `json:"nonce"`
	IssuedAt            time.Time                                `json:"issued_at"`
	Owner               nativeidentity.Descriptor                `json:"owner"`
	Delete              nativeidentity.RetirementOperationRecord `json:"delete"`
	Qualification       string                                   `json:"qualification"`
	ReleaseAuthorized   bool                                     `json:"release_authorized"`
	NamespaceInode      uint64                                   `json:"namespace_inode"`
	NamespaceTerminated bool                                     `json:"namespace_terminated"`
	NamespaceObservedAt time.Time                                `json:"namespace_observed_at"`
	InstanceAbsent      bool                                     `json:"instance_absent"`
	InstanceObservedAt  time.Time                                `json:"instance_observed_at"`
	StorageObservedAt   time.Time                                `json:"storage_observed_at"`
	RootGUID            string                                   `json:"root_guid"`
	RootQuotaBytes      uint64                                   `json:"root_quota_bytes"`
	RootUsedBytes       uint64                                   `json:"root_used_bytes"`
	RootAvailableBytes  uint64                                   `json:"root_available_bytes"`
	PoolGUID            string                                   `json:"pool_guid"`
	PoolLoadGUID        string                                   `json:"pool_load_guid"`
	PoolAllocatedBytes  uint64                                   `json:"pool_allocated_bytes"`
	PoolFreeBytes       uint64                                   `json:"pool_free_bytes"`
	PoolSizeBytes       uint64                                   `json:"pool_size_bytes"`
}

// Configuration is an expected identity pin set, never an enforcement claim.
// Expected must be the complete currently enrolled owner/native tuple.
type Configuration struct {
	Endpoint string
	TLS      *tls.Config
	Expected nativeidentity.Descriptor
	Delete   nativeidentity.RetirementOperationRecord
}

// Observation can only be minted by an actual authenticated Observe exchange.
// It is short-lived and still not a management write or allocation capability:
// the caller must preserve canonical charges and exact DELETE identity; no refund is authorized.
type Observation struct {
	record   Descriptor
	at       time.Time
	verified bool
}

func (o Observation) Valid() bool {
	age := time.Since(o.at)
	return o.verified && fresh(o.record.IssuedAt, time.Now()) && fresh(o.record.NamespaceObservedAt, time.Now()) && fresh(o.record.InstanceObservedAt, time.Now()) && fresh(o.record.StorageObservedAt, time.Now()) && !o.at.IsZero() && age >= 0 && age <= 5*time.Second
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
	if ctx == nil || ctx.Err() != nil || t == nil || t.RootCAs == nil || t.InsecureSkipVerify || t.VerifyConnection != nil || t.VerifyPeerCertificate != nil || t.GetClientCertificate != nil || t.GetConfigForClient != nil || t.KeyLogWriter != nil || len(t.Certificates) != 1 || len(t.Certificates[0].Certificate) == 0 || !ownerValid(c.Expected) || !validDelete(c.Delete, c.Expected) || (t.MaxVersion != 0 && t.MaxVersion < tls.VersionTLS13) {
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
	u.Path = PathPrefix + c.Delete.ClaimID + "/evidence"
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
	if d.Version != Version || d.Nonce != nonce || d.Owner != c.Expected || d.Delete != c.Delete || !fresh(d.IssuedAt, now) || ctx.Err() != nil || time.Since(began) > 5*time.Second {
		return Observation{}, ErrObservation
	}
	if ValidateDescriptor(d) != nil || !fresh(d.NamespaceObservedAt, now) || !fresh(d.InstanceObservedAt, now) || !fresh(d.StorageObservedAt, now) {
		return Observation{}, ErrObservation
	}
	return Observation{record: d, at: began, verified: true}, nil
}

// ValidateDescriptor checks shape, never mints a trusted Observation.
func ValidateDescriptor(d Descriptor) error {
	if d.Version != Version || !hex64(d.Nonce) || d.IssuedAt.IsZero() || !ownerValid(d.Owner) || !validDelete(d.Delete, d.Owner) || d.Qualification != "retained_observations_only" || d.ReleaseAuthorized || d.NamespaceInode == 0 || !d.NamespaceTerminated || !d.InstanceAbsent {
		return ErrObservation
	}
	for _, at := range []time.Time{d.NamespaceObservedAt, d.InstanceObservedAt, d.StorageObservedAt} {
		if at.IsZero() || at.Before(d.IssuedAt) {
			return ErrObservation
		}
	}
	for _, v := range []string{d.RootGUID, d.PoolGUID, d.PoolLoadGUID} {
		if !positive(v) {
			return ErrObservation
		}
	}
	if d.RootQuotaBytes == 0 || d.PoolSizeBytes == 0 || d.PoolAllocatedBytes > d.PoolSizeBytes || d.PoolFreeBytes > d.PoolSizeBytes || d.PoolAllocatedBytes != d.PoolSizeBytes-d.PoolFreeBytes {
		return ErrObservation
	}
	return nil
}
func positive(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == s
}
func uuid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func validDelete(d nativeidentity.RetirementOperationRecord, o nativeidentity.Descriptor) bool {
	if d.PermitID == "" || len(d.PermitID) > 256 || strings.ContainsAny(d.PermitID, "\x00\r\n/\\") || !hex64(d.IntentDigest) || !hex64(d.ClaimID) || !hex64(d.RequestDigest) || d.DomainID != o.DomainID || d.PolicyDigest != o.PolicyDigest || d.NativeName == "" || len(d.NativeName) > 256 || strings.ContainsAny(d.NativeName, "\x00\r\n/\\") || !uuid(d.ResourceID) || !uuid(d.OperationID) || d.Step != "delete" || d.Outcome != "succeeded" || !positive(d.DatasetGUID) {
		return false
	}
	if len(d.Dataset) > 240 || !strings.Contains(d.Dataset, "/") {
		return false
	}
	for _, part := range strings.Split(d.Dataset, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for i, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-' || r == '.')) {
				return false
			}
		}
	}
	return true
}
