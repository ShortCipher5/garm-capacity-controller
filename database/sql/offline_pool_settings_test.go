// Copyright 2025 Cloudbase Solutions SRL
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package sql

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	"github.com/cloudbase/garm/config"
	dbCommon "github.com/cloudbase/garm/database/common"
	"github.com/cloudbase/garm/database/watcher"
	garmTesting "github.com/cloudbase/garm/internal/testing"
	"github.com/cloudbase/garm/params"
)

type OfflinePoolSettingsTestSuite struct {
	suite.Suite
	Store dbCommon.Store
	ctx   context.Context
	dbCfg config.Database

	adminCtx   context.Context
	org        params.Organization
	otherOrg   params.Organization
	repo       params.Repository
	enterprise params.Enterprise
	pool       params.Pool
}

func (s *OfflinePoolSettingsTestSuite) TearDownTest() {
	watcher.CloseWatcher()
}

func (s *OfflinePoolSettingsTestSuite) SetupTest() {
	ctx := context.Background()
	watcher.InitWatcher(ctx)

	s.dbCfg = garmTesting.GetTestSqliteDBConfig(s.T())
	db, err := NewSQLDatabase(context.Background(), s.dbCfg)
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to create db connection: %s", err))
	}
	s.Store = db
	s.adminCtx = garmTesting.ImpersonateAdminContext(ctx, s.Store, s.T())

	githubEndpoint := garmTesting.CreateDefaultGithubEndpoint(s.adminCtx, db, s.T())
	creds := garmTesting.CreateTestGithubCredentials(s.adminCtx, "new-creds", db, s.T(), githubEndpoint)

	org, err := db.CreateOrganization(s.adminCtx, "test-org", creds, "test-webhook-secret", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to create org: %s", err))
	}
	otherOrg, err := db.CreateOrganization(s.adminCtx, "test-org-2", creds, "test-webhook-secret-2", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to create second org: %s", err))
	}
	repo, err := db.CreateRepository(s.adminCtx, "test-owner", "test-repo", creds, "test-webhook-secret-3", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to create repo: %s", err))
	}
	enterprise, err := db.CreateEnterprise(s.adminCtx, "test-enterprise", creds, "test-webhook-secret-4", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to create enterprise: %s", err))
	}

	orgEntity, err := org.GetEntity()
	if err != nil {
		s.FailNow(fmt.Sprintf("failed to get org entity: %s", err))
	}
	pool, err := db.CreateEntityPool(
		s.adminCtx,
		orgEntity,
		params.CreatePoolParams{
			ProviderName:   "test-provider",
			MaxRunners:     5,
			MinIdleRunners: 2,
			Image:          "test-image",
			Flavor:         "test-flavor",
			OSType:         "linux",
			Tags:           []string{"amd64-linux-runner"},
		},
	)
	if err != nil {
		s.FailNow(fmt.Sprintf("cannot create org pool: %v", err))
	}

	s.org = org
	s.otherOrg = otherOrg
	s.repo = repo
	s.enterprise = enterprise
	s.pool = pool
}

// newSecondHandle opens a second, independent database handle over the
// same database file the suite store uses. A full NewSQLDatabase is not
// possible here because producer IDs must be unique within the watcher;
// the experimental methods never notify anyway.
func (s *OfflinePoolSettingsTestSuite) newSecondHandle() *sqlDatabase {
	dbCfg := s.dbCfg
	conn, err := newDBConn(dbCfg)
	s.Require().NoError(err)
	sqlDB, err := conn.DB()
	s.Require().NoError(err)
	s.T().Cleanup(func() { sqlDB.Close() })
	return &sqlDatabase{conn: conn, sqlDB: sqlDB, ctx: context.Background(), cfg: dbCfg}
}

func (s *OfflinePoolSettingsTestSuite) entityOf(holder interface {
	GetEntity() (params.ForgeEntity, error)
}) params.ForgeEntity {
	entity, err := holder.GetEntity()
	s.Require().NoError(err)
	return entity
}

func (s *OfflinePoolSettingsTestSuite) orgEntity() params.ForgeEntity {
	return s.entityOf(s.org)
}

func (s *OfflinePoolSettingsTestSuite) currentPoolRow(poolID string) params.Pool {
	pool, err := s.Store.GetPoolByID(s.adminCtx, poolID)
	s.Require().NoError(err)
	return pool
}

func (s *OfflinePoolSettingsTestSuite) revisionExists() bool {
	sqlDB := s.Store.(*sqlDatabase)
	return sqlDB.conn.Migrator().HasTable(&OfflinePoolSettingsRevision{})
}

func (s *OfflinePoolSettingsTestSuite) receiptsExist() bool {
	sqlDB := s.Store.(*sqlDatabase)
	return sqlDB.conn.Migrator().HasTable(&OfflinePoolSettingsRequestReceipt{})
}

func (s *OfflinePoolSettingsTestSuite) metaExists() bool {
	sqlDB := s.Store.(*sqlDatabase)
	return sqlDB.conn.Migrator().HasTable(&OfflinePoolSettingsMeta{})
}

// installedMetaRow returns the installation marker row; it requires the
// experimental schema to be installed.
func (s *OfflinePoolSettingsTestSuite) installedMetaRow() OfflinePoolSettingsMeta {
	sqlDB := s.Store.(*sqlDatabase)
	var meta OfflinePoolSettingsMeta
	s.Require().NoError(sqlDB.conn.Where("key = ?", offlineSettingsMetaSchemaKey).First(&meta).Error)
	return meta
}

func (s *OfflinePoolSettingsTestSuite) receiptCount() int64 {
	sqlDB := s.Store.(*sqlDatabase)
	var count int64
	s.Require().NoError(sqlDB.conn.Model(&OfflinePoolSettingsRequestReceipt{}).Count(&count).Error)
	return count
}

func (s *OfflinePoolSettingsTestSuite) getReceipt(requestID string) OfflinePoolSettingsRequestReceipt {
	sqlDB := s.Store.(*sqlDatabase)
	var receipt OfflinePoolSettingsRequestReceipt
	s.Require().NoError(sqlDB.conn.Where("request_id = ?", requestID).First(&receipt).Error)
	return receipt
}

