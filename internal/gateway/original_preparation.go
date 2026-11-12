package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
)

// Source policies are trusted startup routing, NOT physical/issuer attestation.
// Endpoint NAME is an actual Garm alias; it is not an invented endpoint UUID.
type PreparationPoolPolicy struct {
	ProviderName       string   `json:"provider_name"`
	EntityType         string   `json:"entity_type"`
	EntityID           string   `json:"entity_id"`
	ForgeEndpointName  string   `json:"forge_endpoint_name"`
	Generation         *uint64  `json:"generation"`
	JITEnabled         *bool    `json:"jit_enabled"`
	SourceFingerprints []string `json:"source_fingerprints"`
}
type PreparationPoolObservation struct {
	TemplateID        uint64
	DefaultBootstrap  bool
	ShellDisabled     bool
	Enabled           bool
	Image             string
	Flavor            string
	OSType            string
	OSArch            string
	PoolID            string
	ProviderName      string
	EntityType        string
	EntityID          string
	ForgeEndpointName string
	Generation        uint64
	FieldsKnown       bool // parsed exact required metadata, NOT whole-host coverage
}
type PreparationSource interface {
	GarmSource
	PreparationPool(context.Context, string) (PreparationPoolObservation, error)
}
type PreparationSettings struct {
	Endpoint  string
	Providers []string
	Policies  map[string]PreparationPoolPolicy
	Source    PreparationSource
}
type preparationRoute struct {
	authority admission.PreparationAuthority
	actors    map[string]bool
}

