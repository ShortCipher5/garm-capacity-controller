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

package routers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/cloudbase/garm/apiserver/controllers"
	"github.com/cloudbase/garm/auth"
	"github.com/cloudbase/garm/config"
	"github.com/cloudbase/garm/database"
	dbCommon "github.com/cloudbase/garm/database/common"
	"github.com/cloudbase/garm/database/watcher"
	garmTesting "github.com/cloudbase/garm/internal/testing"
	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/runner"
)

// nolint: gosec
const testJWTSecret = "test-jwt-secret-do-not-use-in-production-0123456789"

type offlinePoolSettingsEnv struct {
	db         dbCommon.Store
	dbCfg      config.Database
	server     *httptest.Server
	adminToken string
	userToken  string

	org        params.Organization
	org2       params.Organization
	repo       params.Repository
	enterprise params.Enterprise
	orgPool    params.Pool
	repoPool   params.Pool
	entPool    params.Pool
}

func offlineSettingsPath(entityType, entityID, poolID string) string {
	return fmt.Sprintf("/api/v1/offline/entities/%s/%s/pools/%s/settings", entityType, entityID, poolID)
}

// setupOfflinePoolSettingsEnv builds a full stack (sqlite database, runner,
// controller, real router) and starts a test HTTP server. When flag is
// false, the router is built with the experimental API disabled.
func setupOfflinePoolSettingsEnv(t *testing.T, flag bool) *offlinePoolSettingsEnv {
	t.Helper()

	ctx := context.Background()
	watcher.InitWatcher(ctx)
	t.Cleanup(func() { watcher.CloseWatcher() })

	dbCfg := garmTesting.GetTestSqliteDBConfig(t)
	db, err := database.NewDatabase(ctx, dbCfg)
	if err != nil {
		t.Fatalf("failed to create db connection: %s", err)
	}
	if _, err := db.InitController(); err != nil {
		t.Fatalf("failed to init controller: %s", err)
	}

	adminCtx := garmTesting.ImpersonateAdminContext(ctx, db, t)
	// the urls required middleware demands these
	metadataURL := "https://garm.example.com/api/v1/metadata"
	callbackURL := "https://garm.example.com/api/v1/callbacks"
	webhookURL := "https://garm.example.com/webhooks"
	if _, err := db.UpdateController(params.UpdateControllerParams{
		MetadataURL: &metadataURL,
		CallbackURL: &callbackURL,
		WebhookURL:  &webhookURL,
	}); err != nil {
		t.Fatalf("failed to set controller URLs: %s", err)
	}

	githubEndpoint := garmTesting.CreateDefaultGithubEndpoint(adminCtx, db, t)
	creds := garmTesting.CreateTestGithubCredentials(adminCtx, "new-creds", db, t, githubEndpoint)

	env := &offlinePoolSettingsEnv{db: db, dbCfg: dbCfg}

	jwtCfg := config.JWTAuth{
		Secret:     testJWTSecret,
		TimeToLive: "1h",
	}
	authenticator := auth.NewAuthenticator(jwtCfg, db)
	adminToken, err := authenticator.GetJWTToken(adminCtx)
	if err != nil {
		t.Fatalf("failed to get admin token: %s", err)
	}
	env.adminToken = adminToken

	regularUser := garmTesting.CreateGARMTestUser(adminCtx, "regular-user", db, t)
	userCtx := auth.PopulateContext(ctx, regularUser, nil)
	userToken, err := authenticator.GetJWTToken(userCtx)
	if err != nil {
		t.Fatalf("failed to get user token: %s", err)
	}
	env.userToken = userToken

	tokenGetter, err := auth.NewInstanceTokenGetter(jwtCfg.Secret)
	if err != nil {
		t.Fatalf("failed to create instance token getter: %s", err)
	}
	// Create the runner before any entity exists, so no pool managers are
	// started. Besides keeping the test focused, this avoids a known data
	// race in the stock pool manager startup path (runner/pool) that
	// existing runner tests sidestep by never calling NewRunner.
	testRunner, err := runner.NewRunner(ctx, config.Config{}, db, tokenGetter)
	if err != nil {
		t.Fatalf("failed to create runner: %s", err)
	}

	// now create the entities the tests exercise
	org, err := db.CreateOrganization(adminCtx, "test-org", creds, "test-webhook-secret", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		t.Fatalf("failed to create org: %s", err)
	}
	org2, err := db.CreateOrganization(adminCtx, "test-org-2", creds, "test-webhook-secret-2", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		t.Fatalf("failed to create second org: %s", err)
	}
	repo, err := db.CreateRepository(adminCtx, "test-owner", "test-repo", creds, "test-webhook-secret-3", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		t.Fatalf("failed to create repo: %s", err)
	}
	enterprise, err := db.CreateEnterprise(adminCtx, "test-enterprise", creds, "test-webhook-secret-4", params.PoolBalancerTypeRoundRobin, false)
	if err != nil {
		t.Fatalf("failed to create enterprise: %s", err)
	}

	env.org = org
	env.org2 = org2
	env.repo = repo
	env.enterprise = enterprise
	env.orgPool = createPoolForEnv(t, db, adminCtx, org, "org-pool", 5, 2, 1)
	env.repoPool = createPoolForEnv(t, db, adminCtx, repo, "repo-pool", 6, 3, 2)
	env.entPool = createPoolForEnv(t, db, adminCtx, enterprise, "ent-pool", 7, 1, 3)

	controller, err := controllers.NewAPIController(testRunner, authenticator, nil, nil, nil, config.APIServer{})
	if err != nil {
		t.Fatalf("failed to create api controller: %s", err)
	}

	jwtMiddleware, err := auth.NewjwtMiddleware(db, jwtCfg)
	if err != nil {
		t.Fatalf("failed to create jwt middleware: %s", err)
	}
	initMiddleware, err := auth.NewInitRequiredMiddleware(db)
	if err != nil {
		t.Fatalf("failed to create init middleware: %s", err)
	}
	urlsRequiredMiddleware, err := auth.NewUrlsRequiredMiddleware(db)
	if err != nil {
		t.Fatalf("failed to create urls required middleware: %s", err)
	}
	instanceMiddleware, err := auth.NewInstanceMiddleware(db, jwtCfg)
	if err != nil {
		t.Fatalf("failed to create instance middleware: %s", err)
	}

	router := NewAPIRouter(
		controller,
		jwtMiddleware,
		initMiddleware,
		urlsRequiredMiddleware,
		instanceMiddleware,
		false,
		flag,
	)

	env.server = httptest.NewServer(router)
	t.Cleanup(env.server.Close)
	return env
}