func offlineHash(payload string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

// offlineUpdateReq builds a store request for a well-formed patch without
// an idempotency key.
func offlineUpdateReq(rev params.OfflinePoolSettingsRevision, patch params.OfflinePoolSettingsPatch) params.OfflinePoolSettingsUpdateRequest {
	return params.OfflinePoolSettingsUpdateRequest{
		PayloadHash: offlineHash("offline|" + rev.String() + "|" + fmt.Sprintf("%+v", patch)),
		IfMatch:     rev,
		Patch:       &patch,
	}
}

func (s *OfflinePoolSettingsTestSuite) currentRevision(poolID string) params.OfflinePoolSettingsRevision {
	sqlDB := s.Store.(*sqlDatabase)
	var rev OfflinePoolSettingsRevision
	s.Require().NoError(sqlDB.conn.Where("pool_id = ?", poolID).First(&rev).Error)
	return params.OfflinePoolSettingsRevision{Epoch: rev.Epoch, Seq: rev.Seq}
}

// TestNoSchemaSideEffectsWithoutUse verifies that simply opening (and
// migrating) a database does not create the experimental schema.
func (s *OfflinePoolSettingsTestSuite) TestNoSchemaSideEffectsWithoutUse() {
	s.Require().False(s.revisionExists())
	s.Require().False(s.receiptsExist())

	sqlDB := s.Store.(*sqlDatabase)
	var count int64
	s.Require().NoError(sqlDB.conn.Raw(
		"SELECT count(1) FROM sqlite_master WHERE type='table' AND name IN ('offline_pool_settings_revisions', 'offline_pool_settings_request_receipts')").
		Scan(&count).Error)
	s.Require().Zero(count)
}

func (s *OfflinePoolSettingsTestSuite) TestGetCreatesRevisionLazily() {
	settings, rev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(uint(0), settings.Priority)
	s.Require().Equal(uint(5), settings.MaxRunners)
	s.Require().Equal(uint(2), settings.MinIdleRunners)
	s.Require().Zero(rev.Seq)
	s.Require().NotZero(rev.Epoch)
	s.Require().Equal(fmt.Sprintf("%q", rev.String()), rev.ETag())

	// revision row now exists
	s.Require().True(s.revisionExists())

	// a second GET returns the same revision
	_, rev2, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(rev, rev2)
}

func (s *OfflinePoolSettingsTestSuite) TestGetNonexistentPool() {
	_, _, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), "00000000-0000-0000-0000-000000000000")
	s.Require().ErrorIs(err, runnerErrors.ErrNotFound)
}

func (s *OfflinePoolSettingsTestSuite) TestGetPoolNotMemberOfEntity() {
	// same entity type, wrong entity
	_, _, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.entityOf(s.otherOrg), s.pool.ID)
	s.Require().ErrorIs(err, dbCommon.ErrPoolNotMemberOfEntity)

	// different entity types entirely
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, s.entityOf(s.repo), s.pool.ID)
	s.Require().ErrorIs(err, dbCommon.ErrPoolNotMemberOfEntity)

	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, s.entityOf(s.enterprise), s.pool.ID)
	s.Require().ErrorIs(err, dbCommon.ErrPoolNotMemberOfEntity)

	// no revision row may have been created for any of these
	s.Require().True(s.revisionExists())
	sqlDB := s.Store.(*sqlDatabase)
	var count int64
	s.Require().NoError(sqlDB.conn.Model(&OfflinePoolSettingsRevision{}).Count(&count).Error)
	s.Require().Zero(count)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateHappyPath() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	settings, rev1 := result.Settings, result.Revision
	s.Require().Equal(uint(7), settings.Priority)
	s.Require().Equal(uint(5), settings.MaxRunners)
	s.Require().Equal(uint(2), settings.MinIdleRunners)
	s.Require().Equal(rev0.Epoch, rev1.Epoch)
	s.Require().Equal(rev0.Seq+1, rev1.Seq)

	// the stock pools row reflects the change
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)

	// another update advances the revision again
	minIdle := uint(4)
	result, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev1, params.OfflinePoolSettingsPatch{
		MinIdleRunners: &minIdle,
	}))
	s.Require().NoError(err)
	s.Require().Equal(rev1.Seq+1, result.Revision.Seq)
	s.Require().Equal(uint(4), s.currentPoolRow(s.pool.ID).MinIdleRunners)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateStaleRevision() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(3)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)

	// patching with the now stale revision fails and reports the current one
	priority = 9
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().Error(err)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
	var staleErr *dbCommon.OfflinePoolSettingsStaleRevisionError
	s.Require().ErrorAs(err, &staleErr)
	s.Require().Equal(rev0.Seq+1, staleErr.Current.Seq)
	s.Require().Equal(params.OfflinePoolSettings{}, result.Settings)

	// row is byte-for-byte unchanged: priority stuck at 3, seq advanced once
	s.Require().Equal(uint(3), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(rev0.Seq+1, s.currentRevision(s.pool.ID).Seq)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateMinIdleGreaterThanMax() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// min_idle_runners exceeding the current max_runners
	minIdle := uint(6)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		MinIdleRunners: &minIdle,
	}))
	s.Require().ErrorIs(err, runnerErrors.ErrUnprocessable)

	// lowering max_runners below the current min_idle_runners also trips the
	// invariant, even though min_idle_runners is not supplied
	maxRunners := uint(1)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		MaxRunners: &maxRunners,
	}))
	s.Require().ErrorIs(err, runnerErrors.ErrUnprocessable)

	// nothing changed
	s.Require().Equal(uint(5), s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(uint(2), s.currentPoolRow(s.pool.ID).MinIdleRunners)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateNoopDoesNotAdvanceRevision() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(0)
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	s.Require().Equal(rev0, result.Revision)
	s.Require().Equal(uint(0), result.Settings.Priority)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateEmptyPatchRejected() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// a request without a decoded patch (the handler never produces one,
	// this is the store's defensive backstop) is rejected as a bad request
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, params.OfflinePoolSettingsUpdateRequest{
		PayloadHash: offlineHash("empty"),
		IfMatch:     rev0,
	})
	s.Require().ErrorIs(err, runnerErrors.ErrBadRequest)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdatePoolNotMemberOfEntity() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(1)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.entityOf(s.otherOrg), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().ErrorIs(err, dbCommon.ErrPoolNotMemberOfEntity)
	s.Require().Equal(uint(0), s.currentPoolRow(s.pool.ID).Priority)
}

