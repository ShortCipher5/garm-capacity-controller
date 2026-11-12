//go:build linux && cgo && !agent

package instance

// Executes the actual CreateInternal and native test cluster, stopping at the
// existing driver injection boundary. No daemon, driver, LXC or storage runs.
import (
	"context"
	"errors"
	"testing"

	"github.com/lxc/incus/v6/internal/server/db"
	"github.com/lxc/incus/v6/internal/server/db/cluster"
	deviceConfig "github.com/lxc/incus/v6/internal/server/device/config"
	"github.com/lxc/incus/v6/internal/server/instance/instancetype"
	"github.com/lxc/incus/v6/internal/server/instance/operationlock"
	"github.com/lxc/incus/v6/internal/server/operations"
	"github.com/lxc/incus/v6/internal/server/state"
	"github.com/lxc/incus/v6/internal/server/sys"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/osarch"
	"github.com/lxc/incus/v6/shared/revert"
)

func TestQualificationActualCreateInternalDriverFailure(t *testing.T) {
	// Deliberately serial: Create is the upstream package-global injection seam.
	clusterDB, cleanup := db.NewTestCluster(t)
	defer cleanup()
	ctx := context.Background()
	var nodeName string
	if err := clusterDB.Transaction(ctx, func(ctx context.Context, tx *db.ClusterTx) error {
		return tx.Tx().QueryRowContext(ctx, `SELECT name FROM nodes ORDER BY id LIMIT 1`).Scan(&nodeName)
	}); err != nil {
		t.Fatal(err)
	}
	s := &state.State{DB: &db.DB{Cluster: clusterDB}, OS: &sys.OS{Architectures: []int{osarch.ARCH_64BIT_INTEL_X86}}, ServerName: nodeName, InstanceTypes: map[instancetype.Type]error{instancetype.Container: nil}}
	sentinel := errors.New("qualification fake driver stop")
	original := Create
	defer func() { Create = original }()
	calls := 0
	Create = func(_ *state.State, args db.InstanceArgs, _ api.Project, _ *operations.Operation) (Instance, revert.Hook, error) {
		calls++
		// Separate transaction proves the real caller committed before driver entry.
		if err := clusterDB.Transaction(ctx, func(ctx context.Context, tx *db.ClusterTx) error {
			row, err := cluster.GetInstance(ctx, tx.Tx(), "default", "qualification-empty")
			if err != nil {
				return err
			}
			if row.ID != args.ID || row.ID == 0 {
				return errors.New("driver received wrong row")
			}
			var n int
			if err = tx.Tx().QueryRowContext(ctx, `SELECT count(*) FROM instances_devices WHERE instance_id=?`, row.ID).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return errors.New("device transaction missing")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// Real native create lock is still owned at driver boundary.
		lock, err := operationlock.Create("default", args.Name, nil, operationlock.ActionCreate, false, false)
		if err == nil {
			lock.Done(nil)
			t.Fatal("create lock not held")
		}
		return nil, nil, sentinel
	}
	args := db.InstanceArgs{Project: "default", Name: "qualification-empty", Type: instancetype.Container, Architecture: osarch.ARCH_64BIT_INTEL_X86, Profiles: []api.Profile{}, Devices: deviceConfig.Devices{"root": deviceConfig.Device{"type": "disk", "path": "/", "pool": "not-a-real-pool"}}}
	inst, lock, revertHook, err := CreateInternal(s, args, nil, false, true)
	if !errors.Is(err, sentinel) || calls != 1 || inst != nil || lock != nil || revertHook != nil {
		t.Fatalf("unexpected result: calls=%d err=%v", calls, err)
	}
	// The real deferred row cleanup runs, but this says nothing about physical
	// rollback: no native driver was invoked and no storage exists in the fixture.
	if err = clusterDB.Transaction(ctx, func(ctx context.Context, tx *db.ClusterTx) error {
		var n int
		err := tx.Tx().QueryRowContext(ctx, `SELECT count(*) FROM instances WHERE name='qualification-empty'`).Scan(&n)
		if err != nil {
			return err
		}
		if n != 0 {
			return errors.New("row cleanup did not run")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recovered, err := operationlock.Create("default", args.Name, nil, operationlock.ActionCreate, false, false)
	if err != nil {
		t.Fatal("create lock leaked", err)
	}
	recovered.Done(nil)
	args.Name = "invalid/name/extra"
	if _, _, _, err = CreateInternal(s, args, nil, false, true); err == nil {
		t.Fatal("invalid name accepted")
	}
	if calls != 1 {
		t.Fatal("invalid input reached driver")
	}
}