type entityHolder interface {
	GetEntity() (params.ForgeEntity, error)
}

func createPoolForEnv(t *testing.T, db dbCommon.Store, ctx context.Context, holder entityHolder, image string, maxRunners, minIdleRunners, priority uint) params.Pool {
	t.Helper()
	entity, err := holder.GetEntity()
	if err != nil {
		t.Fatalf("failed to get entity: %s", err)
	}
	pool, err := db.CreateEntityPool(
		ctx,
		entity,
		params.CreatePoolParams{
			ProviderName:   "test-provider",
			MaxRunners:     maxRunners,
			MinIdleRunners: minIdleRunners,
			Priority:       priority,
			Image:          image,
			Flavor:         "test-flavor",
			OSType:         "linux",
			Tags:           []string{"amd64-linux-runner"},
		},
	)
	if err != nil {
		t.Fatalf("failed to create pool: %s", err)
	}
	return pool
}

type offlineResponse struct {
	status int
	etag   string
	body   []byte
}

func (e *offlinePoolSettingsEnv) do(t *testing.T, method, path, token, ifMatch string, body string, idemKey ...string) offlineResponse {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatalf("failed to build request: %s", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if len(idemKey) > 0 && idemKey[0] != "" {
		req.Header.Set("Idempotency-Key", idemKey[0])
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %s", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %s", err)
	}
	return offlineResponse{
		status: resp.StatusCode,
		etag:   resp.Header.Get("ETag"),
		body:   respBody,
	}
}