// TestConcurrentUpdatesFromTwoHandles hammers one pool from two separate
// database handles with the same base revision. Exactly one update per
// round must commit; the loser must observe a stale revision and the row
// must reflect exactly the committed writes.
func (s *OfflinePoolSettingsTestSuite) TestConcurrentUpdatesFromTwoHandles() {
	entity := s.orgEntity()

	secondHandle := s.newSecondHandle()

	rounds := 10
	for round := 0; round < rounds; round++ {
		_, curRev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
		s.Require().NoError(err)
		type result struct {
			priority uint
			err      error
		}
		results := make(chan result, 2)
		var wg sync.WaitGroup
		for _, val := range []uint{uint(round*10 + 1), uint(round*10 + 2)} {
			wg.Add(1)
			go func(val uint) {
				defer wg.Done()
				priority := val
				_, err := secondHandle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, offlineUpdateReq(curRev, params.OfflinePoolSettingsPatch{
					Priority: &priority,
				}))
				results <- result{priority: val, err: err}
			}(val)
		}
		wg.Wait()
		close(results)

		var winner *uint
		losers := 0
		for res := range results {
			if res.err == nil {
				s.Require().Nil(winner)
				w := res.priority
				winner = &w
				continue
			}
			s.Require().ErrorIs(res.err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
			losers++
		}
		s.Require().NotNil(winner)
		s.Require().Equal(1, losers)

		// exactly one write landed, the revision advanced exactly once
		s.Require().Equal(*winner, s.currentPoolRow(s.pool.ID).Priority)
		s.Require().Equal(curRev.Seq+1, s.currentRevision(s.pool.ID).Seq)
	}
}

// TestFailedCommitInvariance forces the update transaction to fail before
// it can commit (another connection holds the SQLite write lock) and
// verifies that neither the settings nor the revision changed.
func (s *OfflinePoolSettingsTestSuite) TestFailedCommitInvariance() {
	entity := s.orgEntity()

	// short busy timeout so the blocked BEGIN IMMEDIATE fails fast
	dbCfg := s.dbCfg
	dbCfg.SQLite.BusyTimeoutSeconds = 1
	conn, err := newDBConn(dbCfg)
	s.Require().NoError(err)
	sqlDB, err := conn.DB()
	s.Require().NoError(err)
	s.T().Cleanup(func() { sqlDB.Close() })
	handle := &sqlDatabase{conn: conn, sqlDB: sqlDB, ctx: context.Background(), cfg: dbCfg}

	_, rev0, err := handle.GetOfflinePoolSettings(context.Background(), entity, s.pool.ID)
	s.Require().NoError(err)

	beforePool := s.currentPoolRow(s.pool.ID)

	// grab the write lock on a dedicated connection
	dbConn, err := sqlDB.Conn(context.Background())
	s.Require().NoError(err)
	defer dbConn.Close()
	_, err = dbConn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	s.Require().NoError(err)

	priority := uint(42)
	result, err := handle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().Error(err)
	s.Require().Equal(params.OfflinePoolSettingsUpdateResult{}, result)

	// byte-for-byte invariance
	s.Require().Equal(beforePool.Priority, s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(beforePool.MaxRunners, s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(beforePool.MinIdleRunners, s.currentPoolRow(s.pool.ID).MinIdleRunners)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))

	_, err = dbConn.ExecContext(context.Background(), "ROLLBACK")
	s.Require().NoError(err)

	// once the lock is released, the same update succeeds
	result, err = handle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	s.Require().Equal(rev0.Seq+1, result.Revision.Seq)
	s.Require().Equal(uint(42), s.currentPoolRow(s.pool.ID).Priority)
}

// TestLegacyWriterBumpsRevision verifies that the stock pool update path
// participates in the revision scheme: a stock write advances the
// experimental revision of the pool atomically, so a client holding an old
// ETag observes the legacy write as a conflict.
func (s *OfflinePoolSettingsTestSuite) TestLegacyWriterBumpsRevision() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)

	maxRunners := uint(10)
	minIdle := uint(1)
	_, err = s.Store.UpdateEntityPool(s.adminCtx, entity, s.pool.ID, params.UpdatePoolParams{
		MaxRunners:     &maxRunners,
		MinIdleRunners: &minIdle,
	})
	s.Require().NoError(err)

	// content changed...
	s.Require().Equal(uint(10), s.currentPoolRow(s.pool.ID).MaxRunners)
	// ... and the offline revision moved with it
	s.Require().Equal(rev0.Seq+1, s.currentRevision(s.pool.ID).Seq)

	// a client holding the old ETag loses the race now
	priority := uint(2)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
	s.Require().Equal(uint(10), s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(uint(0), s.currentPoolRow(s.pool.ID).Priority)

	// a fresh ETag can still patch
	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev1, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	s.Require().Equal(rev1.Seq+1, result.Revision.Seq)
	s.Require().Equal(uint(10), s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(uint(2), s.currentPoolRow(s.pool.ID).Priority)
}

// TestLegacyWriteWithoutRevisionTable verifies that a stock write on a
// database where the experimental path never ran (no side tables) is
// unchanged: no error, no table creation, revision starts at zero once the
// experimental API is used for the first time.
func (s *OfflinePoolSettingsTestSuite) TestLegacyWriteWithoutRevisionTable() {
	entity := s.orgEntity()
	s.Require().False(s.revisionExists())

	priority := uint(11)
	_, err := s.Store.UpdateEntityPool(s.adminCtx, entity, s.pool.ID, params.UpdatePoolParams{
		Priority: &priority,
	})
	s.Require().NoError(err)
	s.Require().Equal(uint(11), s.currentPoolRow(s.pool.ID).Priority)

	// still no experimental schema
	s.Require().False(s.revisionExists())
	s.Require().False(s.receiptsExist())

	// the first experimental read sees the legacy change at seq 0
	settings, rev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(uint(11), settings.Priority)
	s.Require().Zero(rev.Seq)
}

// TestLegacyWriteAfterRevisionTableLossFailsClosed drives the experimental
// path once (installing the schema), then simulates a schema loss by
// dropping the revision table while the installation marker stays behind.
// A stock pool write must now fail closed — the whole transaction rolls
// back — instead of committing a change no conditional client can detect.
func (s *OfflinePoolSettingsTestSuite) TestLegacyWriteAfterRevisionTableLossFailsClosed() {
	entity := s.orgEntity()

	_, _, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().True(s.revisionExists())
	s.Require().True(s.receiptsExist())
	s.Require().True(s.metaExists())
	s.Require().True(s.installedMetaRow().Installed)

	beforePool := s.currentPoolRow(s.pool.ID)

	// lose the revision table; the marker (and the receipts table) survive
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_revisions").Error)
	s.Require().False(s.revisionExists())
	s.Require().True(s.metaExists())

	priority := uint(21)
	_, err = s.Store.UpdateEntityPool(s.adminCtx, entity, s.pool.ID, params.UpdatePoolParams{
		Priority: &priority,
	})
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineRevisionSchemaMissing)
	s.Require().Contains(err.Error(), "experimental revision schema is missing after being installed")
	s.Require().Contains(err.Error(), "refusing the legacy write")

	// the stock write rolled back: the pool row is unchanged
	s.Require().Equal(beforePool.Priority, s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(beforePool.MaxRunners, s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(beforePool.MinIdleRunners, s.currentPoolRow(s.pool.ID).MinIdleRunners)
	s.Require().Equal(uint(0), s.currentPoolRow(s.pool.ID).Priority)

	// the stock path never recreates the experimental schema, and the
	// surviving marker/receipts tables are untouched
	s.Require().False(s.revisionExists())
	s.Require().True(s.metaExists())
	s.Require().True(s.receiptsExist())
	s.Require().True(s.installedMetaRow().Installed)
}

// TestExperimentalPathsFailHardAfterRevisionTableLoss drives the
// experimental path once (installing the schema), then simulates a schema
// loss by dropping the revision table while the installation marker stays
// behind. GET and PATCH must both fail hard — no silent recreation, which
// would restart revision history at a fresh epoch while clients still
// hold ETags from the lost one. Recovery is the explicit reinstall.
func (s *OfflinePoolSettingsTestSuite) TestExperimentalPathsFailHardAfterRevisionTableLoss() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().True(s.revisionExists())
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)

	// lose the revision table; the marker (and the receipts table) survive
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_revisions").Error)
	s.Require().False(s.revisionExists())
	s.Require().True(s.metaExists())

	// GET fails hard and does not recreate the table
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().False(s.revisionExists())

	// PATCH fails hard the same way, before any write or receipt
	priority := uint(7)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().False(s.revisionExists())

	// nothing changed, no receipt was persisted
	s.Require().Equal(uint(0), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(int64(0), s.receiptCount())
	s.Require().True(s.installedMetaRow().Installed)
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)
}

