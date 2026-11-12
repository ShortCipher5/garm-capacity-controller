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
	req, policy, err := f.request(name, pool)
	if err != nil {
		return common.ProviderInstance{}, err
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
	instances, err := f.hosts[policy.Host].Instances(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	result := []common.ProviderInstance{}
	seen := map[string]bool{}
	for _, instance := range instances {
		if instance.ControllerID != f.controller || instance.PoolID != pool {
			continue
		}
		req, _, err := f.request(instance.Name, pool)
		if err != nil || seen[instance.Name] || instance.CPUMillis <= 0 || instance.RAMMiB <= 0 {
			return nil, ErrUnavailable
		}
		seen[instance.Name] = true
		if instance.ReservationKey != "" && instance.ReservationKey != req.Key {
			return nil, ErrUnavailable
		}
		result = append(result, providerView(instance, policy))
	}
	return result, nil
}