// receiptCount asks the database directly how many request receipts exist.
func (e *offlinePoolSettingsEnv) receiptCount(t *testing.T) int64 {
	t.Helper()
	checkDB, err := gorm.Open(sqlite.Open(e.dbCfg.SQLite.DBFile), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open check db: %s", err)
	}
	sqlDB, err := checkDB.DB()
	if err != nil {
		t.Fatalf("failed to get check db handle: %s", err)
	}
	defer sqlDB.Close()

	var count int64
	if err := checkDB.Raw("SELECT count(1) FROM offline_pool_settings_request_receipts").Scan(&count).Error; err != nil {
		t.Fatalf("failed to count receipts: %s", err)
	}
	return count
}

// receiptCountFor asks the database directly how many request receipts
// exist for one request id.
func (e *offlinePoolSettingsEnv) receiptCountFor(t *testing.T, requestID string) int64 {
	t.Helper()
	checkDB, err := gorm.Open(sqlite.Open(e.dbCfg.SQLite.DBFile), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open check db: %s", err)
	}
	sqlDB, err := checkDB.DB()
	if err != nil {
		t.Fatalf("failed to get check db handle: %s", err)
	}
	defer sqlDB.Close()

	var count int64
	if err := checkDB.Raw("SELECT count(1) FROM offline_pool_settings_request_receipts WHERE request_id = ?", requestID).
		Scan(&count).Error; err != nil {
		t.Fatalf("failed to count receipts: %s", err)
	}
	return count
}

// etagSeq extracts the seq component of a quoted "<epoch>:<seq>" ETag.
func etagSeq(t *testing.T, etag string) uint64 {
	t.Helper()
	parts := strings.Split(strings.Trim(etag, `"`), ":")
	if len(parts) != 2 {
		t.Fatalf("malformed etag %q", etag)
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("malformed etag %q: %s", etag, err)
	}
	return seq
}

// getETag fetches the current ETag of a pool settings resource.
func (e *offlinePoolSettingsEnv) getETag(t *testing.T, path string) string {
	t.Helper()
	resp := e.do(t, http.MethodGet, path, e.adminToken, "", "")
	if resp.status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.status, resp.body)
	}
	if resp.etag == "" || !strings.HasPrefix(resp.etag, "\"") || !strings.HasSuffix(resp.etag, "\"") {
		t.Fatalf("expected a quoted strong ETag, got %q", resp.etag)
	}
	return resp.etag
}

type OfflinePoolSettingsHTTPSuite struct {
	suite.Suite
	env *offlinePoolSettingsEnv
}

func (s *OfflinePoolSettingsHTTPSuite) SetupTest() {
	s.env = setupOfflinePoolSettingsEnv(s.T(), true)
}