// TestExperimentalPathsFailHardAfterReceiptsTableLoss drops only the
// receipts table after the feature was used. GET and PATCH must fail hard:
// recreating the receipts table would erase the replay record of requests
// whose responses may never have reached their clients.
func (s *OfflinePoolSettingsTestSuite) TestExperimentalPathsFailHardAfterReceiptsTableLoss() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().True(s.receiptsExist())

	// lose the receipts table; the marker (and the revision table) survive
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_request_receipts").Error)
	s.Require().False(s.receiptsExist())
	s.Require().True(s.metaExists())
	s.Require().True(s.revisionExists())

	// GET fails hard even though it never writes receipts: the loss proves
	// the installation is damaged and silent repair is exactly what must
	// not happen
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().False(s.receiptsExist())

	// PATCH fails hard before applying or receipting anything
	priority := uint(7)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().False(s.receiptsExist())
	s.Require().Equal(uint(0), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

// TestSchemaAuditFreshInstallWhenNothingSurvives covers the benign audit
// quadrant: no experimental table exists at all (true pre-feature state),
// so first use installs the schema at generation 1.
func (s *OfflinePoolSettingsTestSuite) TestSchemaAuditFreshInstallWhenNothingSurvives() {
	s.Require().False(s.revisionExists())
	s.Require().False(s.receiptsExist())
	s.Require().False(s.metaExists())

	_, rev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	s.Require().Zero(rev.Seq)
	s.Require().NotZero(rev.Epoch)

	meta := s.installedMetaRow()
	s.Require().True(meta.Installed)
	s.Require().Equal(uint64(1), meta.Generation)
	s.Require().Equal(rev.Epoch, meta.LastEpoch)
}

// TestSchemaAuditFailsHardWhenMarkerLostButRevisionTableSurvives covers the
// witness-lost quadrant: the marker table is gone but the revision table
// survives with rows. First use must fail hard — silently reinstalling
// would restart generation at 1 and last_epoch at 0 over rows that still
// serve ETags — and must leave the surviving rows untouched.
func (s *OfflinePoolSettingsTestSuite) TestSchemaAuditFailsHardWhenMarkerLostButRevisionTableSurvives() {
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.AutoMigrate(&OfflinePoolSettingsRevision{}))

	// a surviving revision row, as left behind by a lost marker
	survivorID := uuid.New()
	s.Require().NoError(sqlDB.conn.Create(&OfflinePoolSettingsRevision{PoolID: survivorID, Epoch: 100}).Error)

	_, _, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().Contains(err.Error(), "installation marker is missing")
	s.Require().Contains(err.Error(), "refusing to install over surviving rows")

	priority := uint(7)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(
		params.OfflinePoolSettingsRevision{Epoch: 1}, params.OfflinePoolSettingsPatch{Priority: &priority}))
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)

	// nothing was installed or recreated, the surviving row is untouched
	s.Require().False(s.metaExists())
	s.Require().False(s.receiptsExist())
	var count int64
	s.Require().NoError(sqlDB.conn.Model(&OfflinePoolSettingsRevision{}).Count(&count).Error)
	s.Require().Equal(int64(1), count)
	var survivor OfflinePoolSettingsRevision
	s.Require().NoError(sqlDB.conn.Where("pool_id = ?", survivorID).First(&survivor).Error)
	s.Require().Equal(uint64(100), survivor.Epoch)

	// the explicit reinstall is the recovery: it recreates the marker and
	// the missing receipts table at generation 1 and leaves the surviving
	// revision row authoritative for its pool
	s.Require().NoError(sqlDB.ReinstallOfflinePoolSettingsSchema())
	s.Require().True(s.metaExists())
	s.Require().True(s.receiptsExist())
	s.Require().True(s.revisionExists())
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)
	s.Require().NoError(sqlDB.conn.Where("pool_id = ?", survivorID).First(&survivor).Error)
	s.Require().Equal(uint64(100), survivor.Epoch)
}

