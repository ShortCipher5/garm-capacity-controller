package gateway

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
	common "github.com/cloudbase/garm-provider-common/params"
	"reflect"
	"sort"
)

func placementHosts(policy PoolPolicy) ([]string, error) {
	ids := append([]string(nil), policy.Hosts...)
	if policy.Host != "" {
		if len(ids) != 0 {
			return nil, ErrPolicy
		}
		ids = []string{policy.Host}
	}
	if len(ids) == 0 || len(ids) > 128 {
		return nil, ErrPolicy
	}
	sort.Strings(ids)
	for i, id := range ids {
		if id == "" || (i > 0 && ids[i-1] == id) {
			return nil, ErrPolicy
		}
	}
	return ids, nil
}
func executionImage(controller, name string, p PoolPolicy) admission.ExecutionImage {
	return admission.ExecutionImage{Controller: controller, Name: name, Flavor: p.Flavor, Image: p.Image, Fingerprint: p.Fingerprint, Profiles: append([]string(nil), p.Profiles...), Type: p.Type, Arch: string(p.Arch)}
}
func (f *Facade) planned(ctx context.Context, name, pool string) (admission.ExecutionPlan, PoolPolicy, error) {
	if _, ok := f.pools[pool]; !ok {
		return admission.ExecutionPlan{}, PoolPolicy{}, ErrPolicy
	}
	p, err := f.store.LookupExecutionPlan(ctx, key(f.controller, pool, name))
	if err != nil || p.Execution.Controller != f.controller || p.Execution.Name != name || p.Request.Pool != pool || f.hosts[p.Request.Host] == nil {
		return admission.ExecutionPlan{}, PoolPolicy{}, ErrUnavailable
	}
	binding, err := f.store.LookupHostBinding(ctx, p.Request.Host)
	if err != nil || p.HostBindingSequence != binding.Sequence {
		return admission.ExecutionPlan{}, PoolPolicy{}, ErrUnavailable
	}
	image := p.Execution
	policy := PoolPolicy{Host: p.Request.Host, CPU: p.Request.Resources.CPU, RAM: p.Request.Resources.RAM, Flavor: image.Flavor, Image: image.Image, Fingerprint: image.Fingerprint, Profiles: append([]string(nil), image.Profiles...), Type: image.Type, Arch: common.OSArch(image.Arch)}
	return p, policy, nil
}

// accountingRequest resolves an already charged reservation independently of
// mutable pool defaults. Orphans may be charged only to an actually observed
// host; an unlocated Garm row cannot guess a multi-host placement.
func (f *Facade) accountingRequest(state admission.SQLiteSnapshot, name, pool, observedHost string) (admission.Request, error) {
	req, policy, err := f.request(name, pool)
	if err != nil {
		return admission.Request{}, err
	}
	if reservation, exists := state.Reservations[req.Key]; exists {
		old := reservation.Request
		if old.Key != req.Key || old.Pool != pool || f.hosts[old.Host] == nil || (observedHost != "" && old.Host != observedHost) {
			return admission.Request{}, ErrUnavailable
		}
		return old, nil
	}
	if observedHost != "" {
		if f.hosts[observedHost] == nil {
			return admission.Request{}, ErrUnavailable
		}
		req.Host = observedHost
		return req, nil
	}
	if len(policy.Hosts) != 1 {
		return admission.Request{}, ErrUnavailable
	}
	return req, nil
}

// A stored plan preserves history, not an exemption from a later trusted
// startup-policy revocation. Refuse drift; never substitute the new image or
// move the old reservation. Current ledger budgets/eligibility are rechecked
// atomically by claim. This cannot revoke another still-running process.
func (f *Facade) planAllowed(plan admission.ExecutionPlan) bool {
	current, ok := f.pools[plan.Request.Pool]
	if !ok {
		return false
	}
	hostAllowed := false
	for _, host := range current.Hosts {
		if host == plan.Request.Host {
			hostAllowed = true
		}
	}
	return hostAllowed && plan.Request.Resources == (admission.Resources{CPU: current.CPU, RAM: current.RAM, Runners: 1}) && reflect.DeepEqual(plan.Execution, executionImage(f.controller, plan.Execution.Name, current))
}
