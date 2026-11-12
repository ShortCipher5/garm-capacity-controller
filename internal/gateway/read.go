package gateway

import (
	"context"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
	common "github.com/cloudbase/garm-provider-common/params"
)

func providerView(instance incusapi.Instance, policy PoolPolicy) common.ProviderInstance {
	status := common.InstanceStatusUnknown
	switch instance.Status {
	case 103:
		status = common.InstanceRunning
	case 102:
		status = common.InstanceStopped
	}
	return common.ProviderInstance{ProviderID: instance.Name, Name: instance.Name, OSType: common.Linux, OSArch: policy.Arch, Status: status}
}

// Get never converts a failed read or uncertain dispatch to provider-not-found.
// Proof-based absence and retirement are deliberately not implemented yet.
func (f *Facade) Get(ctx context.Context, name, pool string) (common.ProviderInstance, error) {
	plan, policy, err := f.planned(ctx, name, pool)
	req := plan.Request
	if err != nil {
		return common.ProviderInstance{}, err
	}
	if _, err := f.boundBackend(ctx, req.Host); err != nil {
		return common.ProviderInstance{}, ErrUnavailable
	}
	instance, err := f.hosts[req.Host].Instance(ctx, name)
	if err != nil || instance.Name != name || instance.ControllerID != f.controller || instance.PoolID != pool || instance.ReservationKey != req.Key || instance.CPUMillis != req.Resources.CPU || instance.RAMMiB != req.Resources.RAM {
		return common.ProviderInstance{}, ErrUnavailable
	}
	return providerView(instance, policy), nil
}
func (f *Facade) List(ctx context.Context, pool string) ([]common.ProviderInstance, error) {
	policy, ok := f.pools[pool]
	if !ok {
		return nil, ErrPolicy
	}
	result := []common.ProviderInstance{}
	seen := map[string]bool{}
	state, err := f.store.ReadSnapshot(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	// Include every enrolled host, not just currently eligible destinations.
	// Removing an eligibility entry cannot hide a previously placed runner.
	for host, backend := range f.hosts {
		if _, err := f.boundBackend(ctx, host); err != nil {
			return nil, ErrUnavailable
		}
		instances, err := backend.Instances(ctx)
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, instance := range instances {
			if instance.ControllerID != f.controller || instance.PoolID != pool {
				continue
			}
			req, err := f.accountingRequest(state, instance.Name, pool, host)
			if err != nil || seen[instance.Name] || instance.CPUMillis <= 0 || instance.RAMMiB <= 0 || (instance.ReservationKey != "" && instance.ReservationKey != req.Key) {
				return nil, ErrUnavailable
			}
			seen[instance.Name] = true
			viewPolicy := policy
			if plan, storedPolicy, err := f.planned(ctx, instance.Name, pool); err == nil {
				if plan.Request.Host != host {
					return nil, ErrUnavailable
				}
				viewPolicy = storedPolicy
			}
			result = append(result, providerView(instance, viewPolicy))
		}
	}
	return result, nil
}