// TestSchemaAuditFailsHardWhenMarkerLostButReceiptsTableSurvives covers the
// mirror witness-lost quadrant: only the receipts table survives. The
// replay record may still hold unanswered requests, so first use must fail
// hard and leave the surviving rows untouched.
func (s *OfflinePoolSettingsTestSuite) TestSchemaAuditFailsHardWhenMarkerLostButReceiptsTableSurvives() {
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.AutoMigrate(&OfflinePoolSettingsRequestReceipt{}))

	// a surviving receipt, as left behind by a lost marker
	s.Require().NoError(sqlDB.conn.Create(&OfflinePoolSettingsRequestReceipt{
		RequestID:   "req-survivor",
		PoolID:      uuid.MustParse(s.pool.ID),
		PayloadHash: offlineHash(`{"priority":7}`),
		Outcome:     string(params.OfflinePoolSettingsOutcomeRejectedMalformed),
		CreatedAt:   time.Now().UTC(),
	}).Error)

	_, _, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().Error(err)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().Contains(err.Error(), "installation marker is missing")

	priority := uint(7)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(
		params.OfflinePoolSettingsRevision{Epoch: 1}, params.OfflinePoolSettingsPatch{Priority: &priority}))
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)

	// nothing was installed or recreated, the surviving receipt is untouched
	s.Require().False(s.metaExists())
	s.Require().False(s.revisionExists())
	s.Require().Equal(int64(1), s.receiptCount())
	s.Require().Equal("req-survivor", s.getReceipt("req-survivor").RequestID)
}

// TestSchemaAuditFailsHardWhenMarkerLostAfterUse covers the restore-over-
// used-schema case: the schema was fully installed and used, then the
// marker table was lost (or a restored file predates it) while both data
// tables survive with rows. First use must fail hard rather than
// reinstalling over surviving rows, and the explicit reinstall restores
// service.
func (s *OfflinePoolSettingsTestSuite) TestSchemaAuditFailsHardWhenMarkerLostAfterUse() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	priority := uint(3)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev0,
		params.OfflinePoolSettingsPatch{Priority: &priority}))
	s.Require().NoError(err)
	survivingRev := s.currentRevision(s.pool.ID)
	s.Require().Equal(rev0.Seq+1, survivingRev.Seq)

	// lose only the marker table; the data tables survive with rows
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_meta").Error)
	s.Require().False(s.metaExists())
	s.Require().True(s.revisionExists())
	s.Require().True(s.receiptsExist())

	// GET and PATCH fail hard with the schema-missing surface
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	s.Require().Contains(err.Error(), "refusing to install over surviving rows")
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(survivingRev,
		params.OfflinePoolSettingsPatch{Priority: &priority}))
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)

	// no silent reinstall happened: rows, settings and receipts are as before
	s.Require().False(s.metaExists())
	s.Require().Equal(survivingRev, s.currentRevision(s.pool.ID))
	s.Require().Equal(uint(3), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(int64(1), s.receiptCount())

	// the explicit reinstall recreates the marker; the lost marker is
	// unrecoverable, so generation restarts at 1, and the surviving
	// revision row stays authoritative for its pool
	s.Require().NoError(sqlDB.ReinstallOfflinePoolSettingsSchema())
	meta := s.installedMetaRow()
	s.Require().True(meta.Installed)
	s.Require().Equal(uint64(1), meta.Generation)

	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(survivingRev, rev1)
}

// TestLegacyPreGenerationMarkerIsUpgradedDeterministically covers the
// marker migration: a database installed by code that predates the
// generation/last_epoch columns carries a marker row with only key,
// installed and the timestamps (the columns are missing from the table
// entirely). First use must add the columns, set generation to 1 and set
// last_epoch to the highest epoch of the surviving revision rows, so every
// epoch minted afterwards strictly exceeds every epoch a surviving row
// carries.
func (s *OfflinePoolSettingsTestSuite) TestLegacyPreGenerationMarkerIsUpgradedDeterministically() {
	sqlDB := s.Store.(*sqlDatabase)

	// The data tables never changed shape; only the marker predates the
	// generation/last_epoch columns.
	s.Require().NoError(sqlDB.conn.AutoMigrate(&OfflinePoolSettingsRevision{}, &OfflinePoolSettingsRequestReceipt{}))
	s.Require().NoError(sqlDB.conn.Exec(
		`CREATE TABLE offline_pool_settings_meta (key text PRIMARY KEY, installed boolean NOT NULL, created_at datetime, updated_at datetime)`).Error)
	now := time.Now().UTC()
	s.Require().NoError(sqlDB.conn.Exec(
		`INSERT INTO offline_pool_settings_meta (key, installed, created_at, updated_at) VALUES (?, 1, ?, ?)`,
		offlineSettingsMetaSchemaKey, now, now).Error)

	// Surviving revision rows from the pre-generation era, including one
	// for the suite pool so the first GET mints no new epoch.
	poolUUID := uuid.MustParse(s.pool.ID)
	s.Require().NoError(sqlDB.conn.Create(&OfflinePoolSettingsRevision{PoolID: poolUUID, Epoch: 100}).Error)
	otherUUID := uuid.New()
	s.Require().NoError(sqlDB.conn.Create(&OfflinePoolSettingsRevision{PoolID: otherUUID, Epoch: 150}).Error)

	// GET upgrades the marker in place: generation 1, last_epoch equal to
	// the highest surviving epoch; the existing revision row is returned
	// as-is.
	_, rev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(params.OfflinePoolSettingsRevision{Epoch: 100, Seq: 0}, rev)
	meta := s.installedMetaRow()
	s.Require().True(meta.Installed)
	s.Require().Equal(uint64(1), meta.Generation)
	s.Require().Equal(uint64(150), meta.LastEpoch)

	// the upgrade is idempotent: repeated reads change nothing
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)
	s.Require().Equal(uint64(150), s.installedMetaRow().LastEpoch)

	// a PATCH against the surviving revision advances seq on the existing
	// epoch; the surviving rows keep their epochs
	priority := uint(7)
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev,
		params.OfflinePoolSettingsPatch{Priority: &priority}))
	s.Require().NoError(err)
	s.Require().Equal(uint64(100), result.Revision.Epoch)
	s.Require().Equal(uint64(1), result.Revision.Seq)
	s.Require().Equal(uint(7), result.Settings.Priority)
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)

	// a revision row minted after the upgrade strictly exceeds every
	// pre-existing epoch
	repoEntity := s.entityOf(s.repo)
	secondPool, err := s.Store.CreateEntityPool(s.adminCtx, repoEntity, params.CreatePoolParams{
		ProviderName:   "test-provider",
		MaxRunners:     4,
		MinIdleRunners: 1,
		Image:          "test-image-2",
		Flavor:         "test-flavor",
		OSType:         "linux",
		Tags:           []string{"amd64-linux-runner"},
	})
	s.Require().NoError(err)
	_, freshRev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, repoEntity, secondPool.ID)
	s.Require().NoError(err)
	s.Require().Greater(freshRev.Epoch, uint64(150))
	s.Require().GreaterOrEqual(freshRev.Epoch, uint64(151))
	s.Require().Equal(s.installedMetaRow().LastEpoch, freshRev.Epoch)

	// the pre-existing rows are untouched
	s.Require().Equal(uint64(100), s.currentRevision(s.pool.ID).Epoch)
	s.Require().Equal(uint64(150), s.currentRevision(otherUUID.String()).Epoch)
}

