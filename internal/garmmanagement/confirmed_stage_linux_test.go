package garmmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func createConfirmedStage(t *testing.T) (*Manager, *Journal, *garmapi.ManagementClient, *stageFixture, StagedPool) {
	t.Helper()
	m, j, c, f, _ := stageSetup(t)
	result, err := m.CreateDisabledPool(context.Background(), requestID, repoID, stageExpected(t, c), nativeStageSpec(), "confirmed source lookup")
	if err != nil {
		t.Fatal(err)
	}
	return m, j, c, f, result
}
func TestConfirmedDisabledPoolDerivesTargetFromJournalAndFreshSourceWithoutMutation(t *testing.T) {
	m, j, _, f, stage := createConfirmedStage(t)
	before, err := j.Audit(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	facts, err := m.ConfirmedDisabledPool(context.Background(), requestID)
	if err != nil || facts.RequestID != requestID || facts.PoolID != stage.PoolID || facts.RepositoryID != repoID || facts.ManagementIdentity != stage.ManagementIdentity || facts.JournalLineage != j.Lineage() || facts.SpecFingerprint != stage.SpecFingerprint || facts.PoolFingerprint != stage.PoolFingerprint || facts.Generation != stage.Generation || facts.ForgeEndpointName != "disposable-forge" || facts.Image != "linux" || facts.Flavor != "small" || facts.OSType != "linux" || facts.OSArch != "amd64" || facts.MaxRunners != 2 || facts.Enabled || facts.MinIdleRunners != 0 || facts.TemplateID != 0 || facts.ShellEnabled || !facts.ExtraSpecsEmpty || facts.ObservedAt.Before(start) || facts.ObservedAt.After(time.Now()) {
		t.Fatalf("invalid facts %+v %v", facts, err)
	}
	after, err := j.Audit(context.Background(), 0, 100)
	if err != nil || len(after) != len(before) || f.posts != 1 || f.gets != 2 {
		t.Fatal("lookup mutated/replayed instead of reading exact UUID")
	}
	// There is no caller pool/spec/result argument from which to invent authority.
	if _, err := m.ConfirmedDisabledPool(context.Background(), "https://not-approved.invalid"); !errors.Is(err, ErrJournal) {
		t.Fatal("arbitrary source reference accepted")
	}
	if f.gets != 2 {
		t.Fatal("invalid request contacted source")
	}
}
func TestConfirmedDisabledPoolRejectsNonconfirmedAndGlobalUnresolvedBarrier(t *testing.T) {
	m, j, _, f, _ := createConfirmedStage(t)
	gets := f.gets
	if _, err := j.begin(context.Background(), Intent{RequestID: nextID, Kind: "repository", Target: repoID, Reason: "other pending mutation", Before: encoded(struct{}{}), Desired: encoded(struct{}{})}, fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrBlocked) || f.gets != gets {
		t.Fatal("global unresolved slot ignored")
	}
	m2, _, c2, f2, _ := stageSetup(t)
	f2.postStatus = 500
	if _, err := m2.CreateDisabledPool(context.Background(), requestID, repoID, stageExpected(t, c2), nativeStageSpec(), "lost creation acknowledgement"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if _, err := m2.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrBlocked) || f2.gets != 0 {
		t.Fatal("GET similarity used to replace unknown POST")
	}
}
func TestConfirmedDisabledPoolChecksGlobalBarrierAgainAfterSourceRead(t *testing.T) {
	m, j, _, f, _ := createConfirmedStage(t)
	f.beforeGet = func() {
		if _, err := j.begin(context.Background(), Intent{RequestID: nextID, Kind: "repository", Target: repoID, Reason: "cross-process mutation during GET", Before: encoded(struct{}{}), Desired: encoded(struct{}{})}, fingerprint, fingerprint); err != nil {
			t.Error(err)
		}
	}
	if _, err := m.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrBlocked) {
		t.Fatal("mid-read unresolved writer escaped barrier", err)
	}
	if f.posts != 1 {
		t.Fatal("source lookup retried creation")
	}
}
func TestConfirmedDisabledPoolRejectsSourceDriftWithoutTrustingStoredBoolean(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){"enabled": func(pool map[string]any) { pool["enabled"] = true }, "generation": func(pool map[string]any) { pool["generation"] = 2 }, "forge-alias": func(pool map[string]any) { pool["endpoint"] = map[string]any{"name": "other-forge"} }, "flavor": func(pool map[string]any) { pool["flavor"] = "different" }} {
		t.Run(name, func(t *testing.T) {
			m, _, _, f, _ := createConfirmedStage(t)
			f.observationEdit = edit
			if _, err := m.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrStagedPoolObservation) {
				t.Fatal("drift became trusted registration fact", err)
			}
			if f.posts != 1 {
				t.Fatal("drift caused another POST")
			}
		})
	}
}
func TestConfirmedDisabledPoolValidatesCanonicalOperationHashAndRecordedIdentity(t *testing.T) {
	for name, sql := range map[string]string{
		"wrong-operation":       `UPDATE intents SET desired_json=replace(CAST(desired_json AS TEXT),'create_disabled_pool','other_operation')`,
		"unknown-desired-field": `UPDATE intents SET desired_json=substr(CAST(desired_json AS TEXT),1,length(desired_json)-1)||',"unexpected":true}'`,
		"wrong-hash":            `UPDATE intents SET request_hash='` + strings.Repeat("f", 64) + `'`,
		"wrong-target":          `UPDATE intents SET target='` + nextID + `'`,
		"wrong-request":         `UPDATE intents SET result_json=replace(CAST(result_json AS TEXT),'` + requestID + `','` + nextID + `')`,
		"wrong-protocol":        `UPDATE intents SET result_json=replace(CAST(result_json AS TEXT),'native-disabled-repository-pool-v1','wrong-protocol')`,
		"wrong-lineage":         `UPDATE intents SET result_json=json_set(CAST(result_json AS TEXT),'$.journal_lineage','` + strings.Repeat("f", 64) + `')`,
		"unknown-result-field":  `UPDATE intents SET result_json=substr(CAST(result_json AS TEXT),1,length(result_json)-1)||',"unexpected":true}'`,
		"activation-spoof":      `UPDATE intents SET result_json=replace(CAST(result_json AS TEXT),'"activation_allowed":false','"activation_allowed":true')`,
	} {
		t.Run(name, func(t *testing.T) {
			m, j, _, f, _ := createConfirmedStage(t)
			if strings.HasPrefix(sql, "UPDATE intents SET desired_json=") || strings.HasPrefix(sql, "UPDATE intents SET result_json=") {
				left, right, _ := strings.Cut(sql, "=")
				sql = left + "=CAST(" + right + " AS BLOB)"
			}
			if _, err := j.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			gets := f.gets
			if _, err := m.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrJournal) || f.gets != gets {
				t.Fatal("malformed journal authority contacted source or succeeded", err)
			}
		})
	}
}
func TestConfirmedDisabledPoolDoesNotEchoUntrustedObserverError(t *testing.T) {
	m, _, client, _, _ := createConfirmedStage(t)
	m.remote = secretStageObserver{client}
	if _, err := m.ConfirmedDisabledPool(context.Background(), requestID); !errors.Is(err, ErrStagedPoolObservation) || strings.Contains(err.Error(), "private") {
		t.Fatal("observer failure leaked details", err)
	}
}

