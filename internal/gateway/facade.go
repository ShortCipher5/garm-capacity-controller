// Package gateway binds trusted placement policy, durable admission and Incus
// transport. It is a server-side library, not a public HTTP/provider endpoint.
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	"github.com/ShortCipher5/garm-capacity-controller/internal/garmprotocol"
	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	"github.com/cloudbase/garm-provider-common/cloudconfig"
	common "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm-provider-common/util"
)

var ErrPolicy = errors.New("provider request conflicts with trusted policy")
var ErrUnavailable = errors.New("provider state unavailable; allocation remains charged")

// Backend is implemented by the concrete mTLS incusapi.Client. Only the gateway
// owner supplies implementations; a repository or browser cannot register one.
type Backend interface {
	Create(context.Context, incusapi.InstanceSpec) (incusapi.Created, error)
	Wait(context.Context, incusapi.Created) error
	Instance(context.Context, string) (incusapi.Instance, error)
	Instances(context.Context) ([]incusapi.Instance, error)
	Operations(context.Context) ([]incusapi.Operation, error)
}

// PoolPolicy is trusted server configuration. Host is the legacy single-host
// shorthand; Hosts enables atomic durable selection. Flavor/Image are Garm-side
// identifiers; neither can override the pinned image or explicit allocation.
type PoolPolicy struct {
	Host        string        `json:"host,omitempty"`
	Hosts       []string      `json:"hosts,omitempty"`
	Strategy    string        `json:"strategy,omitempty"`
	CPU         int64         `json:"cpu_millis"`
	RAM         int64         `json:"ram_mib"`
	Flavor      string        `json:"flavor"`
	Image       string        `json:"image"`
	Fingerprint string        `json:"image_fingerprint"`
	Profiles    []string      `json:"profiles"`
	Type        string        `json:"type"`
	Arch        common.OSArch `json:"arch"`
}

type Facade struct {
	store             *admission.SQLiteStore
	controller        string
	pools             map[string]PoolPolicy
	hosts             map[string]Backend
	preparationRoutes map[string]preparationRoute
	preparationSource PreparationSource
}

func key(controller, pool, name string) string {
	sum := sha256.Sum256([]byte(controller + "\x00" + pool + "\x00" + name))
	return hex.EncodeToString(sum[:])
}

