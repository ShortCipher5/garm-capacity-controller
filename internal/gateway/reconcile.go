package gateway

import (
	"context"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/admission"
)

// GarmInstance is the non-secret subset of an authoritative Garm inventory.
// GarmSource must return ALL rows assigned to this gateway controller, including
// creating, error, unknown and deleted-but-not-yet-retired rows. A partial API
// response or authorization failure must be an error, never an empty success.
type GarmInstance struct {
	Name       string
	ProviderID string
	PoolID     string
	State      string
	Busy       bool
	Idle       bool
}
type GarmSource interface {
	Instances(context.Context) ([]GarmInstance, error)
}

// Reconcile installs its durable global barrier BEFORE reading any source.
// It never retires a missing allocation, releases capacity, cancels operations
// or certifies that a project covers every allocation on a physical host.
// Host/project exclusivity and other provisioning paths remain deployment gates.
func (f *Facade) Reconcile(ctx context.Context, garm GarmSource) error {
	fence, err := f.store.BeginReconciliation(ctx)
	if err != nil {
		return ErrUnavailable
	}
	if garm == nil {
		return ErrPolicy
	}
	state, err := f.store.Snapshot(ctx)
	if err != nil {
		return ErrUnavailable
	}
	// Freshness is measured from collection start, not the end of a slow last
	// request. Earlier source views must not be relabelled freshly observed.
	observedAt := time.Now()
	if err := f.bindBackends(ctx, fence, observedAt); err != nil {
		return err
	}
	rows, err := garm.Instances(ctx)
	if err != nil {
		return ErrUnavailable
	}
	inventory := map[[2]string]admission.InventoryResource{}
	seen := map[string]bool{}
	for _, row := range rows {
		req, _, err := f.request(row.Name, row.PoolID)
		if err != nil || seen[row.Name] || (row.ProviderID != "" && row.ProviderID != row.Name) {
			return ErrUnavailable
		}
		seen[row.Name] = true
		resource := admission.InventoryResource{ID: row.Name, Host: req.Host, Pool: req.Pool, Resources: req.Resources, State: "unknown"}
		if reservation, ok := state.Reservations[req.Key]; ok {
			if reservation.Request != req {
				return ErrUnavailable
			}
			resource.ReservationKey = req.Key
		}
		if row.Busy {
			resource.State = "running"
		} else {
			switch row.State {
			case "creating", "pending_create":
				resource.State = "creating"
			case "error":
				resource.State = "failed"
			case "running":
				if row.Idle {
					resource.State = "idle"
				}
			case "deleting", "pending_delete", "pending_force_delete":
				resource.State = "deleting"
			}
		}
		inventory[[2]string{req.Host, row.Name}] = resource
	}
	snapshot := admission.InventorySnapshot{}
	for host, backend := range f.hosts {
		instances, err := backend.Instances(ctx)
		if err != nil {
			return ErrUnavailable
		}
		hostNames := map[string]bool{}
		for _, instance := range instances {
			policy, known := f.pools[instance.PoolID]
			if !known || policy.Host != host || instance.ControllerID != f.controller || instance.CPUMillis <= 0 || instance.RAMMiB <= 0 || hostNames[instance.Name] {
				return ErrUnavailable
			}
			req, _, err := f.request(instance.Name, instance.PoolID)
			if err != nil {
				return ErrUnavailable
			}
			hostNames[instance.Name] = true
			identity := [2]string{host, instance.Name}
			resource, exists := inventory[identity]
			if exists && resource.Pool != instance.PoolID {
				return ErrUnavailable
			}
			if !exists {
				resource = admission.InventoryResource{ID: instance.Name, Host: host, Pool: instance.PoolID, Resources: admission.Resources{Runners: 1}, State: "unknown"}
			}
			if instance.ReservationKey != "" {
				if instance.ReservationKey != req.Key {
					return ErrUnavailable
				}
				if reservation, ok := state.Reservations[req.Key]; ok {
					if reservation.Request != req {
						return ErrUnavailable
					}
					resource.ReservationKey = req.Key
				}
			} else if resource.ReservationKey != "" {
				return ErrUnavailable
			}
			resource.Resources.CPU = max(resource.Resources.CPU, instance.CPUMillis)
			resource.Resources.RAM = max(resource.Resources.RAM, instance.RAMMiB)
			// Provider Running is not proof of GitHub job-idleness. Unknown stays unknown
			// without a Garm row; every state has the same conservative allocation charge.
			if instance.Status != 103 && resource.State != "deleting" {
				resource.State = "unknown"
			}
			inventory[identity] = resource
		}
		operations, err := backend.Operations(ctx)
		if err != nil {
			return ErrUnavailable
		}
		operationIDs := map[string]bool{}
		for _, operation := range operations {
			if operation.ID == "" || operationIDs[operation.ID] {
				return ErrUnavailable
			}
			operationIDs[operation.ID] = true
			switch operation.Status {
			case 200, 400, 401:
			default:
				return ErrUnavailable
			}
			for _, name := range operation.Instances {
				if !hostNames[name] {
					return ErrUnavailable
				}
			}
		}
		snapshot.Hosts = append(snapshot.Hosts, admission.HostObservation{Host: host, ObservedAt: observedAt, Healthy: true, GarmComplete: true, IncusComplete: true, OperationsComplete: true, Bounded: true})
	}
	for _, resource := range inventory {
		snapshot.Resources = append(snapshot.Resources, resource)
	}
	if err := f.store.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		return ErrUnavailable
	}
	return nil
}