func (s *OfflinePoolSettingsHTTPSuite) orgPoolPath() string {
	return offlineSettingsPath("organization", s.env.org.ID, s.env.orgPool.ID)
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetReturnsSettingsAndStrongETag() {
	resp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.adminToken, "", "")
	s.Require().Equal(http.StatusOK, resp.status)
	s.Require().NotEmpty(resp.etag)
	s.Require().True(strings.HasPrefix(resp.etag, "\""))
	s.Require().True(strings.HasSuffix(resp.etag, "\""))

	parts := strings.Split(strings.Trim(resp.etag, "\""), ":")
	s.Require().Len(parts, 2)

	var settings params.OfflinePoolSettings
	s.Require().NoError(json.Unmarshal(resp.body, &settings))
	s.Require().Equal(uint(1), settings.Priority)
	s.Require().Equal(uint(5), settings.MaxRunners)
	s.Require().Equal(uint(2), settings.MinIdleRunners)
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetWorksForAllEntityTypes() {
	cases := []struct {
		entityType string
		entityID   string
		poolID     string
		maxRunners uint
	}{
		{"organization", s.env.org.ID, s.env.orgPool.ID, 5},
		{"repository", s.env.repo.ID, s.env.repoPool.ID, 6},
		{"enterprise", s.env.enterprise.ID, s.env.entPool.ID, 7},
	}
	for _, c := range cases {
		path := offlineSettingsPath(c.entityType, c.entityID, c.poolID)
		resp := s.env.do(s.T(), http.MethodGet, path, s.env.adminToken, "", "")
		s.Require().Equal(http.StatusOK, resp.status, "path: %s", path)

		var settings params.OfflinePoolSettings
		s.Require().NoError(json.Unmarshal(resp.body, &settings))
		s.Require().Equal(c.maxRunners, settings.MaxRunners)
	}
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetTrailingSlash() {
	resp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath()+"/", s.env.adminToken, "", "")
	s.Require().Equal(http.StatusOK, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetNonexistentPool() {
	path := offlineSettingsPath("organization", s.env.org.ID, "00000000-0000-0000-0000-000000000000")
	resp := s.env.do(s.T(), http.MethodGet, path, s.env.adminToken, "", "")
	s.Require().Equal(http.StatusNotFound, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetUnauthenticated() {
	resp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), "", "", "")
	s.Require().Equal(http.StatusUnauthorized, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchHappyPathAndETagRoundTrip() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"priority":9,"min_idle_runners":4}`)
	s.Require().Equal(http.StatusOK, resp.status, "body: %s", resp.body)
	s.Require().NotEqual(etag, resp.etag)

	var settings params.OfflinePoolSettings
	s.Require().NoError(json.Unmarshal(resp.body, &settings))
	s.Require().Equal(uint(9), settings.Priority)
	s.Require().Equal(uint(5), settings.MaxRunners)
	s.Require().Equal(uint(4), settings.MinIdleRunners)

	// a GET now returns the new values and the new ETag
	getResp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.adminToken, "", "")
	s.Require().Equal(http.StatusOK, getResp.status)
	s.Require().Equal(resp.etag, getResp.etag)
	var current params.OfflinePoolSettings
	s.Require().NoError(json.Unmarshal(getResp.body, &current))
	s.Require().Equal(settings, current)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchMissingIfMatch() {
	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, "",
		`{"priority":9}`)
	s.Require().Equal(http.StatusPreconditionRequired, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchMalformedIfMatch() {
	for _, ifMatch := range []string{
		`abc`,
		`"1-2"`,
		`"1:2:3"`,
		`"1:2", "3:4"`,
		`"1:"`,
		`W/"1:2"`,
		`*`,
		`""`,
	} {
		resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, ifMatch,
			`{"priority":9}`)
		s.Require().Equal(http.StatusBadRequest, resp.status, "If-Match: %s", ifMatch)
	}
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchStaleIfMatch() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	// consume the etag
	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"priority":8}`)
	s.Require().Equal(http.StatusOK, resp.status)

	// replaying the old etag fails with 412 and the current etag
	resp = s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"priority":9}`)
	s.Require().Equal(http.StatusPreconditionFailed, resp.status)
	s.Require().NotEmpty(resp.etag)
	s.Require().Contains(string(resp.body), strings.Trim(resp.etag, "\""))

	// and the row holds the first patch, not the rejected one
	getResp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.adminToken, "", "")
	var settings params.OfflinePoolSettings
	s.Require().NoError(json.Unmarshal(getResp.body, &settings))
	s.Require().Equal(uint(8), settings.Priority)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchMalformedBodies() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())
	bodies := []string{
		`{}`,                           // at least one field required
		`{"unknown":1}`,                // unknown field
		`{"priority":1,"priority":2}`,  // duplicate field
		`{"priority":"1"}`,             // wrong type
		`{"priority":1.5}`,             // non-integer
		`{"priority":1e2}`,             // exponent form
		`{"priority":01}`,              // leading zero
		`{"priority":-1}`,              // negative
		`{"max_runners":0}`,            // below minimum
		`{"priority":1000001}`,         // above maximum
		`{"min_idle_runners":1000001}`, // above maximum
		`{"max_runners":1000001}`,      // above maximum
		`{"priority":1} trailing`,      // garbage after object
		`[1,2,3]`,                      // not an object
		``,                             // empty body
	}
	for _, body := range bodies {
		resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag, body)
		s.Require().Equal(http.StatusBadRequest, resp.status, "body: %s", body)
	}
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchMinIdleGreaterThanMax() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"min_idle_runners":6}`)
	s.Require().Equal(http.StatusUnprocessableEntity, resp.status)

	// row unchanged
	getResp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.adminToken, "", "")
	s.Require().Equal(etag, getResp.etag)
	var settings params.OfflinePoolSettings
	s.Require().NoError(json.Unmarshal(getResp.body, &settings))
	s.Require().Equal(uint(2), settings.MinIdleRunners)

	// supplying both with min > max is also 422
	resp = s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"max_runners":3,"min_idle_runners":4}`)
	s.Require().Equal(http.StatusUnprocessableEntity, resp.status)

	// min == max is fine
	resp = s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"max_runners":4,"min_idle_runners":4}`)
	s.Require().Equal(http.StatusOK, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchNoopDoesNotAdvanceRevision() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag,
		`{"priority":1}`)
	s.Require().Equal(http.StatusOK, resp.status)
	s.Require().Equal(etag, resp.etag)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchWrongEntity() {
	cases := []string{
		offlineSettingsPath("organization", s.env.org2.ID, s.env.orgPool.ID),
		offlineSettingsPath("repository", s.env.repo.ID, s.env.orgPool.ID),
		offlineSettingsPath("enterprise", s.env.enterprise.ID, s.env.orgPool.ID),
		offlineSettingsPath("organization", "00000000-0000-0000-0000-000000000000", s.env.orgPool.ID),
	}
	for _, path := range cases {
		resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, `"0:0"`, `{"priority":9}`)
		s.Require().Equal(http.StatusForbidden, resp.status, "path: %s", path)
	}
}

