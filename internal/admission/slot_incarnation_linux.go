package admission

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/incusapi"
)

// Existing inventory and tombstones are opaque by default. Store identity on
// the accounting row itself so a missing auxiliary record cannot hide an alias.
// Some migration fixtures retain newer tables with an older user_version.
func migrateSlotIncarnations(tx *sql.Tx) error {
	var inventoryUpgraded, releasesUpgraded bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('inventory') WHERE name='slot_subject'),
 EXISTS(SELECT 1 FROM pragma_table_info('releases') WHERE name='physical_pair')`).Scan(&inventoryUpgraded, &releasesUpgraded); err != nil {
		return err
	}
	if !inventoryUpgraded {
		if _, err := tx.Exec(`ALTER TABLE inventory ADD COLUMN slot_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE inventory ADD COLUMN physical_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE inventory ADD COLUMN physical_pair TEXT NOT NULL DEFAULT '';
ALTER TABLE inventory ADD COLUMN physical_created_at TEXT NOT NULL DEFAULT '';
ALTER TABLE inventory ADD COLUMN identity_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inventory ADD COLUMN last_seen_at TEXT NOT NULL DEFAULT '';
ALTER TABLE inventory ADD COLUMN incarnation_conflict INTEGER NOT NULL DEFAULT 0 CHECK(incarnation_conflict IN (0,1));`); err != nil {
			return err
		}
	}
	if !releasesUpgraded {
		if _, err := tx.Exec(`ALTER TABLE releases ADD COLUMN physical_pair TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN scoped_resource_id TEXT NOT NULL DEFAULT '';`); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS slot_inventory_epoch ON inventory(identity_epoch);
CREATE INDEX IF NOT EXISTS slot_inventory_pair ON inventory(physical_pair);
CREATE INDEX IF NOT EXISTS slot_inventory_id ON inventory(resource_id);
CREATE INDEX IF NOT EXISTS slot_inventory_conflicts ON inventory(incarnation_conflict) WHERE incarnation_conflict=1;
CREATE INDEX IF NOT EXISTS slot_release_pair ON releases(physical_pair);
CREATE INDEX IF NOT EXISTS slot_release_id ON releases(scoped_resource_id);
UPDATE metadata SET ready=0,nonce='',began_at='' WHERE id=1;
PRAGMA user_version=31;`)
	return err
}

// WithListedIdentity attaches the authenticated Incus list row to its accounting
// row for this reconciliation only. JSON and restored snapshots carry no proof.
func (r InventoryResource) WithListedIdentity(i incusapi.ListedInstanceIdentity) InventoryResource {
	r.listed = i.Observation()
	return r
}

func physicalScope(controller, daemon, project, uuid, generation string) string {
	return evidenceDigest([]byte(strings.Join([]string{controller, daemon, project, uuid, generation}, "\x00")))
}

