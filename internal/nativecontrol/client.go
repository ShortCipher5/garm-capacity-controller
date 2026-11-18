package nativecontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

type ClaimConfiguration struct {
	Endpoint             string
	TLS                  *tls.Config
	ControllerServerLeaf string
	OwnerClientLeaf      string
	DomainID             string
	PolicyDigest         string
}

// ClaimClient is constructed only by the owner deployment. Its consume method
// is private: no HTTP request supplies a preclaimed flag or detached capability.
// Future native execution must stay in this package after that fresh exchange.
type ClaimClient struct {
	config    ClaimConfiguration
	client    *http.Client
	endpoint  string
	transport *http.Transport
}

func NewClaimClient(config ClaimConfiguration) (*ClaimClient, error) {
	c := config.TLS
	if c == nil || c.InsecureSkipVerify || c.RootCAs == nil || len(c.Certificates) != 1 || len(c.Certificates[0].Certificate) == 0 || c.Certificates[0].PrivateKey == nil || c.GetClientCertificate != nil || c.GetConfigForClient != nil || c.GetCertificate != nil || c.VerifyConnection != nil || c.VerifyPeerCertificate != nil || !hash(config.ControllerServerLeaf) || !hash(config.OwnerClientLeaf) || config.ControllerServerLeaf == config.OwnerClientLeaf || fingerprint(c) != config.OwnerClientLeaf || !hash(config.DomainID) || !hash(config.PolicyDigest) {
		return nil, ErrControl
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || (c.ServerName != "" && c.ServerName != endpoint.Hostname()) || (c.MaxVersion != 0 && c.MaxVersion < tls.VersionTLS13) {
		return nil, ErrControl
	}
	frozen := c.Clone()
	frozen.RootCAs = c.RootCAs.Clone()
	frozen.MinVersion = tls.VersionTLS13
	frozen.KeyLogWriter = nil
	frozen.ServerName = endpoint.Hostname()
	frozen.NextProtos = []string{"http/1.1"}
	frozen.VerifyConnection = func(state tls.ConnectionState) error {
		if !verifiedLeaf(&state, config.ControllerServerLeaf) {
			return ErrControl
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: frozen, Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384}
	endpoint.Path = ClaimPath
	return &ClaimClient{config: config, endpoint: endpoint.String(), transport: transport, client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrControl }}}, nil
}
func (c *ClaimClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

// consume does exactly one new authenticated HTTP exchange. A lost response,
// replay, stale/malformed response or non-201 returns no claim. It performs no
// native request, retry, compensation or change to allocation accounting.
func (c *ClaimClient) consume(ctx context.Context, envelope admission.NativeExecutionEnvelope) (admission.FreshNativeClaim, error) {
	fail := func() (admission.FreshNativeClaim, error) { return admission.FreshNativeClaim{}, ErrControl }
	if c == nil || c.client == nil || envelope.Version != "native-execution/v1" || envelope.Operation != "create_and_start" || envelope.DomainID != c.config.DomainID || envelope.PolicyDigest != c.config.PolicyDigest || !permitID(envelope.PermitID) {
		return fail()
	}
	digest, err := admission.NativeEnvelopeDigest(envelope)
	if err != nil {
		return fail()
	}
	body, _ := json.Marshal(claimRequest{PermitID: envelope.PermitID, EnvelopeDigest: digest})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fail()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	started := time.Now().UTC()
	response, err := c.client.Do(request)
	if err != nil {
		return fail()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated || !peer(response.TLS, c.config.ControllerServerLeaf) {
		return fail()
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	if err != nil || len(data) > 32768 {
		return fail()
	}
	var claim admission.FreshNativeClaim
	if decodeCanonical(data, &claim) != nil || !hash(claim.ClaimID) || claim.EnvelopeDigest != digest || claim.ConsumedAt.IsZero() || claim.ConsumedAt.Before(started.Add(-30*time.Second)) || claim.ConsumedAt.After(time.Now().UTC().Add(30*time.Second)) {
		return fail()
	}
	actual, err := admission.NativeEnvelopeDigest(claim.Envelope)
	if err != nil || actual != digest {
		return fail()
	}
	return claim, nil
}