func (s *OfflinePoolSettingsHTTPSuite) TestGetWrongEntity() {
	path := offlineSettingsPath("organization", s.env.org2.ID, s.env.orgPool.ID)
	resp := s.env.do(s.T(), http.MethodGet, path, s.env.adminToken, "", "")
	s.Require().Equal(http.StatusForbidden, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchNonexistentPool() {
	path := offlineSettingsPath("organization", s.env.org.ID, "00000000-0000-0000-0000-000000000000")
	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, `"0:0"`, `{"priority":9}`)
	s.Require().Equal(http.StatusNotFound, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestNonAdminGets403() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	resp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.userToken, "", "")
	s.Require().Equal(http.StatusForbidden, resp.status)

	resp = s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.userToken, etag, `{"priority":9}`)
	s.Require().Equal(http.StatusForbidden, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestConcurrentPatchWithSameETag() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())

	type result struct {
		status int
		etag   string
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPatch, s.env.server.URL+s.orgPoolPath(),
				strings.NewReader(fmt.Sprintf(`{"priority":%d}`, 100+i)))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+s.env.adminToken)
			req.Header.Set("If-Match", etag)
			resp, err := s.env.server.Client().Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			results <- result{status: resp.StatusCode, etag: resp.Header.Get("ETag")}
		}(i)
	}
	wg.Wait()
	close(results)

	var statuses []int
	var winnerETag string
	for res := range results {
		s.Require().NoError(res.err)
		statuses = append(statuses, res.status)
		if res.status == http.StatusOK {
			winnerETag = res.etag
		}
	}
	s.Require().ElementsMatch([]int{http.StatusOK, http.StatusPreconditionFailed}, statuses)

	// the row reflects exactly one write, with a fresh etag
	getResp := s.env.do(s.T(), http.MethodGet, s.orgPoolPath(), s.env.adminToken, "", "")
	s.Require().Equal(winnerETag, getResp.etag)
}