func New(ctx context.Context, store *admission.SQLiteStore, controller string, pools map[string]PoolPolicy, hosts map[string]Backend, preparations ...PreparationSettings) (*Facade, error) {
	if store == nil || controller == "" || strings.ContainsRune(controller, 0) || len(pools) == 0 || len(hosts) == 0 {
		return nil, ErrPolicy
	}
	state, err := store.Snapshot(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := &Facade{store: store, controller: controller, pools: map[string]PoolPolicy{}, hosts: map[string]Backend{}}
	for host, backend := range hosts {
		if backend == nil {
			return nil, ErrPolicy
		}
		if _, ok := state.Config.Hosts[host]; !ok {
			return nil, ErrPolicy
		}
		f.hosts[host] = backend
	}
	if len(hosts) != len(state.Config.Hosts) {
		return nil, ErrPolicy
	}
	for id, policy := range pools {
		configured, ok := state.Config.Pools[id]
		ids, hostErr := placementHosts(policy)
		if !ok || hostErr != nil || policy.Flavor == "" || policy.Image == "" || (policy.Arch != common.Amd64 && policy.Arch != common.Arm64) || strings.ContainsRune(id, 0) {
			return nil, ErrPolicy
		}
		for _, host := range ids {
			allowed := false
			for _, configuredHost := range configured.Hosts {
				if host == configuredHost {
					allowed = true
				}
			}
			if !allowed || f.hosts[host] == nil {
				return nil, ErrPolicy
			}
		}
		if policy.Strategy == "" {
			policy.Strategy = "first_fit"
		}
		if policy.Strategy != "first_fit" && policy.Strategy != "least_runners" {
			return nil, ErrPolicy
		}
		policy.Hosts = ids

		spec := incusapi.InstanceSpec{Name: "policy-validation", Type: policy.Type, Profiles: policy.Profiles, ImageFingerprint: policy.Fingerprint, CPUMillis: policy.CPU, RAMMiB: policy.RAM, UserData: "validation", ControllerID: controller, PoolID: id, ReservationKey: strings.Repeat("0", 64)}
		if incusapi.ValidateSpec(spec) != nil {
			return nil, ErrPolicy
		}
		policy.Profiles = append([]string(nil), policy.Profiles...)
		f.pools[id] = policy
	}
	if len(preparations) > 1 {
		return nil, ErrPolicy
	}
	if len(preparations) == 1 {
		if err := f.configurePreparations(preparations[0]); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *Facade) request(name, pool string) (admission.Request, PoolPolicy, error) {
	policy, ok := f.pools[pool]
	if !ok || name == "" || strings.ContainsRune(name, 0) {
		return admission.Request{}, PoolPolicy{}, ErrPolicy
	}
	req := admission.Request{Key: key(f.controller, pool, name), Pool: pool, Host: policy.Hosts[0], Resources: admission.Resources{CPU: policy.CPU, RAM: policy.RAM, Runners: 1}}
	spec := incusapi.InstanceSpec{Name: name, Type: policy.Type, Profiles: policy.Profiles, ImageFingerprint: policy.Fingerprint, CPUMillis: policy.CPU, RAMMiB: policy.RAM, UserData: "validation", ControllerID: f.controller, PoolID: pool, ReservationKey: req.Key}
	if incusapi.ValidateSpec(spec) != nil {
		return admission.Request{}, PoolPolicy{}, ErrPolicy
	}
	return req, policy, nil
}

// Admit returns the strict pre-JIT wire receipt. It never renders bootstrap,
// contacts Incus or emits a dispatch permit. Errors are not child-output text.
func (f *Facade) Admit(ctx context.Context, request garmprotocol.Request) (int, []byte, error) {
	if request.Validate() != nil {
		return 0, nil, ErrPolicy
	}
	req, policy, err := f.request(request.Name, request.PoolID)
	if err != nil {
		return 0, nil, err
	}
	if f.preparationAdmissionCheck(ctx, req.Pool) != nil {
		// Known upstream/configuration drift is benign backpressure BEFORE a
		// reservation or source JIT. Never adopt a newer generation silently.
		err = admission.ErrReconciliation
	} else {
		_, err = f.store.AdmitPlacement(ctx, admission.PlacementRequest{Key: req.Key, Pool: req.Pool, Resources: req.Resources, Execution: executionImage(f.controller, request.Name, policy), Hosts: append([]string(nil), policy.Hosts...), Strategy: policy.Strategy})
	}
	no := false
	response := garmprotocol.Response{Protocol: garmprotocol.Version, Name: request.Name, PoolID: request.PoolID, Dispatched: &no}
	exit := 0
	if err == nil {
		response.Outcome = "reserved"
		response.ReservationKey = req.Key
	} else if errors.Is(err, admission.ErrDenied) || errors.Is(err, admission.ErrReconciliation) || errors.Is(err, admission.ErrPoolRouteInactive) {
		exit = garmprotocol.DeferredExitCode
		response.Outcome = "deferred"
		response.RetryAfterSeconds = 10
	} else {
		return 0, nil, ErrUnavailable
	}
	data, err := garmprotocol.Encode(exit, response, request)
	return exit, data, err
}

func (f *Facade) verified(ctx context.Context, req admission.Request, name string) (incusapi.Instance, error) {
	instance, err := f.hosts[req.Host].Instance(ctx, name)
	if err != nil || instance.Name != name || instance.ControllerID != f.controller || instance.PoolID != req.Pool || instance.ReservationKey != req.Key || instance.CPUMillis != req.Resources.CPU || instance.RAMMiB != req.Resources.RAM || instance.Status != 103 {
		return incusapi.Instance{}, ErrUnavailable
	}
	return instance, nil
}

// Create consumes only an exact existing preflight reservation. Bootstrap is
// produced by the authenticated Garm controller, never by an arbitrary job.
// Official pinned provider-common cloud-init rendering performs no download.
// The bootstrap and rendered userdata are neither journaled nor logged here.
func (f *Facade) Create(ctx context.Context, bootstrap common.BootstrapInstance) (common.ProviderInstance, error) {
	plan, policy, err := f.planned(ctx, bootstrap.Name, bootstrap.PoolID)
	req := plan.Request
	if err == nil && !f.planAllowed(plan) {
		return common.ProviderInstance{}, ErrUnavailable
	}
	if err != nil || bootstrap.Flavor != policy.Flavor || bootstrap.Image != policy.Image || bootstrap.OSType != common.Linux || bootstrap.OSArch != policy.Arch || (len(bootstrap.ExtraSpecs) != 0 && !bytes.Equal(bytes.TrimSpace(bootstrap.ExtraSpecs), []byte("{}"))) {
		return common.ProviderInstance{}, ErrPolicy
	}
	requirePreparation := f.PreparationConfigured() || f.RuntimeOriginalPreparationRequired()
	var sourceReceipt admission.OriginalPreparationReceipt
	var sourceRow admission.GarmRowObservation
	if requirePreparation {
		sourceReceipt, sourceRow, err = f.preparedCreate(ctx, plan, bootstrap.JitConfigEnabled)
		if err != nil {
			return common.ProviderInstance{}, ErrUnavailable
		}
	}
	// Restrict this slice to the official default cloud-init path, not custom scripts.
	if bootstrap.UserDataOptions.EnableBootDebug || len(bootstrap.UserDataOptions.ExtraPackages) != 0 {
		return common.ProviderInstance{}, ErrPolicy
	}
	tools, err := util.GetTools(bootstrap.OSType, bootstrap.OSArch, bootstrap.Tools)
	if err != nil {
		return common.ProviderInstance{}, ErrPolicy
	}
	userdata, err := cloudconfig.GetCloudConfig(bootstrap, tools, bootstrap.Name)
	if err != nil {
		return common.ProviderInstance{}, ErrPolicy
	}
	spec := incusapi.InstanceSpec{Name: bootstrap.Name, Type: policy.Type, Profiles: append([]string(nil), policy.Profiles...), ImageFingerprint: policy.Fingerprint, CPUMillis: policy.CPU, RAMMiB: policy.RAM, UserData: userdata, ControllerID: f.controller, PoolID: req.Pool, ReservationKey: req.Key}
	if incusapi.ValidateSpec(spec) != nil {
		return common.ProviderInstance{}, ErrPolicy
	}
	binding, err := f.boundBackend(ctx, req.Host)
	if err != nil {
		return common.ProviderInstance{}, ErrUnavailable
	}
	execute := func(ctx context.Context, permit admission.DispatchPermit) (admission.DispatchReceipt, error) {
		created, createErr := f.hosts[req.Host].Create(ctx, spec)
		receipt := admission.DispatchReceipt{State: "unknown", OperationID: created.OperationID, ResourceID: created.ResourceID}
		if createErr != nil {
			return receipt, ErrUnavailable
		}
		if created.ResourceID != bootstrap.Name {
			return receipt, ErrUnavailable
		}
		// The physical POST has happened. Persist its validated correlation
		// BEFORE waiting so a crash during wait cannot erase a received UUID.
		// This is not terminal-operation proof, a redispatch or release permit.
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		submitted := admission.DispatchReceipt{State: "submitted", OperationID: created.OperationID, ResourceID: created.ResourceID}
		persistErr := f.store.RecordDispatchOutcome(persist, permit, submitted)
		cancel()
		if persistErr != nil {
			return receipt, ErrUnavailable
		}
		if err := f.hosts[req.Host].Wait(ctx, created); err != nil {
			return receipt, ErrUnavailable
		}
		if _, verifyErr := f.verified(ctx, req, bootstrap.Name); verifyErr != nil {
			return receipt, verifyErr
		}
		if f.collectCreateEvidence(ctx, req, created) == createEvidenceWriterBlocked {
			return receipt, ErrUnavailable
		}
		receipt.State = "confirmed"
		return receipt, nil
	}
	if requirePreparation {
		_, err = f.store.ExecutePreparedDispatch(ctx, plan, binding, sourceReceipt, sourceRow, execute)
	} else {
		_, err = f.store.ExecutePlannedDispatch(ctx, plan, binding, execute)
	}
	if err != nil {
		// A confirmed replay may return the same verified resource, but never create.
		if !errors.Is(err, admission.ErrDispatchClaimed) {
			return common.ProviderInstance{}, ErrUnavailable
		}
		record, lookupErr := f.store.LookupDispatch(ctx, req.Key)
		if lookupErr != nil || record.State != "confirmed" || record.ResourceID != bootstrap.Name || record.HostBindingSequence != binding.Sequence || (requirePreparation && record.OriginalPreparationSequence != sourceReceipt.RecordSequence) {
			return common.ProviderInstance{}, ErrUnavailable
		}
	}
	if _, err := f.verified(ctx, req, bootstrap.Name); err != nil {
		return common.ProviderInstance{}, err
	}
	return common.ProviderInstance{ProviderID: bootstrap.Name, Name: bootstrap.Name, OSType: common.Linux, OSArch: policy.Arch, Status: common.InstanceRunning}, nil
}

// CancelAdmission cancels only an exact provably never-dispatched reservation.
// The ledger atomically checks dispatch/inventory and tombstones its identity.
// This cannot cancel an unknown create, retire a runner or issue a backend call.
func (f *Facade) CancelAdmission(ctx context.Context, request garmprotocol.Request) (garmprotocol.CancellationResponse, error) {
	if request.Validate() != nil {
		return garmprotocol.CancellationResponse{}, ErrPolicy
	}
	plan, _, err := f.planned(ctx, request.Name, request.PoolID)
	req := plan.Request
	if err != nil {
		return garmprotocol.CancellationResponse{}, err
	}
	cancelled, err := f.store.CancelUndispatched(ctx, req)
	if err != nil {
		return garmprotocol.CancellationResponse{}, ErrUnavailable
	}
	no := false
	replay := cancelled.Replay
	result := garmprotocol.CancellationResponse{Protocol: garmprotocol.Version, Outcome: "cancelled", Name: request.Name, PoolID: request.PoolID, Dispatched: &no, ReservationKey: req.Key, Replay: &replay}
	if _, err := garmprotocol.EncodeCancellation(result, request); err != nil {
		return garmprotocol.CancellationResponse{}, ErrUnavailable
	}
	return result, nil
}
