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
	CreateAndWait(context.Context, incusapi.InstanceSpec) (incusapi.Created, error)
	Instance(context.Context, string) (incusapi.Instance, error)
	Instances(context.Context) ([]incusapi.Instance, error)
	Operations(context.Context) ([]incusapi.Operation, error)
}

// PoolPolicy is trusted server configuration. Fixed placement intentionally
// precedes a future transactional host chooser. Flavor/Image are the Garm-side
// identifiers; neither can override the pinned image or explicit allocation.
type PoolPolicy struct {
	Host        string        `json:"host"`
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
	store      *admission.SQLiteStore
	controller string
	pools      map[string]PoolPolicy
	hosts      map[string]Backend
}

func key(controller, pool, name string) string {
	sum := sha256.Sum256([]byte(controller + "\x00" + pool + "\x00" + name))
	return hex.EncodeToString(sum[:])
}

func New(ctx context.Context, store *admission.SQLiteStore, controller string, pools map[string]PoolPolicy, hosts map[string]Backend) (*Facade, error) {
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
		allowed := false
		for _, host := range configured.Hosts {
			if host == policy.Host {
				allowed = true
			}
		}
		if !ok || !allowed || f.hosts[policy.Host] == nil || policy.Flavor == "" || policy.Image == "" || (policy.Arch != common.Amd64 && policy.Arch != common.Arm64) || strings.ContainsRune(id, 0) {
			return nil, ErrPolicy
		}
		spec := incusapi.InstanceSpec{Name: "policy-validation", Type: policy.Type, Profiles: policy.Profiles, ImageFingerprint: policy.Fingerprint, CPUMillis: policy.CPU, RAMMiB: policy.RAM, UserData: "validation", ControllerID: controller, PoolID: id, ReservationKey: strings.Repeat("0", 64)}
		if incusapi.ValidateSpec(spec) != nil {
			return nil, ErrPolicy
		}
		policy.Profiles = append([]string(nil), policy.Profiles...)
		f.pools[id] = policy
	}
	return f, nil
}

func (f *Facade) request(name, pool string) (admission.Request, PoolPolicy, error) {
	policy, ok := f.pools[pool]
	if !ok || name == "" || strings.ContainsRune(name, 0) {
		return admission.Request{}, PoolPolicy{}, ErrPolicy
	}
	req := admission.Request{Key: key(f.controller, pool, name), Pool: pool, Host: policy.Host, Resources: admission.Resources{CPU: policy.CPU, RAM: policy.RAM, Runners: 1}}
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
	req, _, err := f.request(request.Name, request.PoolID)
	if err != nil {
		return 0, nil, err
	}
	_, err = f.store.Admit(ctx, req)
	no := false
	response := garmprotocol.Response{Protocol: garmprotocol.Version, Name: request.Name, PoolID: request.PoolID, Dispatched: &no}
	exit := 0
	if err == nil {
		response.Outcome = "reserved"
		response.ReservationKey = req.Key
	} else if errors.Is(err, admission.ErrDenied) || errors.Is(err, admission.ErrReconciliation) {
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
	req, policy, err := f.request(bootstrap.Name, bootstrap.PoolID)
	if err != nil || bootstrap.Flavor != policy.Flavor || bootstrap.Image != policy.Image || bootstrap.OSType != common.Linux || bootstrap.OSArch != policy.Arch || (len(bootstrap.ExtraSpecs) != 0 && !bytes.Equal(bytes.TrimSpace(bootstrap.ExtraSpecs), []byte("{}"))) {
		return common.ProviderInstance{}, ErrPolicy
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
	_, err = f.store.ExecuteDispatch(ctx, req, func(ctx context.Context, _ admission.DispatchPermit) (admission.DispatchReceipt, error) {
		created, createErr := f.hosts[req.Host].CreateAndWait(ctx, spec)
		receipt := admission.DispatchReceipt{State: "unknown", OperationID: created.OperationID, ResourceID: created.ResourceID}
		if createErr != nil {
			return receipt, ErrUnavailable
		}
		if created.ResourceID != bootstrap.Name {
			return receipt, ErrUnavailable
		}
		if _, verifyErr := f.verified(ctx, req, bootstrap.Name); verifyErr != nil {
			return receipt, verifyErr
		}
		receipt.State = "confirmed"
		return receipt, nil
	})
	if err != nil {
		// A confirmed replay may return the same verified resource, but never create.
		if !errors.Is(err, admission.ErrDispatchClaimed) {
			return common.ProviderInstance{}, ErrUnavailable
		}
		record, lookupErr := f.store.LookupDispatch(ctx, req.Key)
		if lookupErr != nil || record.State != "confirmed" || record.ResourceID != bootstrap.Name {
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
	req, _, err := f.request(request.Name, request.PoolID)
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