func (s *OfflinePoolSettingsHTTPSuite) TestStockPutBumpsRevision() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	// legacy writer through the store layer (the path used by
	// PUT /api/v1/pools/{poolID} and the nested entity pool PUTs)
	entity, err := s.env.org.GetEntity()
	s.Require().NoError(err)
	priority := uint(33)
	_, err = s.env.db.UpdateEntityPool(s.T().Context(), entity, s.env.orgPool.ID, params.UpdatePoolParams{
		Priority: &priority,
	})
	s.Require().NoError(err)

	// the experimental API now exposes a new ETag...
	newETag := s.env.getETag(s.T(), path)
	s.Require().NotEqual(etag, newETag)
	s.Require().Equal(etagSeq(s.T(), etag)+1, etagSeq(s.T(), newETag))

	// ... and a client still holding the old ETag loses the race
	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":44}`)
	s.Require().Equal(http.StatusPreconditionFailed, resp.status)
	s.Require().Equal(newETag, resp.etag)

	// the fresh ETag patches fine
	resp = s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, newETag, `{"priority":44}`)
	s.Require().Equal(http.StatusOK, resp.status)
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchAdvancesSeqExactlyOnce() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`)
	s.Require().Equal(http.StatusOK, resp.status)

	// the response, a subsequent GET and the revision row all agree on
	// exactly one increment
	s.Require().Equal(etagSeq(s.T(), etag)+1, etagSeq(s.T(), resp.etag))
	s.Require().Equal(resp.etag, s.env.getETag(s.T(), path))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchIdempotentReplayAfterClientRestart() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)
	body := `{"priority":9}`

	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, body, "key-replay")
	s.Require().Equal(http.StatusOK, resp.status, "body: %s", resp.body)
	s.Require().NotEqual(etag, resp.etag)

	// simulated client restart: byte-identical retry, stale If-Match and
	// all. The stored receipt answers it; nothing is re-applied.
	replay := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, body, "key-replay")
	s.Require().Equal(http.StatusOK, replay.status, "body: %s", replay.body)
	s.Require().Equal(resp.etag, replay.etag)

	var replayDoc params.OfflinePoolSettingsRequestReplay
	s.Require().NoError(json.Unmarshal(replay.body, &replayDoc))
	s.Require().Equal(params.OfflinePoolSettingsOutcomeAppliedChanged, replayDoc.Outcome)
	s.Require().Equal(resp.etag, replayDoc.ResultingETag)

	// zero additional effect: the resource still carries the original etag
	s.Require().Equal(resp.etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchIdempotencyKeyReuseConflict() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, "key-reuse")
	s.Require().Equal(http.StatusOK, resp.status, "body: %s", resp.body)

	// same key, different payload -> 409 and no state change
	conflict := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":10}`, "key-reuse")
	s.Require().Equal(http.StatusConflict, conflict.status)
	s.Require().Contains(string(conflict.body), "Idempotency-Key")
	s.Require().Equal(resp.etag, s.env.getETag(s.T(), path))

	// the original receipt is intact: the original retry still replays
	replay := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, "key-reuse")
	s.Require().Equal(http.StatusOK, replay.status)
	s.Require().Equal(resp.etag, replay.etag)
	s.Require().Equal(resp.etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchIdempotencyKeyReuseAcrossPools() {
	etag := s.env.getETag(s.T(), s.orgPoolPath())
	body := `{"priority":9}`

	resp := s.env.do(s.T(), http.MethodPatch, s.orgPoolPath(), s.env.adminToken, etag, body, "key-scope")
	s.Require().Equal(http.StatusOK, resp.status, "body: %s", resp.body)

	// same key and payload, different pool (scope)
	repoPath := offlineSettingsPath("repository", s.env.repo.ID, s.env.repoPool.ID)
	conflict := s.env.do(s.T(), http.MethodPatch, repoPath, s.env.adminToken, s.env.getETag(s.T(), repoPath), body, "key-scope")
	s.Require().Equal(http.StatusConflict, conflict.status)
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchRejectedStaleReceiptReplay() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	// consume the etag (no key)
	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":8}`)
	s.Require().Equal(http.StatusOK, resp.status)

	// stale attempt with its own key
	stale := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, "key-stale")
	s.Require().Equal(http.StatusPreconditionFailed, stale.status)
	s.Require().Equal(resp.etag, stale.etag)

	// replaying the stale request replays the 412 with the observed etag
	replay := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, "key-stale")
	s.Require().Equal(http.StatusPreconditionFailed, replay.status)
	s.Require().Equal(resp.etag, replay.etag)
	s.Require().Contains(string(replay.body), strings.Trim(resp.etag, `"`))

	// still zero additional effect (the keyless first PATCH also persisted
	// a durable, unaddressable receipt with a generated id; the keyed
	// receipt is exactly one row)
	s.Require().Equal(resp.etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(1), s.env.receiptCountFor(s.T(), "key-stale"))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchRejectedPolicyReceiptReplay() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"min_idle_runners":6}`, "key-policy")
	s.Require().Equal(http.StatusUnprocessableEntity, resp.status)
	s.Require().Equal(etag, s.env.getETag(s.T(), path))

	replay := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"min_idle_runners":6}`, "key-policy")
	s.Require().Equal(http.StatusUnprocessableEntity, replay.status)
	s.Require().Equal(etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchRejectedMalformedReceiptReplay() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"bogus":1}`, "key-malformed")
	s.Require().Equal(http.StatusBadRequest, resp.status)

	// the detailed decoding error is preserved on the fresh rejection
	s.Require().Contains(string(resp.body), "unknown field")

	replay := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"bogus":1}`, "key-malformed")
	s.Require().Equal(http.StatusBadRequest, replay.status)
	s.Require().Equal(etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

// TestPatchNoReceiptsBeforeWriteTransaction verifies that 403/404/428
// rejections never persist a receipt: the database must stay empty, and a
// retried 428 request with a now-valid precondition must be processed
// fresh (no replay).
func (s *OfflinePoolSettingsHTTPSuite) TestPatchNoReceiptsBeforeWriteTransaction() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)
	s.Require().Equal(int64(0), s.env.receiptCount(s.T()))

	// 403: wrong entity
	wrongPath := offlineSettingsPath("organization", s.env.org2.ID, s.env.orgPool.ID)
	resp := s.env.do(s.T(), http.MethodPatch, wrongPath, s.env.adminToken, etag, `{"priority":9}`, "key-403")
	s.Require().Equal(http.StatusForbidden, resp.status)

	// 403: non-admin
	resp = s.env.do(s.T(), http.MethodPatch, path, s.env.userToken, etag, `{"priority":9}`, "key-403b")
	s.Require().Equal(http.StatusForbidden, resp.status)

	// 404: nonexistent pool
	missingPath := offlineSettingsPath("organization", s.env.org.ID, "00000000-0000-0000-0000-000000000000")
	resp = s.env.do(s.T(), http.MethodPatch, missingPath, s.env.adminToken, etag, `{"priority":9}`, "key-404")
	s.Require().Equal(http.StatusNotFound, resp.status)

	// 428: missing If-Match
	resp = s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, "", `{"priority":9}`, "key-428")
	s.Require().Equal(http.StatusPreconditionRequired, resp.status)

	s.Require().Equal(int64(0), s.env.receiptCount(s.T()))

	// the 428'd request retried with a valid precondition is fresh work
	resp = s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, "key-428")
	s.Require().Equal(http.StatusOK, resp.status)
	s.Require().Equal(int64(1), s.env.receiptCount(s.T()))
}

