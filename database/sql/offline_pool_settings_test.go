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

	sqlDB := s.Store.(*sqlDatabase)
	var count int64
	s.Require().NoError(sqlDB.conn.Raw(
		"SELECT count(1) FROM sqlite_master WHERE type='table' AND name='offline_pool_settings_revisions'").
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
	settings, rev1, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().NoError(err)
	s.Require().Equal(uint(7), settings.Priority)
	s.Require().Equal(uint(5), settings.MaxRunners)
	s.Require().Equal(uint(2), settings.MinIdleRunners)
	s.Require().Equal(rev0.Epoch, rev1.Epoch)
	s.Require().Equal(rev0.Seq+1, rev1.Seq)

	// the stock pools row reflects the change
	s.Require().Equal(uint(7), s.currentPoolRow(s.pool.ID).Priority)

	// another update advances the revision again
	minIdle := uint(4)
	_, rev2, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev1, params.OfflinePoolSettingsPatch{
		MinIdleRunners: &minIdle,
	})
	s.Require().NoError(err)
	s.Require().Equal(rev1.Seq+1, rev2.Seq)
	s.Require().Equal(uint(4), s.currentPoolRow(s.pool.ID).MinIdleRunners)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateStaleRevision() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(3)
	_, _, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().NoError(err)

	// patching with the now stale revision fails and reports the current one
	priority = 9
	settings, _, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().Error(err)
	s.Require().ErrorIs(err, dbCommon.ErrOfflinePoolSettingsStaleRevision)
	var staleErr *dbCommon.OfflinePoolSettingsStaleRevisionError
	s.Require().ErrorAs(err, &staleErr)
	s.Require().Equal(rev0.Seq+1, staleErr.Current.Seq)
	s.Require().Equal(params.OfflinePoolSettings{}, settings)

	// row is byte-for-byte unchanged: priority stuck at 3, seq advanced once
	s.Require().Equal(uint(3), s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(rev0.Seq+1, s.currentRevision(s.pool.ID).Seq)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateMinIdleGreaterThanMax() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	// min_idle_runners exceeding the current max_runners
	minIdle := uint(6)
	_, _, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		MinIdleRunners: &minIdle,
	})
	s.Require().ErrorIs(err, runnerErrors.ErrUnprocessable)

	// lowering max_runners below the current min_idle_runners also trips the
	// invariant, even though min_idle_runners is not supplied
	maxRunners := uint(1)
	_, _, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		MaxRunners: &maxRunners,
	})
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
	settings, rev, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().NoError(err)
	s.Require().Equal(rev0, rev)
	s.Require().Equal(uint(0), settings.Priority)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))
}

func (s *OfflinePoolSettingsTestSuite) TestUpdateEmptyPatchRejected() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	_, _, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{})
	s.Require().ErrorIs(err, runnerErrors.ErrBadRequest)
}

func (s *OfflinePoolSettingsTestSuite) TestUpdatePoolNotMemberOfEntity() {
	_, rev0, err := s.Store.GetOfflinePoolSettings(s.adminCtx, s.orgEntity(), s.pool.ID)
	s.Require().NoError(err)

	priority := uint(1)
	_, _, err = s.Store.UpdateOfflinePoolSettings(s.adminCtx, s.entityOf(s.otherOrg), s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
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
				_, _, err := secondHandle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, curRev, params.OfflinePoolSettingsPatch{
					Priority: &priority,
				})
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
	settings, _, err := handle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().Error(err)
	s.Require().Equal(params.OfflinePoolSettings{}, settings)

	// byte-for-byte invariance
	s.Require().Equal(beforePool.Priority, s.currentPoolRow(s.pool.ID).Priority)
	s.Require().Equal(beforePool.MaxRunners, s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(beforePool.MinIdleRunners, s.currentPoolRow(s.pool.ID).MinIdleRunners)
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))

	_, err = dbConn.ExecContext(context.Background(), "ROLLBACK")
	s.Require().NoError(err)

	// once the lock is released, the same update succeeds
	_, rev1, err := handle.UpdateOfflinePoolSettings(context.Background(), entity, s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().NoError(err)
	s.Require().Equal(rev0.Seq+1, rev1.Seq)
	s.Require().Equal(uint(42), s.currentPoolRow(s.pool.ID).Priority)
}

// TestLegacyWriterDoesNotBumpRevision documents the known limitation:
// stock writers update the same pools columns without advancing the
// offline revision.
func (s *OfflinePoolSettingsTestSuite) TestLegacyWriterDoesNotBumpRevision() {
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
	// ... but the offline revision did not move
	s.Require().Equal(rev0, s.currentRevision(s.pool.ID))

	// and a client holding the old ETag can still commit on top
	priority := uint(2)
	_, rev1, err := s.Store.UpdateOfflinePoolSettings(s.adminCtx, entity, s.pool.ID, rev0, params.OfflinePoolSettingsPatch{
		Priority: &priority,
	})
	s.Require().NoError(err)
	s.Require().Equal(rev0.Seq+1, rev1.Seq)
	s.Require().Equal(uint(10), s.currentPoolRow(s.pool.ID).MaxRunners)
	s.Require().Equal(uint(2), s.currentPoolRow(s.pool.ID).Priority)
}

func TestOfflinePoolSettingsTestSuite(t *testing.T) {
	suite.Run(t, new(OfflinePoolSettingsTestSuite))
}
