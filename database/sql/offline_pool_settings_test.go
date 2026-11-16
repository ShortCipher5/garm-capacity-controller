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
	"fmt"
	"sync"
	"testing"

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