func (s *OfflinePoolSettingsHTTPSuite) TestPatchMalformedIdempotencyKey() {
	path := s.orgPoolPath()
	etag := s.env.getETag(s.T(), path)

	for _, key := range []string{
		strings.Repeat("a", 129), // too long
		"key with spaces",        // whitespace
		"key\twith\ttabs",        // control characters
		"schlüssel",              // non-ASCII
	} {
		resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, key)
		s.Require().Equal(http.StatusBadRequest, resp.status, "key: %q", key)
	}
	s.Require().Equal(etag, s.env.getETag(s.T(), path))
	s.Require().Equal(int64(0), s.env.receiptCount(s.T()))

	// boundary: exactly 128 chars is accepted
	resp := s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, etag, `{"priority":9}`, strings.Repeat("b", 128))
	s.Require().Equal(http.StatusOK, resp.status)
}

// TestGetIgnoresIdempotencyKey documents that GET does not participate in
// idempotency at all: the header is ignored and no receipt is written.
func (s *OfflinePoolSettingsHTTPSuite) TestGetIgnoresIdempotencyKey() {
	path := s.orgPoolPath()
	resp := s.env.do(s.T(), http.MethodGet, path, s.env.adminToken, "", "", "key-get")
	s.Require().Equal(http.StatusOK, resp.status)
	s.Require().Equal(int64(0), s.env.receiptCount(s.T()))
}

