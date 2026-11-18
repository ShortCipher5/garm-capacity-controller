//go:build linux

// Package nativecontroller coordinates the canonical ledger and an independently
// enrolled native owner. It is experimental and refuses active runtime stores.
package nativecontroller

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShortCipher5/garm-capacity-controller/internal/stockjit"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeapi"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeidentity"
)

var ErrConfiguration = errors.New("exact native coordinator enrollment required")
var ErrUnqualified = errors.New("native production authority unqualified; runtime remains disabled")
var ErrUnknown = errors.New("native execution unknown; retain charge and do not retry")

type Configuration struct {
	Store                 *admission.SQLiteStore
	Policy                nativeapi.Policy
	Owner                 nativeidentity.Configuration
	DomainBindingSequence int64
	PollInterval          time.Duration
	TerminalTimeout       time.Duration
}
type Coordinator struct {
	store         *admission.SQLiteStore
	policy        nativeapi.Policy
	owner         nativeidentity.Configuration
	binding       admission.NativeDomainBinding
	poll, timeout time.Duration
}

func New(ctx context.Context, c Configuration) (*Coordinator, error) {
	if c.Store == nil || c.DomainBindingSequence <= 0 || !c.Policy.Protected() {
		return nil, ErrConfiguration
	}
	if c.Store.RuntimeLineageBound() {
		return nil, ErrUnqualified
	}
	binding, e := c.Store.LookupNativeDomainBinding(ctx, c.DomainBindingSequence)
	if e != nil {
		return nil, ErrConfiguration
	}
	spec := binding.Spec
	if c.Policy.Digest() != spec.PolicyDigest || c.Policy.DomainID() != spec.DomainID || c.Policy.Controller() != spec.Controller || c.Policy.Host() != spec.Host || c.Owner.DomainID != spec.DomainID || c.Owner.PolicyDigest != spec.PolicyDigest || c.Owner.OwnerClientLeaf != spec.OwnerClientLeaf || c.Owner.OwnerServerLeaf != spec.OwnerServerLeaf {
		return nil, ErrConfiguration
	}
	if c.PollInterval == 0 {
		c.PollInterval = 250 * time.Millisecond
	}
	if c.TerminalTimeout == 0 {
		c.TerminalTimeout = 2 * time.Minute
	}
	if c.PollInterval < 10*time.Millisecond || c.PollInterval > 5*time.Second || c.TerminalTimeout < time.Second || c.TerminalTimeout > 10*time.Minute {
		return nil, ErrConfiguration
	}
	owner := c.Owner
	owner.TLS, e = freezeTLS(c.Owner.TLS)
	if e != nil {
		return nil, ErrConfiguration
	}
	observation, e := nativeidentity.Observe(ctx, owner)
	if e != nil {
		return nil, ErrConfiguration
	}
	measured, e := admission.ObservedNativeHostBinding(spec.Host, observation)
	if e != nil {
		return nil, ErrConfiguration
	}
	actual, e := c.Store.LookupHostBinding(ctx, spec.Host)
	if e != nil || actual.Sequence != spec.HostBindingSequence || !actual.Matches(measured) {
		return nil, ErrConfiguration
	}
	return &Coordinator{store: c.Store, policy: c.Policy, owner: owner, binding: binding, poll: c.PollInterval, timeout: c.TerminalTimeout}, nil
}

// Execute owns the canonical one-shot dispatch callback. Caller-supplied plans
// are rechecked by that ledger before this code can send an owner mutation.
// No exported callback accepts detached native execution receipts.
func (c *Coordinator) Execute(ctx context.Context, plan admission.ExecutionPlan, host admission.HostBinding, authority admission.StockProviderAuthority, commandDigest, userData string) (admission.DispatchRecord, error) {
	return c.execute(ctx, plan, host, authority, commandDigest, userData, nil)
}