// TestReinstallAfterLossBumpsGenerationAndInvalidatesOldETags exercises the
// explicit recovery: after the revision and receipts tables are lost, the
// experimental paths fail hard until ReinstallOfflinePoolSettingsSchema
// runs. The reinstall bumps the marker generation, recreates the tables
// and wipes their rows, and every pre-loss ETag is rejected stale: no
// client can write against revision state the server no longer holds.
func (s *OfflinePoolSettingsTestSuite) TestReinstallAfterLossBumpsGenerationAndInvalidatesOldETags() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().NotZero(rev0.Epoch)
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)

	// apply a keyed change so a receipt exists to lose
	priority := uint(3)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-pre-loss",
		PayloadHash: offlineHash(`{"priority":3}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.receiptCount())

	// lose both data tables; the installation marker survives
	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_revisions").Error)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_request_receipts").Error)

	// both experimental paths fail hard
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, req)
	s.Require().ErrorIs(err, errOfflineSettingsSchemaMissing)

	// explicit reinstall: generation bump, tables back, rows wiped
	s.Require().NoError(sqlDB.ReinstallOfflinePoolSettingsSchema())
	s.Require().True(s.revisionExists())
	s.Require().True(s.receiptsExist())
	meta := s.installedMetaRow()
	s.Require().True(meta.Installed)
	s.Require().Equal(uint64(2), meta.Generation)
	s.Require().GreaterOrEqual(meta.LastEpoch, rev0.Epoch)
	s.Require().Equal(int64(0), s.receiptCount())

	// the pre-loss receipt is gone: the same request id is treated as a
	// brand new request, and its pre-loss precondition is stale
	_, _, err = s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().Zero(rev1.Seq)
	s.Require().NotEqual(rev0, rev1)

	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, req)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedStale, result.Outcome)
	var staleErr *dbCommon.OfflinePoolSettingsStaleRevisionError
	s.Require().ErrorAs(err, &staleErr)
	s.Require().Equal(rev1, staleErr.Current)
	s.Require().NotEqual(rev0.Epoch, staleErr.Current.Epoch)

	// the settings row still carries the pre-loss value; only a fresh
	// conditional write against the new epoch may change it
	s.Require().Equal(uint(3), s.currentPoolRow(s.pool.ID).Priority)
	priority = uint(7)
	fresh := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-post-loss",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev1,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, fresh)
	s.Require().NoError(err)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, result.Outcome)
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)
}

// TestReinstallNeverReusesAnEpoch guards the durable generation finding:
// epochs are minted as max(unix_now, marker.last_epoch+1), so a reinstall
// that completes within the same Unix second as the original install must
// still mint a strictly greater epoch. A reused epoch could let an
// outstanding "<epoch>:<seq>" ETag match the restarted revision sequence
// and write on top of state the client never read.
func (s *OfflinePoolSettingsTestSuite) TestReinstallNeverReusesAnEpoch() {
	entity := s.orgEntity()

	// no sleep here on purpose: reinstall happens in the same Unix second
	// whenever the clock allows, which is exactly the dangerous case
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().NotZero(rev0.Epoch)
	s.Require().Equal(uint64(1), s.installedMetaRow().Generation)

	// advance the sequence so the dangerous pair exists: a client holding
	// "<epoch0>:<seq0>" observed the settings that were lost with the table
	priority := uint(9)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)

	sqlDB := s.Store.(*sqlDatabase)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_revisions").Error)
	s.Require().NoError(sqlDB.conn.Exec("DROP TABLE offline_pool_settings_request_receipts").Error)
	s.Require().NoError(sqlDB.ReinstallOfflinePoolSettingsSchema())
	s.Require().Equal(uint64(2), s.installedMetaRow().Generation)

	// even in the same second, the fresh epoch exceeds every epoch ever
	// minted; the dangerous "<epoch0>:0" pair can never be reissued
	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().Zero(rev1.Seq)
	s.Require().Greater(rev1.Epoch, rev0.Epoch)
	s.Require().GreaterOrEqual(rev1.Epoch, rev0.Epoch+1)
	s.Require().NotEqual(rev0, rev1)

	// and a second reinstall keeps climbing rather than reusing rev1's
	// epoch (the receipts/revision rows exist and intact this time: the
	// reinstall must still force a fresh epoch for every pool)
	s.Require().NoError(sqlDB.ReinstallOfflinePoolSettingsSchema())
	s.Require().Equal(uint64(3), s.installedMetaRow().Generation)
	_, rev2, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)
	s.Require().Zero(rev2.Seq)
	s.Require().Greater(rev2.Epoch, rev1.Epoch)
	s.Require().NotEqual(rev1, rev2)
}

// TestCommitThenDroppedResponseReplayIsIdempotent models the commit-then-
// drop-response failure: a keyed PATCH commits its effect and receipt, but
// the response never reaches the client (connection drop, crash between
// commit and write). The client retry must be answered from the durable
// receipt — 200 semantics with the stored resulting_etag — and further
// retries must not advance the revision again.
func (s *OfflinePoolSettingsTestSuite) TestCommitThenDroppedResponseReplayIsIdempotent() {
	entity := s.orgEntity()

	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, entity, s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-dropped-response",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().False(result.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, result.Outcome)
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)

	// the receipt is durable even though no response was ever produced
	receipt := s.getReceipt("req-dropped-response")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeAppliedChanged), receipt.Outcome)
	s.Require().NotNil(receipt.ResultingETag)
	s.Require().Equal(result.Revision.ETag(), *receipt.ResultingETag)

	// the retry — same key, same payload, now-stale precondition — is
	// answered from the receipt with the stored resulting etag
	for i := 0; i < 2; i++ {
		replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, req)
		s.Require().NoError(err)
		s.Require().True(replayed.Replayed)
		s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, replayed.Outcome)
		s.Require().Equal(result.Revision.ETag(), replayed.ResultingETag)
		s.Require().Equal(params.OfflinePoolSettings{}, replayed.Settings)

		// idempotent: no additional revision advance, no extra receipt, no
		// settings change
		s.Require().Equal(result.Revision, s.currentRevision(s.pool.ID))
		s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)
		s.Require().Equal(int64(1), s.receiptCount())
	}
}

// TestUpdateAdvancesSeqExactlyOnce guards the no-double-bump guarantee: one
// PATCH through the experimental API advances the revision exactly once,
// even though the stock update path now also bumps revisions.
func (s *OfflinePoolSettingsTestSuite) TestUpdateAdvancesSeqExactlyOnce() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, result.Outcome)
	s.Require().Equal(rev0.Seq+1, result.Revision.Seq)
	s.Require().Equal(rev0.Seq+1, s.currentRevision(s.pool.ID).Seq)

	// a legacy write on top advances exactly once more
	otherPriority := uint(9)
	_, err = s.Store.UpdateEntityPool(s.adminCtx, s.orgEntity(), s.pool.ID, params.UpdatePoolParams{
		Priority: &otherPriority,
	})
	s.Require().NoError(err)
	s.Require().Equal(rev0.Seq+2, s.currentRevision(s.pool.ID).Seq)
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptAppliedChanged() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-applied",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().False(result.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, result.Outcome)

	receipt := s.getReceipt("req-applied")
	s.Require().Equal(offlineHash(`{"priority":7}`), receipt.PayloadHash)
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeAppliedChanged), receipt.Outcome)
	s.Require().NotNil(receipt.PresentedETag)
	s.Require().Equal(rev0.ETag(), *receipt.PresentedETag)
	s.Require().NotNil(receipt.ResultingETag)
	s.Require().Equal(result.Revision.ETag(), *receipt.ResultingETag)
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptReplayHasNoAdditionalEffect() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-replay",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().Equal(rev0.Seq+1, result.Revision.Seq)

	// simulated client restart: identical retry (same key and payload; the
	// precondition is stale now, which must not matter for a replay)
	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, replayed.Outcome)
	s.Require().Equal(result.Revision.ETag(), replayed.ResultingETag)

	// zero additional effect
	s.Require().Equal(result.Revision, s.currentRevision(s.pool.ID))
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(int64(1), s.receiptCount())
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptIDReuseWithDifferentPayload() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-reuse",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)

	// same key, different payload -> id reuse error, no state change
	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)
	otherPriority := uint(8)
	reused := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-reuse",
		PayloadHash: offlineHash(`{"priority":8}`),
		IfMatch:     rev1,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &otherPriority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, reused)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsRequestIDReuse)

	// original receipt untouched, original retry still replays
	receipt := s.getReceipt("req-reuse")
	s.Require().Equal(offlineHash(`{"priority":7}`), receipt.PayloadHash)
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeAppliedChanged), receipt.Outcome)

	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(rev1.ETag(), replayed.ResultingETag)
	s.Require().Equal(rev1, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptIDReuseWithDifferentScope() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-scope",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)

	// same key and payload, but a different pool
	repoEntity := s.entityOf(s.repo)
	repoPool, err := s.Store.CreateEntityPool(s.adminCtx, repoEntity, params.CreatePoolParams{
		ProviderName:   "test-provider",
		MaxRunners:     4,
		MinIdleRunners: 1,
		Image:          "test-image-2",
		Flavor:         "test-flavor",
		OSType:         "linux",
		Tags:           []string{"amd64-linux-runner"},
	})
	s.Require().NoError(err)
	_, repoRev, err := s.Store.GetOfflinePoolSettings(s.adminCtx, repoEntity, repoPool.ID)
	s.Require().NoError(err)

	reused := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-scope",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatch:     repoRev,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, repoEntity, repoPool.ID, reused)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsRequestIDReuse)
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptAppliedNoop() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// the suite pool already has priority 0
	priority := uint(0)
	req := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-noop",
		PayloadHash: offlineHash(`{"priority":0}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedNoop, result.Outcome)
	s.Require().Equal(rev0, result.Revision)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))

	receipt := s.getReceipt("req-noop")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeAppliedNoop), receipt.Outcome)
	s.Require().NotNil(receipt.ResultingETag)
	s.Require().Equal(rev0.ETag(), *receipt.ResultingETag)

	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedNoop, replayed.Outcome)
	s.Require().Equal(rev0.ETag(), replayed.ResultingETag)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptRejectedStaleCarriesObservedETag() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(3)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	_, rev1, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// stale request with its own key
	priority = uint(9)
	staleReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-stale",
		PayloadHash: offlineHash(`{"priority":9}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, staleReq)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedStale, result.Outcome)

	receipt := s.getReceipt("req-stale")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeRejectedStale), receipt.Outcome)
	s.Require().NotNil(receipt.ResultingETag)
	s.Require().Equal(rev1.ETag(), *receipt.ResultingETag)
	s.Require().NotNil(receipt.PresentedETag)
	s.Require().Equal(rev0.ETag(), *receipt.PresentedETag)

	// replaying the stale request returns the stored outcome and etag
	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, staleReq)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedStale, replayed.Outcome)
	s.Require().Equal(rev1.ETag(), replayed.ResultingETag)
	s.Require().Equal(rev1, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptRejectedPolicy() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// min_idle_runners (6) > max_runners (5)
	minIdle := uint(6)
	policyReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-policy",
		PayloadHash: offlineHash(`{"min_idle_runners":6}`),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{MinIdleRunners: &minIdle},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, policyReq)
	s.Require().ErrorIs(err, runnerErrors.ErrUnprocessable)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedPolicy, result.Outcome)

	receipt := s.getReceipt("req-policy")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeRejectedPolicy), receipt.Outcome)
	s.Require().Nil(receipt.ResultingETag)

	// nothing changed
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
	s.Require().Equal(uint(2), s.currentPoolRow(s.pool.ID).MinIdleRunners)

	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, policyReq)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedPolicy, replayed.Outcome)
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptRejectedMalformed() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	bodyErr := fmt.Errorf("unknown field %q: %w", "bogus", errOfflineTestMalformed)
	malformedReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-malformed",
		PayloadHash: offlineHash(`{"bogus":1}`),
		IfMatch:     rev0,
		BodyErr:     bodyErr,
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, malformedReq)
	s.Require().ErrorIs(err, errOfflineTestMalformed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedMalformed, result.Outcome)

	receipt := s.getReceipt("req-malformed")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeRejectedMalformed), receipt.Outcome)
	s.Require().NotNil(receipt.PresentedETag)
	s.Require().Equal(rev0.ETag(), *receipt.PresentedETag)
	s.Require().Nil(receipt.ResultingETag)

	// the row is untouched
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))

	replayed, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, malformedReq)
	s.Require().NoError(err)
	s.Require().True(replayed.Replayed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedMalformed, replayed.Outcome)
}

func (s *OfflinePoolSettingsTestSuite) TestReceiptRejectedMalformedIfMatch() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	ifMatchErr := fmt.Errorf("expected a single quoted etag: %w", errOfflineTestMalformed)
	malformedReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-malformed-ifmatch",
		PayloadHash: offlineHash(`{"priority":7}`),
		IfMatchErr:  ifMatchErr,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, malformedReq)
	s.Require().ErrorIs(err, errOfflineTestMalformed)
	s.Require().Equal(params.OfflinePoolSettingsOutcomeRejectedMalformed, result.Outcome)

	receipt := s.getReceipt("req-malformed-ifmatch")
	s.Require().Equal(string(params.OfflinePoolSettingsOutcomeRejectedMalformed), receipt.Outcome)
	s.Require().Nil(receipt.PresentedETag)
	s.Require().Nil(receipt.ResultingETag)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

// TestNoReceiptFor403And404 verifies that requests rejected before the
// write transaction (pool missing, pool not a member of the path entity)
// never persist a receipt.
func (s *OfflinePoolSettingsTestSuite) TestNoReceiptFor403And404() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	notFoundReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-404",
		PayloadHash: offlineHash("404"),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), "00000000-0000-0000-0000-000000000000", notFoundReq)
	s.Require().ErrorIs(err, runnerErrors.ErrNotFound)

	forbiddenReq := params.OfflinePoolSettingsUpdateRequest{
		RequestID:   "req-403",
		PayloadHash: offlineHash("403"),
		IfMatch:     rev0,
		Patch:       &params.OfflinePoolSettingsPatch{Priority: &priority},
	}
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.entityOf(s.otherOrg), s.pool.ID, forbiddenReq)
	s.Require().ErrorIs(err, dbCommon.ErrPoolNotMemberOfEntity)

	s.Require().Zero(s.receiptCount())
}

// TestReceiptWithoutKeyIsDurableButNotReplayable verifies that a PATCH
// without an Idempotency-Key still persists a receipt (addressable only by
// its generated id).
func (s *OfflinePoolSettingsTestSuite) TestReceiptWithoutKeyIsDurableButNotReplayable() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(7)
	_, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	}))
	s.Require().NoError(err)
	s.Require().Equal(int64(1), s.receiptCount())
}

var errOfflineTestMalformed = fmt.Errorf("malformed settings document")

// TestConcurrentConditionalUpdatesExactlyOneApplies races N conditional
// updates, all holding the same observed revision, against one real SQLite
// database. Exactly one update may commit; every loser must come back as a
// typed stale rejection and, critically, must not leave its settings write
// behind: the pool row must reflect only the winner even when the race is
// detected at revision-advance time (after the early stale check passed).
func (s *OfflinePoolSettingsTestSuite) TestConcurrentConditionalUpdatesExactlyOneApplies() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	const racers = 8
	type outcome struct {
		priority uint
		result   params.OfflinePoolSettingsUpdateResult
		err      error
	}
	results := make(chan outcome, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			priority := uint(100 + i)
			req := offlineUpdateReq(rev0, params.OfflinePoolSettingsPatch{Priority: &priority})
			req.RequestID = fmt.Sprintf("race-%d", i)
			result, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, req)
			results <- outcome{priority: priority, result: result, err: err}
		}(i)
	}
	wg.Wait()
	close(results)

	var winners []uint
	losers := 0
	for res := range results {
		if res.err == nil {
			s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, res.result.Outcome)
			winners = append(winners, res.priority)
			continue
		}
		s.Require().ErrorIs(res.err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
		losers++
	}
	s.Require().Len(winners, 1, "exactly one racing conditional update may commit")
	s.Require().Equal(racers-1, losers)

	// The revision moved exactly once and the row carries only the winner's
	// value: a rejection never commits its settings change, no matter where
	// the race was detected.
	s.Require().Equal(rev0.Seq+1, s.currentRevision(s.pool.ID).Seq)
	s.Require().Equal(winners[0], s.currentPoolRow(s.pool.ID).Priority)
	// Every racer persisted a durable request receipt.
	s.Require().Equal(int64(racers), s.receiptCount())
}

func TestOfflinePoolSettingsTestSuite(t *testing.T) {
	suite.Run(t, new(OfflinePoolSettingsTestSuite))
}

// TestAbsentRevisionTableErrorDialects qualifies the benign-error
// classifier across the database dialects GARM supports. Only the
// absent-side-table form may be swallowed by the legacy bump; anything
// else must fail the write.
func TestAbsentRevisionTableErrorDialects(t *testing.T) {
	benign := []string{
		"no such table: offline_pool_settings_revisions",                                 // SQLite (mattn, modernc)
		"Error 1146 (42S02): Table 'garm.offline_pool_settings_revisions' doesn't exist", // MySQL
		`pq: relation "offline_pool_settings_revisions" does not exist`,                  // PostgreSQL
	}
	for _, msg := range benign {
		if !absentRevisionTableError(fmt.Errorf("%w", errors.New(msg))) {
			t.Errorf("expected benign classification for %q", msg)
		}
	}
	real := []string{
		"database is locked",
		"UNIQUE constraint failed: pools.id",
		"Error 1213 (40001): Deadlock found when trying to get lock",
		"context deadline exceeded",
		"disk I/O error",
		"missing destination name some_column in *sql.Row",
	}
	for _, msg := range real {
		if absentRevisionTableError(errors.New(msg)) {
			t.Errorf("expected real-failure classification for %q", msg)
		}
	}
}

// TestLegacyWriteWithoutExperimentalSchema verifies the benign bump path:
// a stock pool update on a database where the experimental feature was
// never used must succeed, change the row and leave no experimental
// schema behind.
func (s *OfflinePoolSettingsTestSuite) TestLegacyWriteWithoutExperimentalSchema() {
	s.Require().False(s.revisionExists())
	s.Require().False(s.receiptsExist())

	maxRunners := uint(11)
	_, err := s.Store.UpdateEntityPool(s.adminCtx, s.orgEntity(), s.pool.ID, params.UpdatePoolParams{
		MaxRunners: &maxRunners,
	})
	s.Require().NoError(err)
	s.Require().Equal(uint(11), s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().False(s.revisionExists())
	s.Require().False(s.receiptsExist())
}