func TestOfflinePoolSettingsHTTPSuite(t *testing.T) {
	suite.Run(t, new(OfflinePoolSettingsHTTPSuite))
}

type OfflinePoolSettingsFlagOffSuite struct {
	suite.Suite
	env *offlinePoolSettingsEnv
}

func (s *OfflinePoolSettingsFlagOffSuite) SetupTest() {
	s.env = setupOfflinePoolSettingsEnv(s.T(), false)
}

func (s *OfflinePoolSettingsFlagOffSuite) TestRoutesAreNotFound() {
	path := offlineSettingsPath("organization", s.env.org.ID, s.env.orgPool.ID)

	// GET falls through to the normal not-found handler.
	resp := s.env.do(s.T(), http.MethodGet, path, s.env.adminToken, "", "")
	s.Require().Equal(http.StatusNotFound, resp.status)

	// PATCH against any unknown path is answered with 405 by the stock
	// router (the catch-all not-found route does not list PATCH), which is
	// exactly how a nonexistent route behaves with the flag off.
	resp = s.env.do(s.T(), http.MethodPatch, path, s.env.adminToken, `"0:0"`, `{"priority":9}`)
	s.Require().Equal(http.StatusMethodNotAllowed, resp.status)
}

func (s *OfflinePoolSettingsFlagOffSuite) TestNoSchemaSideEffects() {
	// ask the database directly; the experimental tables must not exist
	checkDB, err := gorm.Open(sqlite.Open(s.env.dbCfg.SQLite.DBFile), &gorm.Config{})
	s.Require().NoError(err)
	sqlDB, err := checkDB.DB()
	s.Require().NoError(err)
	defer sqlDB.Close()

	var count int64
	s.Require().NoError(checkDB.Raw(
		"SELECT count(1) FROM sqlite_master WHERE type='table' AND name IN ('offline_pool_settings_revisions', 'offline_pool_settings_request_receipts')").
		Scan(&count).Error)
	s.Require().Zero(count)
}

func TestOfflinePoolSettingsFlagOffSuite(t *testing.T) {
	suite.Run(t, new(OfflinePoolSettingsFlagOffSuite))
}
