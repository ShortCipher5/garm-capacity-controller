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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
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

// dropResponseWriter simulates a transport failure that happens after the
// handler's database transaction committed: the first attempt to write the
// response panics, standing in for a connection drop or a crash between
// commit and response. The durable state (settings change + request
// receipt) survives; only the response is lost.
type dropResponseWriter struct {
	header http.Header
}

func (w *dropResponseWriter) Header() http.Header { return w.header }

func (w *dropResponseWriter) Write([]byte) (int, error) {
	panic("response dropped after commit")
}

func (w *dropResponseWriter) WriteHeader(int) {
	panic("response dropped after commit")
}

type dropResponseEnv struct {
	db         dbCommon.Store
	dbCfg      config.Database
	controller *controllers.APIController
	adminCtx   context.Context
	org        params.Organization
	pool       params.Pool
}

// setupDropResponseEnv builds a minimal full stack (sqlite database,
// runner, controller) for direct handler invocation. Mirrors
// setupOfflinePoolSettingsEnv, but keeps the pieces a drop-response test
// needs and returns the controller.
func setupDropResponseEnv(t *testing.T) *dropResponseEnv {
	t.Helper()

	ctx := context.Background()
	watcher.InitWatcher(ctx)
	t.Cleanup(func() { watcher.CloseWatcher() })

	dbCfg := garmTesting.GetTestSqliteDBConfig(t)
	db, err := database.NewDatabase(ctx, dbCfg)
	require.NoError(t, err)
	_, err = db.InitController()
	require.NoError(t, err)

	adminCtx := garmTesting.ImpersonateAdminContext(ctx, db, t)
	metadataURL := "https://garm.example.com/api/v1/metadata"
	callbackURL := "https://garm.example.com/api/v1/callbacks"
	webhookURL := "https://garm.example.com/webhooks"
	_, err = db.UpdateController(params.UpdateControllerParams{
		MetadataURL: &metadataURL,
		CallbackURL: &callbackURL,
		WebhookURL:  &webhookURL,
	})
	require.NoError(t, err)

	githubEndpoint := garmTesting.CreateDefaultGithubEndpoint(adminCtx, db, t)
	creds := garmTesting.CreateTestGithubCredentials(adminCtx, "new-creds", db, t, githubEndpoint)

	jwtCfg := config.JWTAuth{
		Secret:     testJWTSecret,
		TimeToLive: "1h",
	}
	authenticator := auth.NewAuthenticator(jwtCfg, db)
	tokenGetter, err := auth.NewInstanceTokenGetter(jwtCfg.Secret)
	require.NoError(t, err)
	// Create the runner before any entity exists, so no pool managers are
	// started (same rationale as setupOfflinePoolSettingsEnv).
	testRunner, err := runner.NewRunner(ctx, config.Config{}, db, tokenGetter)
	require.NoError(t, err)

	org, err := db.CreateOrganization(adminCtx, "drop-org", creds, "webhook-secret", params.PoolBalancerTypeRoundRobin, false)
	require.NoError(t, err)
	orgEntity, err := org.GetEntity()
	require.NoError(t, err)
	pool, err := db.CreateEntityPool(adminCtx, orgEntity, params.CreatePoolParams{
		ProviderName:   "test-provider",
		MaxRunners:     5,
		MinIdleRunners: 2,
		Image:          "test-image",
		Flavor:         "test-flavor",
		OSType:         "linux",
		Tags:           []string{"amd64-linux-runner"},
	})
	require.NoError(t, err)

	controller, err := controllers.NewAPIController(testRunner, authenticator, nil, nil, nil, config.APIServer{})
	require.NoError(t, err)

	return &dropResponseEnv{db: db, dbCfg: dbCfg, controller: controller, adminCtx: adminCtx, org: org, pool: pool}
}

func (e *dropResponseEnv) pathVars() map[string]string {
	return map[string]string{
		"entityType": "organization",
		"entityID":   e.org.ID,
		"poolID":     e.pool.ID,
	}
}

func (e *dropResponseEnv) doGet(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, offlineSettingsPath("organization", e.org.ID, e.pool.ID), nil)
	req = req.WithContext(e.adminCtx)
	req = mux.SetURLVars(req, e.pathVars())
	rec := httptest.NewRecorder()
	e.controller.GetOfflinePoolSettingsHandler(rec, req)
	return rec
}

