package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStockAndBootstrapStateSurviveArchiveRestoreTogether(t *testing.T) {
	ctx := context.Background()
	s := placementFixture(t)
	if err := s.InitializeBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	for _, v := range []struct {
		id, request string
		installed   bool
	}{{"pending-host", "11111111-1111-4111-8111-111111111111", false}, {"installed-host", "22222222-2222-4222-8222-222222222222", true}} {
		if _, run, err := s.BeginBootstrap(ctx, v.id, digest, v.request, false); err != nil || !run {
			t.Fatal(run, err)
		}
		if v.installed {
			if err := s.CompleteBootstrap(ctx, v.id, digest, v.request); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := s.BootstrapOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issueStockTestGrant(t, s, "unused")
	p, b := stockPlan(t, s, "stock")
	grantState, err := s.ReadBrokerGrantState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), func(context.Context, DispatchPermit) (DispatchReceipt, error) {
		return DispatchReceipt{}, errors.New("lost response")
	}); !errors.Is(err, ErrDispatchUnknown) {
		t.Fatal(err)
	}
	original, err := s.LookupDispatchReadOnly(ctx, p.Request.Key)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(sqliteBackupDir(t), "archive.db")
	if err := s.BackupSQLite(ctx, archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(sqliteBackupDir(t), "restored.db")
	if err := RestoreSQLite(ctx, archive, restored); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{archive, restored} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			copy, err := OpenSQLite(path, Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer copy.Close()
			if err := copy.InitializeBootstrap(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := copy.BootstrapOperations(ctx)
			if err != nil || !reflect.DeepEqual(got, before) {
				t.Fatalf("bootstrap state changed: %+v %v", got, err)
			}
			dispatch, err := copy.LookupDispatchReadOnly(ctx, p.Request.Key)
			if err != nil || !reflect.DeepEqual(dispatch, original) {
				t.Fatalf("stock dispatch changed: %+v %v", dispatch, err)
			}
			restoredGrants, err := copy.ReadBrokerGrantState(ctx)
			if err != nil || !reflect.DeepEqual(restoredGrants.Grants, grantState.Grants) {
				t.Fatal("grant state changed", err)
			}
			snap := sqliteSnapshot(t, copy)
			if snap.Ready || snap.Allocated.Global.Runners != 2 {
				t.Fatal("backup activated or lost stock charge", snap.Ready, snap.Allocated.Global)
			}
			if _, run, err := copy.BeginBootstrap(ctx, "pending-host", digest, "11111111-1111-4111-8111-111111111111", false); err != nil || run {
				t.Fatal("unknown bootstrap automatically retried", run, err)
			}
			if _, err := copy.ExecuteStockProviderDispatch(ctx, p, b, stockAuthority(), stockDigest(), stockSuccess); !errors.Is(err, ErrDispatchClaimed) {
				t.Fatal("stock unknown was redispatched", err)
			}
			var version int
			if err := copy.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != sqliteCurrentVersion {
				t.Fatal("wrong schema", version, err)
			}
		})
	}
}

func TestStockMigrationPreservesExistingBootstrapOperations(t *testing.T) {
	ctx := context.Background()
	s := placementFixture(t)
	if err := s.InitializeBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, run, err := s.BeginBootstrap(ctx, "existing-host", strings.Repeat("a", 64), "11111111-1111-4111-8111-111111111111", false); err != nil || !run {
		t.Fatal(run, err)
	}
	before, err := s.BootstrapOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(stockMigrationDowngradeSQL + "PRAGMA user_version=15"); err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenSQLite(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	after, err := migrated.BootstrapOperations(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration lost bootstrap operation %+v %v", after, err)
	}
	if sqliteSnapshot(t, migrated).Ready {
		t.Fatal("migration reused old readiness")
	}
}
