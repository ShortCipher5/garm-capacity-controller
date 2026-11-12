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
	RowID               string
	ProviderName        string
	AgentID             int64
	Generation          uint64
	IdentityFieldsKnown bool
	ScaleSetID          uint64
	// PreparationPending is set only by the pinned API parser for the exact
	// pending_create/pending/zero-agent/no-provider/no-job shape. It alone grants
	// nothing: immutable plan, no dispatch and complete physical proof are required.
	PreparationPending bool
	// SourcePreparationPending is only a canonical row-shape candidate; a durable
	// original source receipt and full physical reconciliation remain mandatory.
	SourcePreparationPending bool
	Name                     string
	ProviderID               string
	PoolID                   string
	State                    string
	Busy                     bool
	Idle                     bool
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
	preparations := map[[2]string]admission.GarmPreparation{}
	seen := map[string]bool{}
	for _, row := range rows {
		req, err := f.accountingRequest(state, row.Name, row.PoolID, "")
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

		preparation, eligible, err := f.garmPreparation(ctx, fence, state, row, req, observedAt)
		if err != nil {
			return ErrUnavailable
		}
		if eligible {
			preparations[[2]string{req.Host, row.Name}] = preparation
		}
	}
	snapshot := admission.InventorySnapshot{}
	physicalNames := map[string]bool{}
	for host, backend := range f.hosts {
		instances, err := backend.Instances(ctx)
		if err != nil {
			return ErrUnavailable
		}
		hostNames := map[string]bool{}
		for _, instance := range instances {
			_, known := f.pools[instance.PoolID]
			if !known || physicalNames[instance.Name] || instance.ControllerID != f.controller || instance.CPUMillis <= 0 || instance.RAMMiB <= 0 || hostNames[instance.Name] {
				return ErrUnavailable
			}
			req, err := f.accountingRequest(state, instance.Name, instance.PoolID, host)
			if err != nil {
				return ErrUnavailable
			}
			hostNames[instance.Name] = true
			physicalNames[instance.Name] = true
			delete(preparations, [2]string{host, instance.Name})
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

	for identity, preparation := range preparations {
		// Only after ALL physical/operation collections succeeded may this exact
		// nonphysical row be omitted. The reservation remains fully charged and the
		// commit transaction rechecks every plan/binding/no-dispatch/history guard.
		delete(inventory, identity)
		snapshot.Preparations = append(snapshot.Preparations, preparation)
	}
	for _, resource := range inventory {
		snapshot.Resources = append(snapshot.Resources, resource)
	}
	if err := f.store.CompleteReconciliation(ctx, fence, snapshot); err != nil {
		return ErrUnavailable
	}
	return nil
}