// ExecuteProtected preserves authentic source proof through native incarnation
// binding. The raw payload-only API cannot fabricate this optional correlation.
func (c *Coordinator) ExecuteProtected(ctx context.Context, plan admission.ExecutionPlan, host admission.HostBinding, authority admission.StockProviderAuthority, commandDigest string, source stockjit.ProtectedBootstrap) (admission.DispatchRecord, error) {
	if !source.Valid() {
		return admission.DispatchRecord{}, ErrConfiguration
	}
	return c.execute(ctx, plan, host, authority, commandDigest, source.Payload(), &source)
}
func (c *Coordinator) execute(ctx context.Context, plan admission.ExecutionPlan, host admission.HostBinding, authority admission.StockProviderAuthority, commandDigest, userData string, source *stockjit.ProtectedBootstrap) (admission.DispatchRecord, error) {
	if c == nil || c.store == nil {
		return admission.DispatchRecord{}, ErrConfiguration
	}
	if c.store.RuntimeLineageBound() {
		return admission.DispatchRecord{}, ErrUnqualified
	}
	if host.Host != c.binding.Spec.Host || host.Sequence != c.binding.Spec.HostBindingSequence || plan.Request.Host != host.Host || plan.HostBindingSequence != host.Sequence {
		return admission.DispatchRecord{}, ErrConfiguration
	}
	x := plan.Execution
	r := plan.Request.Resources
	prepared, e := c.policy.Compile(nativeapi.Allocation{Controller: x.Controller, Host: plan.Request.Host, Pool: plan.Request.Pool, Name: x.Name, RequestKey: plan.Request.Key, CPU: r.CPU, RAM: r.RAM, Disk: r.Disk, Runners: r.Runners, Flavor: x.Flavor, Image: x.Image, Fingerprint: x.Fingerprint, Type: x.Type, Arch: x.Arch, Profiles: x.Profiles}, userData)
	if e != nil {
		return admission.DispatchRecord{}, ErrConfiguration
	}
	return c.store.ExecuteStockProviderDispatch(ctx, plan, host, authority, commandDigest, func(ctx context.Context, permit admission.DispatchPermit) (admission.DispatchReceipt, error) {
		envelope, e := c.store.BindNativeExecution(ctx, permit, c.binding.Sequence, prepared.Digest())
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		in := struct {
			Envelope admission.NativeExecutionEnvelope `json:"envelope"`
			UserData string                            `json:"user_data"`
		}{Envelope: envelope, UserData: userData}
		body, e := json.Marshal(in)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		create, e := c.post(ctx, "/v1/native/executions", body, "create_step_claim_id")
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		created, e := c.capture(ctx, create, envelope, admission.NativeCreateStopped)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		quota, e := nativeidentity.ObserveQuota(ctx, c.owner, create)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if e = c.store.RecordNativeQuota(ctx, quota); e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if quota.Record().ResourceID != created.ResourceID {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		bootstrapPlan, e := c.store.BindNativeBootstrap(ctx, permit.ID(), userData, c.policy)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if source != nil {
			if _, e = c.store.BindNativeRunner(ctx, permit.ID(), *source, c.policy); e != nil {
				return admission.DispatchReceipt{}, ErrUnknown
			}
		}
		bootstrap, e := c.post(ctx, "/v1/native/executions/"+permit.ID()+"/bootstrap", []byte("{}"), "bootstrap_claim_id")
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		proof, e := nativeidentity.ObserveBootstrap(ctx, c.owner, bootstrap)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if e = c.store.RecordNativeBootstrap(ctx, proof); e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if proof.Record().ResourceID != created.ResourceID || proof.Record().PermitID != bootstrapPlan.PermitID || proof.Record().Outcome != "verified" {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		start, e := c.post(ctx, "/v1/native/executions/"+permit.ID()+"/start", []byte("{}"), "start_step_claim_id")
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		started, e := c.capture(ctx, start, envelope, admission.NativeStart)
		if e != nil {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		if started.ResourceID != created.ResourceID {
			return admission.DispatchReceipt{}, ErrUnknown
		}
		return admission.DispatchReceipt{State: "confirmed", OperationID: started.OperationID, ResourceID: started.ResourceID}, nil
	})
}
func (c *Coordinator) capture(ctx context.Context, claimID string, envelope admission.NativeExecutionEnvelope, step string) (nativeidentity.OperationRecord, error) {
	accepted, e := nativeidentity.ObserveOperation(ctx, c.owner, claimID)
	if e != nil {
		return nativeidentity.OperationRecord{}, ErrUnknown
	}
	wantDigest, de := admission.NativeEnvelopeDigest(envelope)
	r := accepted.Record()
	if de != nil || r.PermitID != envelope.PermitID || r.EnvelopeDigest != wantDigest || r.DomainID != envelope.DomainID || r.PolicyDigest != envelope.PolicyDigest || r.NativeName != envelope.NativeName || r.Step != step {
		return nativeidentity.OperationRecord{}, ErrUnknown
	}
	if e = c.store.RecordNativeOperation(ctx, accepted); e != nil {
		return nativeidentity.OperationRecord{}, ErrUnknown
	}
	if accepted.Record().Outcome != "accepted" {
		return nativeidentity.OperationRecord{}, ErrUnknown
	}
	wait, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	for {
		terminal, e := nativeidentity.ObserveOperationTerminal(wait, c.owner, claimID)
		if e != nil {
			return nativeidentity.OperationRecord{}, ErrUnknown
		}
		// Repeated pending observations are reads, not new lifecycle events.
		// Avoid committing the same accepted state under an expiring poll context.
		// A changed pending tuple still fails closed against the durable ACK.
		if terminal.Record().Outcome == "accepted" {
			if terminal.Record() != accepted.Record() {
				return nativeidentity.OperationRecord{}, ErrUnknown
			}
		} else if e = c.store.RecordNativeOperation(wait, terminal); e != nil {
			return nativeidentity.OperationRecord{}, ErrUnknown
		}
		switch terminal.Record().Outcome {
		case "succeeded":
			return terminal.Record(), nil
		case "accepted":
		default:
			return nativeidentity.OperationRecord{}, ErrUnknown
		}
		timer := time.NewTimer(c.poll)
		select {
		case <-wait.Done():
			timer.Stop()
			return nativeidentity.OperationRecord{}, ErrUnknown
		case <-timer.C:
		}
	}
}
func hash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// post makes exactly one owner mutation request. No redirects, implicit
// idempotency key, pooled connection retry, request replay body or error-body log.
func (c *Coordinator) post(ctx context.Context, path string, body []byte, resultKey string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint, e := url.Parse(c.owner.Endpoint)
	if e != nil {
		return "", ErrUnknown
	}
	endpoint.Path = path
	cfg := c.owner.TLS.Clone()
	cfg.MinVersion = tls.VersionTLS13
	cfg.KeyLogWriter = nil
	previous := cfg.VerifyConnection
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if s.Version < tls.VersionTLS13 || len(s.PeerCertificates) == 0 || len(s.VerifiedChains) == 0 || nativeidentity.Fingerprint(s.PeerCertificates[0].Raw) != c.binding.Spec.OwnerServerLeaf {
			return ErrUnknown
		}
		if previous != nil {
			return previous(s)
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: cfg, Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 25 * time.Second, MaxResponseHeaderBytes: 16384}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnknown }}
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if e != nil {
		return "", ErrUnknown
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	response, e := client.Do(request)
	if e != nil {
		return "", ErrUnknown
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated || response.TLS == nil || !response.TLS.HandshakeComplete {
		return "", ErrUnknown
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 1025))
	if e != nil || len(data) > 1024 {
		return "", ErrUnknown
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return "", ErrUnknown
	}
	token, e = d.Token()
	if e != nil || token != resultKey {
		return "", ErrUnknown
	}
	var id string
	if d.Decode(&id) != nil || !hash(id) {
		return "", ErrUnknown
	}
	token, e = d.Token()
	if e != nil || token != json.Delim('}') {
		return "", ErrUnknown
	}
	if _, e = d.Token(); e != io.EOF {
		return "", ErrUnknown
	}
	return id, nil
}

// freezeTLS owns trust roots, certificate bytes and the client private key.
// Dynamic client-certificate providers and non-exportable signing handles are
// unsupported here rather than retaining caller-mutable authority material.
func freezeTLS(in *tls.Config) (*tls.Config, error) {
	if in == nil || in.RootCAs == nil || in.GetClientCertificate != nil || len(in.Certificates) != 1 {
		return nil, ErrConfiguration
	}
	out := in.Clone()
	out.RootCAs = in.RootCAs.Clone()
	if in.ClientCAs != nil {
		out.ClientCAs = in.ClientCAs.Clone()
	}
	out.Certificates = make([]tls.Certificate, len(in.Certificates))
	for i, cert := range in.Certificates {
		copied := cert
		copied.Certificate = make([][]byte, len(cert.Certificate))
		for j, der := range cert.Certificate {
			copied.Certificate[j] = bytes.Clone(der)
		}
		copied.OCSPStaple = bytes.Clone(cert.OCSPStaple)
		copied.SignedCertificateTimestamps = make([][]byte, len(cert.SignedCertificateTimestamps))
		for j, timestamp := range cert.SignedCertificateTimestamps {
			copied.SignedCertificateTimestamps[j] = bytes.Clone(timestamp)
		}
		copied.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), cert.SupportedSignatureAlgorithms...)
		copied.Leaf = nil
		if len(copied.Certificate) == 0 {
			return nil, ErrConfiguration
		}
		leaf, e := x509.ParseCertificate(copied.Certificate[0])
		if e != nil {
			return nil, ErrConfiguration
		}
		copied.Leaf = leaf
		keyBytes, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
		if e != nil {
			return nil, ErrConfiguration
		}
		key, e := x509.ParsePKCS8PrivateKey(keyBytes)
		clear(keyBytes)
		if e != nil {
			return nil, ErrConfiguration
		}
		copied.PrivateKey = key
		out.Certificates[i] = copied
	}
	out.NameToCertificate = nil
	out.NextProtos = append([]string(nil), in.NextProtos...)
	out.CipherSuites = append([]uint16(nil), in.CipherSuites...)
	out.CurvePreferences = append([]tls.CurveID(nil), in.CurvePreferences...)
	out.ClientSessionCache = nil
	out.KeyLogWriter = nil
	return out, nil
}