func recordListedIncarnation(tx *sql.Tx, r InventoryResource, existed bool, fence Fence, began, now time.Time, maxAge time.Duration) error {
	listed := r.listed
	id := listed.Subject
	if listed.FenceDigest != sha256.Sum256([]byte(fence.Nonce)) || !listed.StartedAt.After(began) || listed.CompletedAt.Before(listed.StartedAt) || listed.CompletedAt.After(now) || now.Sub(listed.StartedAt) > maxAge ||
		!routeUUID(id.UUID) || !routeUUID(id.Generation) || !bindingName(id.ControllerID) || id.PoolID != r.Pool ||
		id.CPUMillis > r.Resources.CPU || id.RAMMiB > r.Resources.RAM || id.CreatedAt.IsZero() ||
		(r.ReservationKey != "" && r.ReservationKey != id.ReservationKey) {
		return ErrReconciliation
	}
	// The gateway uses the observed name. A reconciler that captured its
	// own original route-scoped ID may link a digest only by exact reservation.
	if id.Name != r.ID && !(bindingFingerprint(r.ID) && r.ReservationKey != "" && r.ReservationKey == id.ReservationKey) {
		return ErrReconciliation
	}
	var bound bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM host_bindings WHERE host=? AND server_key=? AND scope=? AND origin=?
 AND server_name=? AND server_version=?)`, r.Host, id.BackendKeyFingerprint, id.ScopeFingerprint, id.OriginFingerprint, id.ServerName, id.ServerVersion).Scan(&bound); err != nil {
		return err
	}
	if !bound || id.ServerVersion != "6.0.4" {
		return ErrReconciliation
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	digest := evidenceDigest(raw)
	// An independently identified row can disambiguate another allocation.
	// To retire this row itself, also require the original request-name binding
	// and the actual CPU/RAM bounds. Disk disposition is a separate proof group.
	scope := ""
	nameKey := evidenceDigest([]byte(strings.Join([]string{id.ControllerID, id.PoolID, id.Name}, "\x00")))
	if r.ReservationKey == nameKey && id.ReservationKey == nameKey && id.CPUMillis == r.Resources.CPU && id.RAMMiB == r.Resources.RAM {
		scope = physicalScope(id.ControllerID, id.BackendKeyFingerprint, id.ScopeFingerprint, id.UUID, id.Generation)
	}
	pair := id.UUID + "/" + id.Generation
	birth := id.CreatedAt.UTC().Format(time.RFC3339Nano)
	// A late observation cannot establish which incarnation an originally opaque
	// charge covered. A name or tag reused for a different subject stays opaque
	// permanently, even if the former subject is seen again.
	if existed {
		var original, originalPair, originalBirth string
		if err := tx.QueryRow("SELECT slot_subject,physical_pair,physical_created_at FROM inventory WHERE host=? AND resource_id=?", r.Host, r.ID).Scan(&original, &originalPair, &originalBirth); err != nil {
			return err
		}
		if original != digest {
			_, err := tx.Exec(`UPDATE inventory SET slot_subject='',identity_epoch=0,last_seen_at='',
 physical_pair=CASE WHEN physical_pair='' THEN ? ELSE physical_pair END,
 physical_created_at=CASE WHEN physical_created_at='' THEN ? ELSE physical_created_at END,
 incarnation_conflict=MAX(incarnation_conflict,?) WHERE host=? AND resource_id=?`, pair, birth, originalPair != "" && (originalPair != pair || originalBirth != birth), r.Host, r.ID)
			return err
		}
	}
	_, err = tx.Exec(`UPDATE inventory SET slot_subject=?,physical_scope=?,physical_pair=?,physical_created_at=?,identity_epoch=?,last_seen_at=? WHERE host=? AND resource_id=?`,
		digest, scope, pair, birth, fence.Epoch, listed.CompletedAt.UTC().Format(time.RFC3339Nano), r.Host, r.ID)
	return err
}

const slotConflictQuery = `SELECT
 EXISTS(SELECT 1 FROM inventory WHERE identity_epoch < ?) OR
 EXISTS(SELECT 1 FROM inventory WHERE identity_epoch > ?) OR
 EXISTS(SELECT 1 FROM inventory WHERE physical_pair=? AND (host<>? OR resource_id<>?)) OR
 EXISTS(SELECT 1 FROM inventory WHERE resource_id=? AND host<>?),
 EXISTS(SELECT 1 FROM releases WHERE physical_pair=''),
 EXISTS(SELECT 1 FROM releases WHERE physical_pair=?) OR EXISTS(SELECT 1 FROM releases WHERE scoped_resource_id=?)`

func checkSlotIncarnation(ctx context.Context, tx *sql.Tx, hold Request, e SlotReuseEvidence, epoch int64) error {
	id := e.Identity
	var row InventoryResource
	var scope, pair, seenAt string
	var seenEpoch int64
	err := tx.QueryRowContext(ctx, `SELECT i.resource_id,i.host,i.pool,i.reservation_key,i.cpu,i.ram,i.disk,i.runners,i.state,
 i.physical_scope,i.physical_pair,i.identity_epoch,i.last_seen_at FROM inventory i
 WHERE i.reservation_key=?`, hold.Key).Scan(&row.ID, &row.Host, &row.Pool, &row.ReservationKey, &row.Resources.CPU, &row.Resources.RAM, &row.Resources.Disk,
		&row.Resources.Runners, &row.State, &scope, &pair, &seenEpoch, &seenAt)
	if err != nil {
		return err
	}
	want := InventoryResource{ID: id.ResourceID, Host: hold.Host, Pool: hold.Pool, ReservationKey: hold.Key, Resources: hold.Resources, State: "deleting"}
	if row != want || seenEpoch != epoch || scope != physicalScope(id.Controller, id.Daemon, id.Project, id.Instance, id.Generation) || pair != id.Instance+"/"+id.Generation {
		return ErrConflict
	}
	lastSeen, err := time.Parse(time.RFC3339Nano, seenAt)
	if err != nil || !e.Teardown.ObservedAt.After(lastSeen) {
		return ErrConflict
	}
	var ambiguous, legacy, retired bool
	err = tx.QueryRowContext(ctx, slotConflictQuery,
		epoch, epoch, pair, row.Host, row.ID, row.ID, row.Host, pair, row.ID).Scan(&ambiguous, &legacy, &retired)
	if err != nil {
		return err
	}
	if legacy {
		return ErrSlotReuseEvidence
	}
	if ambiguous || retired {
		return ErrConflict
	}
	return nil
}

func checkDeletingSlot(tx *sql.Tx, hold Request) error {
	var current Request
	var state string
	err := tx.QueryRow(`SELECT request_key,pool,host,cpu,ram,disk,runners,state FROM reservations WHERE request_key=?`, hold.Key).
		Scan(&current.Key, &current.Pool, &current.Host, &current.Resources.CPU, &current.Resources.RAM, &current.Resources.Disk, &current.Resources.Runners, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSlotReuseEvidence
	}
	if err != nil {
		return err
	}
	if current != hold {
		return ErrConflict
	}
	if state != "deleting" {
		return ErrDenied
	}
	return nil
}