// doPatch invokes the PATCH handler directly. The recorder is supplied by
// the caller so the test can substitute a dropResponseWriter.
func (e *dropResponseEnv) doPatch(t *testing.T, w http.ResponseWriter, body, ifMatch, idemKey string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, offlineSettingsPath("organization", e.org.ID, e.pool.ID), strings.NewReader(body))
	req = req.WithContext(e.adminCtx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", ifMatch)
	req.Header.Set("Idempotency-Key", idemKey)
	req = mux.SetURLVars(req, e.pathVars())
	e.controller.UpdateOfflinePoolSettingsHandler(w, req)
}

// receiptCount reads how many receipts exist for one request id straight
// from the database file, bypassing the store under test.
func (e *dropResponseEnv) receiptCount(t *testing.T, requestID string) int64 {
	t.Helper()
	checkDB, err := gorm.Open(sqlite.Open(e.dbCfg.SQLite.DBFile), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := checkDB.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	var count int64
	require.NoError(t, checkDB.Raw("SELECT count(1) FROM offline_pool_settings_request_receipts WHERE request_id = ?", requestID).
		Scan(&count).Error)
	return count
}

// receiptFor reads the durable request receipt straight from the database
// file, bypassing the store under test.
func (e *dropResponseEnv) receiptFor(t *testing.T, requestID string) (outcome, resultingETag string, found bool) {
	t.Helper()
	checkDB, err := gorm.Open(sqlite.Open(e.dbCfg.SQLite.DBFile), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := checkDB.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	row := checkDB.Raw("SELECT outcome, resulting_etag FROM offline_pool_settings_request_receipts WHERE request_id = ?", requestID).Row()
	var etag *string
	err = row.Scan(&outcome, &etag)
	if err != nil {
		return "", "", false
	}
	if etag != nil {
		resultingETag = *etag
	}
	return outcome, resultingETag, true
}

// TestOfflinePoolSettingsCommitThenDroppedResponse drives the failure the
// durable receipts exist for: a keyed PATCH commits its settings change
// and its receipt, but the response dies before reaching the client. The
// client retry must be answered from the receipt — 200 with the stored
// resulting_etag and the replay envelope — and further retries must not
// advance the revision or write another receipt.
func TestOfflinePoolSettingsCommitThenDroppedResponse(t *testing.T) {
	env := setupDropResponseEnv(t)

	getRec := env.doGet(t)
	require.Equal(t, http.StatusOK, getRec.Code)
	etag := getRec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	const idemKey = "drop-response-key-1"

	// the PATCH commits, then the response is dropped mid-write
	dropped := &dropResponseWriter{header: http.Header{}}
	require.Panics(t, func() {
		env.doPatch(t, dropped, `{"priority":7}`, etag, idemKey)
	})

	// the commit survived the drop: the settings changed and the receipt
	// is durable, even though no response was ever produced
	outcome, resultingETag, found := env.receiptFor(t, idemKey)
	require.True(t, found, "receipt must be durable across a dropped response")
	require.Equal(t, string(params.OfflinePoolSettingsOutcomeAppliedChanged), outcome)
	require.NotEmpty(t, resultingETag)
	require.NotEqual(t, etag, resultingETag, "a changed patch must advance the revision")

	// the client retry (same key, same payload; the precondition is stale
	// now, which must not matter) is answered from the stored receipt
	retryRec := httptest.NewRecorder()
	env.doPatch(t, retryRec, `{"priority":7}`, etag, idemKey)
	require.Equal(t, http.StatusOK, retryRec.Code)
	require.Equal(t, resultingETag, retryRec.Header().Get("ETag"))
	var replay params.OfflinePoolSettingsRequestReplay
	require.NoError(t, json.Unmarshal(retryRec.Body.Bytes(), &replay))
	require.Equal(t, params.OfflinePoolSettingsOutcomeAppliedChanged, replay.Outcome)
	require.Equal(t, resultingETag, replay.ResultingETag)

	// idempotent: the revision did not advance again and no second receipt
	// exists
	afterRetry := env.doGet(t)
	require.Equal(t, http.StatusOK, afterRetry.Code)
	require.Equal(t, resultingETag, afterRetry.Header().Get("ETag"))
	require.Equal(t, int64(1), env.receiptCount(t, idemKey))

	// a further replay keeps returning the same envelope without moving
	// the revision
	secondRetry := httptest.NewRecorder()
	env.doPatch(t, secondRetry, `{"priority":7}`, etag, idemKey)
	require.Equal(t, http.StatusOK, secondRetry.Code)
	require.Equal(t, resultingETag, secondRetry.Header().Get("ETag"))
	var secondReplay params.OfflinePoolSettingsRequestReplay
	require.NoError(t, json.Unmarshal(secondRetry.Body.Bytes(), &secondReplay))
	require.Equal(t, replay, secondReplay)
	require.Equal(t, resultingETag, env.doGet(t).Header().Get("ETag"))
}