type secretStageObserver struct{ *garmapi.ManagementClient }

func (secretStageObserver) ObserveDisabledRepoPool(context.Context, string, string, garmapi.DisabledPoolSpec) (garmapi.DisabledPoolObservation, error) {
	return garmapi.DisabledPoolObservation{}, errors.New("private endpoint credential error")
}
func TestConfirmedDisabledPoolResultRemainsRestricted(t *testing.T) {
	m, _, _, _, _ := createConfirmedStage(t)
	facts, err := m.ConfirmedDisabledPool(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(facts)
	for _, private := range []string{"private-stage-fixture-token", "https://", "certificate", "api_base_url"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("raw source/credential exposed")
		}
	}
}

func TestConfirmedDisabledPoolPreservesConservativeReadStartAcrossDelayedSource(t *testing.T) {
	m, _, _, fixture, _ := createConfirmedStage(t)
	snapshot := make(chan time.Time, 1)
	release := make(chan struct{})
	fixture.beforeGet = func() { snapshot <- time.Now().UTC(); <-release }
	type outcome struct {
		value DisabledPoolFacts
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := m.ConfirmedDisabledPool(context.Background(), requestID)
		done <- outcome{value, err}
	}()
	var snapshotAt time.Time
	select {
	case snapshotAt = <-snapshot:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("source read did not enter")
	}
	time.Sleep(30 * time.Millisecond)
	close(release)
	select {
	case result := <-done:
		if result.err != nil || result.value.ObservedAt.After(snapshotAt) {
			t.Fatalf("source time refreshed after delayed response/journal recheck: read=%v source=%v err=%v", result.value.ObservedAt, snapshotAt, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed source lookup stuck")
	}
}
