package incusapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// ListedInstanceIdentity identifies one row in a complete authenticated Incus
// instance list. Only the pinned transport can populate its private observation.
// It carries no authority over jobs, storage, or retirement.
type ListedInstanceIdentity struct {
	observation ListedInstanceObservation
}

// ListedInstanceObservation is a copy of the bounded fields and collection
// interval. Only the opaque ListedInstanceIdentity can be attached to a charge.
type ListedInstanceObservation struct {
	Subject                InstanceIdentitySubject
	StartedAt, CompletedAt time.Time
	FenceDigest            [32]byte
}

func (i ListedInstanceIdentity) Observation() ListedInstanceObservation { return i.observation }

type inventoryFenceKey struct{}

// WithInventoryFence binds this list request to the random nonce returned by
// BeginReconciliation. The nonce is local collection context and is never sent
// to the daemon. A sealed observation from an earlier barrier cannot be rebound.
func WithInventoryFence(ctx context.Context, nonce string) context.Context {
	var digest [32]byte
	if len(nonce) == 32 {
		digest = sha256.Sum256([]byte(nonce))
	}
	return context.WithValue(ctx, inventoryFenceKey{}, digest)
}

func (c *Client) listedIdentity(fence [32]byte, physical *PhysicalServerIdentity, response envelope, raw json.RawMessage, instance Instance, started, completed time.Time) ListedInstanceIdentity {
	if fence == ([32]byte{}) || physical == nil || !physical.Valid() || physical.ServerVersion() != "6.0.4" || physical.ServerKeyFingerprint() != response.serverKeyFingerprint {
		return ListedInstanceIdentity{}
	}
	fields, config, err := exactInstanceEvidenceFields(raw)
	if err != nil || rejectFolded(fields, "project") != nil {
		return ListedInstanceIdentity{}
	}
	if _, reported := fields["project"]; reported {
		project, err := field[string](fields, "project")
		if err != nil || project != c.project {
			return ListedInstanceIdentity{}
		}
	}
	createdAt, err := identityTime(fields, "created_at")
	if err != nil || createdAt.After(completed) {
		return ListedInstanceIdentity{}
	}
	uuid, uuidErr := field[string](config, "volatile.uuid")
	generation, genErr := field[string](config, "volatile.uuid.generation")
	const nilUUID = "00000000-0000-0000-0000-000000000000"
	if uuidErr != nil || genErr != nil || !operationID(uuid) || !operationID(generation) || uuid == nilUUID || generation == nilUUID {
		return ListedInstanceIdentity{}
	}
	return ListedInstanceIdentity{observation: ListedInstanceObservation{Subject: InstanceIdentitySubject{
		BackendKeyFingerprint: physical.ServerKeyFingerprint(), OriginFingerprint: physical.OriginFingerprint(),
		ScopeFingerprint: physical.ScopeFingerprint(), ServerName: physical.ServerName(), ServerVersion: physical.ServerVersion(),
		Project: c.project, Name: instance.Name, UUID: uuid, Generation: generation, CreatedAt: createdAt,
		ControllerID: instance.ControllerID, PoolID: instance.PoolID, ReservationKey: instance.ReservationKey,
		CPUMillis: instance.CPUMillis, RAMMiB: instance.RAMMiB,
	}, StartedAt: started, CompletedAt: completed, FenceDigest: fence}}
}
