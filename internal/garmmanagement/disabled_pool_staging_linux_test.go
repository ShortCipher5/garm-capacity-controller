package garmmanagement

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ShortCipher5/garm-capacity-controller/internal/garmapi"
)

func fixedStageSetup(t *testing.T) (*DisabledPoolStaging, *Manager, *Journal, *stageFixture, DisabledPoolSelection) {
	t.Helper()
	m, j, _, f, _ := stageSetup(t)
	templates := []DisabledPoolTemplate{{Reference: "trusted/linux", RepositoryID: repoID, Spec: nativeStageSpec()}, {Reference: "trusted/same-spec", RepositoryID: repoID, Spec: nativeStageSpec()}}
	s, e := NewDisabledPoolStaging(m, templates)
	if e != nil {
		t.Fatal(e)
	}
	templates[0].Spec.Tags[0] = "mutated caller config"
	if s.Templates()[0].Spec.Tags[0] != "linux" {
		t.Fatal("constructor borrowed caller template")
	}
	returned := s.Templates()
	returned[0].Spec.Tags[0] = "mutated returned catalog"
	if s.Templates()[0].Spec.Tags[0] != "linux" {
		t.Fatal("catalog getter borrowed service state")
	}
	return s, m, j, f, DisabledPoolSelection{RequestID: requestID, TemplateReference: "trusted/linux", Reason: "explicit offline staging"}
}
func stagePosts(f *stageFixture) int { f.mu.Lock(); defer f.mu.Unlock(); return f.posts }
func TestFixedDisabledPoolStagingReviewCreateAndRecordedBinding(t *testing.T) {
	s, m, j, f, selection := fixedStageSetup(t)
	ctx := context.Background()
	plan, e := s.ReviewCreate(ctx, selection)
	if e != nil || plan.Validate() != nil {
		t.Fatal("actual read-only review", e)
	}
	var rows int
	if j.db.QueryRow("SELECT count(*) FROM intents").Scan(&rows) != nil || rows != 0 || stagePosts(f) != 0 {
		t.Fatal("review persisted intent or created pool")
	}
	cloned := plan.Clone()
	cloned.Spec.Tags[0] = "mutable copy"
	if plan.Spec.Tags[0] != "linux" {
		t.Fatal("plan clone aliases")
	}
	selection.ExpectedPlanDigest = plan.PlanDigest
	result, e := s.Create(ctx, selection)
	if e != nil || result.PoolID != poolID || !result.DisabledReadback || !result.RouteRequired || result.RouteInstalled || result.ActivationAllowed || stagePosts(f) != 1 {
		t.Fatal("fixed disabled-only creation", result, e)
	}
	recorded, e := m.RecordedDisabledPoolPlan(ctx, requestID)
	if e != nil || !reflect.DeepEqual(recorded, plan) {
		t.Fatal("actual persisted template plan", recorded, e)
	}
	// Identical replay is the original durable library result, never another POST.
	replay, e := s.Create(ctx, selection)
	if e != nil || replay != result || stagePosts(f) != 1 {
		t.Fatal("confirmed replay reissued command", e)
	}
	other := selection
	other.TemplateReference = "trusted/same-spec"
	other.ExpectedPlanDigest = ""
	otherPlan, e := s.ReviewCreate(ctx, other)
	if e != nil || otherPlan.PlanDigest == plan.PlanDigest {
		t.Fatal("template reference not human-bound", e)
	}
	other.ExpectedPlanDigest = otherPlan.PlanDigest
	if _, e := s.Create(ctx, other); !errors.Is(e, ErrRequestConflict) || stagePosts(f) != 1 {
		t.Fatal("same spec rebound request ID to new template", e)
	}
	unchanged, e := m.RecordedDisabledPoolPlan(ctx, requestID)
	if e != nil || !reflect.DeepEqual(unchanged, plan) {
		t.Fatal("rebind changed stored provenance", e)
	}
}
func TestFixedDisabledPoolStagingUnknownNeverRepostsOrClaimsBinding(t *testing.T) {
	s, m, _, f, selection := fixedStageSetup(t)
	ctx := context.Background()
	plan, e := s.ReviewCreate(ctx, selection)
	if e != nil {
		t.Fatal(e)
	}
	selection.ExpectedPlanDigest = plan.PlanDigest
	f.mu.Lock()
	f.postStatus = 503
	f.mu.Unlock()
	if _, e := s.Create(ctx, selection); !errors.Is(e, ErrOutcomeUnknown) || stagePosts(f) != 1 {
		t.Fatal("uncertain create not stopped", e)
	}
	if _, e := s.Create(ctx, selection); !errors.Is(e, ErrBlocked) || stagePosts(f) != 1 {
		t.Fatal("unknown retried", e)
	}
	if _, e := m.RecordedDisabledPoolPlan(ctx, requestID); !errors.Is(e, ErrBlocked) {
		t.Fatal("unknown became confirmed template binding", e)
	}
	status, e := m.Status(ctx)
	if e != nil || !status.WriteBlocked || len(status.Pending) != 1 {
		t.Fatal("global stop lost", e)
	}
}
func TestFixedDisabledPoolStagingSelectorsAndReviewCAS(t *testing.T) {
	for _, name := range []string{"missing digest", "changed digest", "unknown template", "zero request", "changed reason", "borrowed request"} {
		t.Run(name, func(t *testing.T) {
			s, _, j, f, selection := fixedStageSetup(t)
			p, e := s.ReviewCreate(context.Background(), selection)
			if e != nil {
				t.Fatal(e)
			}
			selection.ExpectedPlanDigest = p.PlanDigest
			switch name {
			case "missing digest":
				selection.ExpectedPlanDigest = ""
			case "changed digest":
				selection.ExpectedPlanDigest = strings.Repeat("d", 64)
			case "unknown template":
				selection.TemplateReference = "caller/arbitrary"
			case "zero request":
				selection.RequestID = "00000000-0000-0000-0000-000000000000"
			case "changed reason":
				selection.Reason = "different human purpose"
			case "borrowed request":
				selection.RequestID = nextID
			}
			if _, e := s.Create(context.Background(), selection); !errors.Is(e, ErrDisabledPoolSelection) || stagePosts(f) != 0 {
				t.Fatal("changed/missing human tuple created pool", e)
			}
			var count int
			if j.db.QueryRow("SELECT count(*) FROM intents").Scan(&count) != nil || count != 0 {
				t.Fatal("rejected human tuple persisted an intent")
			}
		})
	}
	s, _, _, f, selection := fixedStageSetup(t)
	copied := *s
	if _, e := copied.ReviewCreate(context.Background(), selection); e == nil || stagePosts(f) != 0 {
		t.Fatal("copied service accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.ReviewCreate(ctx, selection); e == nil {
		t.Fatal("cancelled review accepted")
	}
}
func TestFixedDisabledPoolStagingLegacyBytesAndNoBindingAdoption(t *testing.T) {
	spec := nativeStageSpec()
	old := disabledPoolIntent{Operation: "create_disabled_pool", Protocol: DisabledPoolProtocol, Spec: spec}
	raw, e := json.Marshal(old)
	if e != nil {
		t.Fatal(e)
	}
	want := `{"operation":"create_disabled_pool","protocol":"native-disabled-repository-pool-v1","spec":{"provider_name":"capacity","max_runners":2,"image":"linux","flavor":"small","os_arch":"amd64","tags":["linux"]}}`
	if string(raw) != want || bytes.Contains(raw, []byte("template_binding")) {
		t.Fatal("legacy nil-binding bytes changed", string(raw))
	}
	h, e := requestHash(requestID, repoID, strings.Repeat("a", 64), "repository", "legacy golden", old)
	if e != nil || h != "de9e31cadc7666aa16314830eef3f1039ceba0ebe694fd4d6b4337a6609e373f" {
		t.Fatal("actual c2 baseline request-hash golden changed", h, e)
	}
	s, m, _, f, selection := fixedStageSetup(t)
	ctx := context.Background()
	p, e := s.ReviewCreate(ctx, selection)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := m.CreateDisabledPool(ctx, requestID, repoID, p.RepositoryFingerprint, p.Spec, p.Reason); e != nil {
		t.Fatal("original public library", e)
	}
	if _, e := m.RecordedDisabledPoolPlan(ctx, requestID); !errors.Is(e, ErrDisabledPoolSelection) {
		t.Fatal("legacy spec inferred template binding", e)
	}
	selection.ExpectedPlanDigest = p.PlanDigest
	if _, e := s.Create(ctx, selection); !errors.Is(e, ErrRequestConflict) || stagePosts(f) != 1 {
		t.Fatal("bound service adopted legacy nil request", e)
	}
}
func TestFixedDisabledPoolStagingRejectsBadCatalog(t *testing.T) {
	m, _, _, _, _ := stageSetup(t)
	for _, templates := range [][]DisabledPoolTemplate{nil, {{Reference: "bad", RepositoryID: repoID, Spec: garmapi.DisabledPoolSpec{}}}, {{Reference: "duplicate", RepositoryID: repoID, Spec: nativeStageSpec()}, {Reference: "duplicate", RepositoryID: repoID, Spec: nativeStageSpec()}}} {
		if _, e := NewDisabledPoolStaging(m, templates); e == nil {
			t.Fatal("invalid protected catalog accepted")
		}
	}
}

func TestFixedDisabledPoolStagingChangedTemplateRefusesOldPlan(t *testing.T) {
	s, m, j, f, selection := fixedStageSetup(t)
	p, e := s.ReviewCreate(context.Background(), selection)
	if e != nil {
		t.Fatal(e)
	}
	changed := nativeStageSpec()
	changed.Image = "different-trusted-image"
	newer, e := NewDisabledPoolStaging(m, []DisabledPoolTemplate{{Reference: selection.TemplateReference, RepositoryID: repoID, Spec: changed}})
	if e != nil {
		t.Fatal(e)
	}
	selection.ExpectedPlanDigest = p.PlanDigest
	if _, e := newer.Create(context.Background(), selection); !errors.Is(e, ErrDisabledPoolSelection) || stagePosts(f) != 0 {
		t.Fatal("changed protected spec accepted old review", e)
	}
	var count int
	if j.db.QueryRow("SELECT count(*) FROM intents").Scan(&count) != nil || count != 0 {
		t.Fatal("changed template created intent")
	}
}
func TestFixedDisabledPoolStagingRepositoryDriftRefusesOldPlan(t *testing.T) {
	var changed atomic.Bool
	var posts atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disposable-staging-review-drift" {
			t.Error("bad fixture auth")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/repositories/"+repoID {
			balancer := "roundrobin"
			if changed.Load() {
				balancer = "pack"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": repoID, "owner": "example", "name": "app", "pool_balancing_type": balancer})
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		t.Error("unexpected fixture command", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, e := garmapi.NewManagement(garmapi.ManagementConfig{Endpoint: server.URL, Token: "disposable-staging-review-drift", Trust: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Providers: []string{"capacity"}, RepositoryIDs: []string{repoID}, AllowWrites: true, AllowDisabledPoolCreation: true})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	j, e := InitializeJournal(filepath.Join(privateJournalDir(t), "drift.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	m, e := New(client, j)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewDisabledPoolStaging(m, []DisabledPoolTemplate{{Reference: "trusted/linux", RepositoryID: repoID, Spec: nativeStageSpec()}})
	if e != nil {
		t.Fatal(e)
	}
	selection := DisabledPoolSelection{RequestID: requestID, TemplateReference: "trusted/linux", Reason: "offline repository drift"}
	p, e := s.ReviewCreate(context.Background(), selection)
	if e != nil {
		t.Fatal(e)
	}
	selection.ExpectedPlanDigest = p.PlanDigest
	changed.Store(true)
	if _, e := s.Create(context.Background(), selection); !errors.Is(e, ErrDisabledPoolSelection) || posts.Load() != 0 {
		t.Fatal("repository drift accepted reviewed plan", e)
	}
	var count int
	if j.db.QueryRow("SELECT count(*) FROM intents").Scan(&count) != nil || count != 0 {
		t.Fatal("repository drift created intent")
	}
}

func TestFixedDisabledPoolStagingRecordedBindingRejectsTampering(t *testing.T) {
	s, m, j, f, selection := fixedStageSetup(t)
	ctx := context.Background()
	p, e := s.ReviewCreate(ctx, selection)
	if e != nil {
		t.Fatal(e)
	}
	selection.ExpectedPlanDigest = p.PlanDigest
	if _, e := s.Create(ctx, selection); e != nil {
		t.Fatal(e)
	}
	var original []byte
	var originalHash string
	if e := j.db.QueryRow("SELECT desired_json,request_hash FROM intents WHERE request_id=?", requestID).Scan(&original, &originalHash); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"changed reference", "changed template digest", "changed review digest", "unknown binding field"} {
		t.Run(name, func(t *testing.T) {
			var malformed []byte
			hash := originalHash
			if name == "unknown binding field" {
				var desired map[string]any
				if e := json.Unmarshal(original, &desired); e != nil {
					t.Fatal(e)
				}
				desired["template_binding"].(map[string]any)["caller_permission"] = true
				malformed, e = json.Marshal(desired)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				// Typed canonical bytes AND a recomputed request hash deliberately pass the
				// old generic intent checks. The fixed binding validator itself must refuse
				// the contradictory ref/template/review linkage. No actual known is forged.
				var desired disabledPoolIntent
				if e := json.Unmarshal(original, &desired); e != nil {
					t.Fatal(e)
				}
				switch name {
				case "changed reference":
					desired.TemplateBinding.Reference = "different/original"
				case "changed template digest":
					desired.TemplateBinding.TemplateDigest = strings.Repeat("d", 64)
				case "changed review digest":
					desired.TemplateBinding.ReviewDigest = strings.Repeat("d", 64)
				}
				malformed, e = json.Marshal(desired)
				if e != nil {
					t.Fatal(e)
				}
				hash, e = requestHash(requestID, repoID, p.RepositoryFingerprint, "repository", p.Reason, desired)
				if e != nil {
					t.Fatal(e)
				}
				if bytes.Equal(malformed, original) || hash == originalHash {
					t.Fatal("mutation did not change canonical intent/hash")
				}
			}
			if _, e := j.db.Exec("UPDATE intents SET desired_json=?,request_hash=? WHERE request_id=?", malformed, hash, requestID); e != nil {
				t.Fatal(e)
			}
			f.mu.Lock()
			gets := f.gets
			f.mu.Unlock()
			if _, e := m.RecordedDisabledPoolPlan(ctx, requestID); !errors.Is(e, ErrJournal) {
				t.Fatal("noncanonical/contradictory original binding accepted", e)
			}
			if _, e := m.ConfirmedDisabledPool(ctx, requestID); !errors.Is(e, ErrJournal) {
				t.Fatal("contradictory bound history used for fresh source", e)
			}
			f.mu.Lock()
			afterGets := f.gets
			f.mu.Unlock()
			if afterGets != gets {
				t.Fatal("invalid binding was read from source before refusal")
			}
			if _, e := j.db.Exec("UPDATE intents SET desired_json=?,request_hash=? WHERE request_id=?", original, originalHash, requestID); e != nil {
				t.Fatal(e)
			}
		})
	}
	retained, e := m.RecordedDisabledPoolPlan(ctx, requestID)
	if e != nil || !reflect.DeepEqual(retained, p) || stagePosts(f) != 1 {
		t.Fatal("read-only refusal changed original or repeated POST", e)
	}
}