func preparationEndpoint(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", ErrPolicy
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", ErrPolicy
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(number))
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
func (f *Facade) configurePreparations(settings PreparationSettings) error {
	endpoint, err := preparationEndpoint(settings.Endpoint)
	if err != nil || settings.Source == nil || len(settings.Policies) != len(f.pools) {
		return ErrPolicy
	}
	providers := map[string]bool{}
	for _, p := range settings.Providers {
		if p == "" || providers[p] {
			return ErrPolicy
		}
		providers[p] = true
	}
	routes := map[string]preparationRoute{}
	for id, policy := range settings.Policies {
		if _, ok := f.pools[id]; !ok || policy.Generation == nil || policy.JITEnabled == nil || !providers[policy.ProviderName] || len(policy.SourceFingerprints) == 0 || len(policy.SourceFingerprints) > 64 {
			return ErrPolicy
		}
		request := garmprotocol.PreparationRequest{Protocol: garmprotocol.PreparationVersion, Name: "source-policy-validation", PoolID: id, InstanceID: "11111111-1111-4111-8111-111111111111", Generation: *policy.Generation, ProviderName: policy.ProviderName, EntityType: policy.EntityType, EntityID: policy.EntityID, ForgeEndpointName: policy.ForgeEndpointName, JITEnabled: *policy.JITEnabled}
		if request.JITEnabled {
			request.AgentID = 1
		}
		if request.Validate() != nil {
			return ErrPolicy
		}
		actors := map[string]bool{}
		fingerprints := append([]string(nil), policy.SourceFingerprints...)
		sort.Strings(fingerprints)
		for _, fp := range fingerprints {
			raw, err := hex.DecodeString(fp)
			if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != fp || actors[fp] {
				return ErrPolicy
			}
			actors[fp] = true
		}
		scope, _ := json.Marshal(struct {
			Endpoint, Controller, PoolID, Provider, EntityType, EntityID, ForgeName string
			Generation                                                              uint64
			JIT                                                                     bool
			Actors                                                                  []string
		}{endpoint, f.controller, id, policy.ProviderName, policy.EntityType, policy.EntityID, policy.ForgeEndpointName, *policy.Generation, *policy.JITEnabled, fingerprints})
		sum := sha256.Sum256(scope)
		routes[id] = preparationRoute{actors: actors, authority: admission.PreparationAuthority{Controller: f.controller, SourceScopeFingerprint: hex.EncodeToString(sum[:]), Role: "source", PoolID: id, ProviderName: policy.ProviderName, EntityType: policy.EntityType, EntityID: policy.EntityID, ForgeEndpointName: policy.ForgeEndpointName, Generation: *policy.Generation, JITEnabled: *policy.JITEnabled}}
	}
	f.preparationRoutes = routes
	f.preparationSource = settings.Source
	return nil
}
func (f *Facade) PreparationConfigured() bool { return len(f.preparationRoutes) > 0 }
func (f *Facade) AuthorizePreparationPool(pool, actor string) bool {
	route, ok := f.preparationRoutes[pool]
	return ok && route.actors[actor]
}
func (f *Facade) PreparationFingerprints() []string {
	seen := map[string]bool{}
	for _, route := range f.preparationRoutes {
		for fp := range route.actors {
			seen[fp] = true
		}
	}
	result := []string{}
	for fp := range seen {
		result = append(result, fp)
	}
	sort.Strings(result)
	return result
}
func (f *Facade) preparationAuthority(pool, actor string) (admission.PreparationAuthority, error) {
	route, ok := f.preparationRoutes[pool]
	if !ok || !route.actors[actor] {
		return admission.PreparationAuthority{}, ErrPolicy
	}
	a := route.authority
	a.ClientFingerprint = actor
	return a, nil
}
func (f *Facade) preparationPoolMetadata(ctx context.Context, request garmprotocol.PreparationRequest, a admission.PreparationAuthority) (PreparationPoolObservation, error) {
	if f.preparationSource == nil {
		return PreparationPoolObservation{}, ErrUnavailable
	}
	pool, err := f.preparationSource.PreparationPool(ctx, request.PoolID)
	policy, configured := f.pools[request.PoolID]
	if err != nil || !configured || pool.Image != policy.Image || pool.Flavor != policy.Flavor || pool.OSType != "linux" || pool.OSArch != string(policy.Arch) || !pool.FieldsKnown || !pool.DefaultBootstrap || !pool.ShellDisabled || pool.TemplateID != 0 || pool.PoolID != request.PoolID || pool.ProviderName != a.ProviderName || pool.EntityType != a.EntityType || pool.EntityID != a.EntityID || pool.ForgeEndpointName != a.ForgeEndpointName || pool.Generation != a.Generation {
		return PreparationPoolObservation{}, ErrUnavailable
	}
	return pool, nil
}
func (f *Facade) checkPreparationPool(ctx context.Context, request garmprotocol.PreparationRequest, a admission.PreparationAuthority) error {
	pool, err := f.preparationPoolMetadata(ctx, request, a)
	if err != nil || !pool.Enabled {
		return ErrUnavailable
	}
	return nil
}
func preparationResponse(receipt admission.OriginalPreparationReceipt, outcome string) (garmprotocol.PreparationResponse, error) {
	no := false
	replay := receipt.Replay
	if outcome == "validated" {
		replay = true
	}
	response := garmprotocol.PreparationResponse{PreparationRequest: receipt.Original, Outcome: outcome, ReservationKey: receipt.Request.Key, RecordSequence: receipt.RecordSequence, Replay: &replay, Dispatched: &no}
	if _, err := garmprotocol.EncodePreparationResponse(response, receipt.Original); err != nil {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	return response, nil
}
func (f *Facade) RecordPreparation(ctx context.Context, request garmprotocol.PreparationRequest, actor string) (garmprotocol.PreparationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if request.Validate() != nil {
		return garmprotocol.PreparationResponse{}, ErrPolicy
	}
	a, err := f.preparationAuthority(request.PoolID, actor)
	if err != nil {
		return garmprotocol.PreparationResponse{}, err
	}
	if err := f.checkPreparationPool(ctx, request, a); err != nil {
		return garmprotocol.PreparationResponse{}, err
	}
	plan, _, err := f.planned(ctx, request.Name, request.PoolID)
	if err != nil || !f.planAllowed(plan) {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	// First mint must precede visibility. This defensive read complements, rather
	// than cryptographically proves, the installed trusted source-hook ordering.
	if _, err := f.store.LookupOriginalPreparation(ctx, plan.Request.Key); errors.Is(err, sql.ErrNoRows) {
		rows, err := f.preparationSource.Instances(ctx)
		if err != nil {
			return garmprotocol.PreparationResponse{}, ErrUnavailable
		}
		for _, row := range rows {
			if row.Name == request.Name || row.RowID == request.InstanceID {
				return garmprotocol.PreparationResponse{}, ErrUnavailable
			}
		}
	} else if err != nil {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	receipt, err := f.store.RecordOriginalPreparation(ctx, request, a)
	if err != nil {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	return preparationResponse(receipt, "recorded")
}

// observeOriginalPreparation reads actual authenticated Garm metadata. A caller
// frame or bootstrap bool cannot manufacture row existence or creation eligibility.
func (f *Facade) observeOriginalPreparation(ctx context.Context, receipt admission.OriginalPreparationReceipt) (GarmInstance, error) {
	rows, err := f.preparationSource.Instances(ctx)
	if err != nil {
		return GarmInstance{}, ErrUnavailable
	}
	var found *GarmInstance
	for _, row := range rows {
		if row.Name != receipt.Original.Name {
			continue
		}
		if found != nil {
			return GarmInstance{}, ErrUnavailable
		}
		copy := row
		found = &copy
	}
	if found == nil || !found.IdentityFieldsKnown || found.RowID != receipt.Original.InstanceID || found.PoolID != receipt.Original.PoolID || found.ProviderName != receipt.Original.ProviderName || found.AgentID != receipt.Original.AgentID || found.Generation != receipt.Original.Generation || found.ScaleSetID != 0 {
		return GarmInstance{}, ErrUnavailable
	}
	return *found, nil
}
func (f *Facade) ValidatePreparation(ctx context.Context, request garmprotocol.PreparationRequest, actor string) (garmprotocol.PreparationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if request.Validate() != nil {
		return garmprotocol.PreparationResponse{}, ErrPolicy
	}
	a, err := f.preparationAuthority(request.PoolID, actor)
	if err != nil {
		return garmprotocol.PreparationResponse{}, err
	}
	if err := f.checkPreparationPool(ctx, request, a); err != nil {
		return garmprotocol.PreparationResponse{}, err
	}
	receipt, err := f.store.ValidateOriginalPreparation(ctx, request, a)
	if err != nil {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	row, err := f.observeOriginalPreparation(ctx, receipt)
	if err != nil || !row.SourcePreparationPending || row.Busy || row.Idle || row.ProviderID != "" || (row.State != "pending_create" && row.State != "creating") {
		return garmprotocol.PreparationResponse{}, ErrUnavailable
	}
	return preparationResponse(receipt, "validated")
}

func (f *Facade) RuntimeOriginalPreparationRequired() bool { return f.store.RuntimeLineageBound() }
func (f *Facade) preparedCreate(ctx context.Context, plan admission.ExecutionPlan, jitEnabled bool) (admission.OriginalPreparationReceipt, admission.GarmRowObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	started := time.Now()
	if !f.PreparationConfigured() {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, ErrUnavailable
	}
	receipt, err := f.store.LookupOriginalPreparation(ctx, plan.Request.Key)
	if err != nil || receipt.Original.JITEnabled != jitEnabled {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, ErrUnavailable
	}
	a, err := f.preparationAuthority(plan.Request.Pool, receipt.Source.ClientFingerprint)
	if err != nil {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, ErrUnavailable
	}
	pool, err := f.preparationPoolMetadata(ctx, receipt.Original, a)
	if err != nil {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, err
	}
	receipt, err = f.store.ValidateOriginalPreparation(ctx, receipt.Original, a)
	if err != nil {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, ErrUnavailable
	}
	row, err := f.observeOriginalPreparation(ctx, receipt)
	if err != nil {
		return admission.OriginalPreparationReceipt{}, admission.GarmRowObservation{}, err
	}
	eligible := pool.Enabled && row.SourcePreparationPending && (row.State == "pending_create" || row.State == "creating") && row.ProviderID == "" && !row.Busy && !row.Idle
	observation := admission.GarmRowObservation{RequestKey: plan.Request.Key, SourceScopeFingerprint: a.SourceScopeFingerprint, InstanceID: row.RowID, Name: row.Name, PoolID: row.PoolID, ProviderName: row.ProviderName, AgentID: row.AgentID, Generation: row.Generation, ObservedAt: started, PreparationShape: row.SourcePreparationPending, CreationEligible: eligible}
	return receipt, observation, nil
}

// Check known upstream policy drift before reserving or asking GitHub for JIT.
// This is not a remote CAS: all later source/row/claim checks remain mandatory.
func (f *Facade) preparationAdmissionCheck(ctx context.Context, pool string) error {
	if f.preparationSource == nil && !f.RuntimeOriginalPreparationRequired() {
		return nil
	}
	route, ok := f.preparationRoutes[pool]
	if !ok {
		return ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return f.checkPreparationPool(bounded, garmprotocol.PreparationRequest{PoolID: pool}, route.authority)
}
