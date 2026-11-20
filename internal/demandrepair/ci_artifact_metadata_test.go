package demandrepair

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const ciArtifactFixturePath = "/repos/owner/repo/actions/runs/9007199254740995/artifacts"

func TestCIArtifactMetadataOnlyReportsExactRunWithoutProducer(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	calls := 0
	s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("wrong artifact read authority")
		}
		switch r.URL.Path {
		case strings.TrimSuffix(ciFixtureAttempt, "/2") + "/1":
			io.WriteString(w, strings.Replace(ciRunJSON(ciRepoID), `"run_attempt":2`, `"run_attempt":1`, 1))
		case ciFixtureAttempt:
			io.WriteString(w, ciRunJSON(ciRepoID))
		case ciArtifactFixturePath:
			calls++
			q := r.URL.Query()
			if len(q) != 2 || q.Get("per_page") != strconv.Itoa(CIPerPage) || q.Get("page") != "1" {
				t.Error("unqualified artifact query")
			}
			run := map[string]any{"id": int64(ciRunID), "repository_id": int64(ciRepoID), "job_id": 42, "run_attempt": 2}
			json.NewEncoder(w).Encode(map[string]any{"total_count": 4, "artifacts": []map[string]any{
				{"id": int64(ciFirstJobID), "workflow_run": run, "size_in_bytes": int64(9_007_199_254_740_999),
					"expired": false, "created_at": past, "expires_at": future, "name": "private-junit-original-name",
					"archive_download_url": "https://invalid.example/archive-secret", "url": "https://invalid.example/metadata-secret",
					"job_id": 42, "run_attempt": 2},
				{"id": 2, "workflow_run": run, "size_in_bytes": 0, "expired": false, "expires_at": past},
				{"id": 3, "workflow_run": run, "expired": false},
				{"id": 4, "workflow_run": run, "expired": true},
			}})
		default:
			t.Error("unexpected artifact/other request", r.URL.Path)
		}
	})
	var rows []CIArtifactMetadata
	for i := int64(1); i <= 2; i++ {
		attempt, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, i)
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.ListCIRunArtifactMetadata(ctx, attempt, 1)
		if err != nil || page.RepositoryID != ciRepoID || page.RunID != ciRunID || page.SourceID != s.SourceID() ||
			page.TotalCount != 4 || page.NextPage != 0 || len(page.Artifacts) != 4 || page.ObservedAt.IsZero() {
			t.Fatalf("invalid run metadata: %+v %v", page, err)
		}
		rendered := fmt.Sprintf("%+v", page)
		for _, ignored := range []string{"private-junit-original-name", "archive-secret", "metadata-secret", "JobID", "Attempt"} {
			if strings.Contains(rendered, ignored) {
				t.Fatal("retained unsupported provenance or untrusted metadata")
			}
		}
		rows = page.Artifacts
	}
	if calls != 2 || rows[0].ID != ciFirstJobID || rows[0].SizeInBytes != 9007199254740999 || !rows[0].SizeKnown ||
		!rows[0].CreatedAt.Equal(past) || !rows[0].ExpiresAt.Equal(future) || rows[0].Availability != CIArtifactListedUnexpired ||
		!rows[1].SizeKnown || rows[1].SizeInBytes != 0 || rows[1].Availability != CIArtifactExpired ||
		rows[2].SizeKnown || !rows[2].CreatedAt.IsZero() || rows[2].Availability != CIArtifactAvailabilityUnknown ||
		rows[3].Availability != CIArtifactExpired {
		t.Fatalf("unqualified size/availability assessment: %+v", rows)
	}
}

func TestCIArtifactMetadataPaginationAndEmptyScope(t *testing.T) {
	ctx := context.Background()
	for _, current := range []int64{ciRepoID, ciRepoID + 1} {
		s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case ciFixtureAttempt:
				io.WriteString(w, ciRunJSON(ciRepoID))
			case "/repos/owner/repo":
				fmt.Fprintf(w, `{"id":%d,"full_name":"owner/repo"}`, current)
			case ciArtifactFixturePath:
				switch r.URL.Query().Get("page") {
				case "1":
					io.WriteString(w, `{"total_count":0,"artifacts":[]}`)
				case "2":
					fmt.Fprintf(w, `{"total_count":51,"artifacts":[{"id":51,"workflow_run":{"id":%d,"repository_id":%d}}]}`, ciRunID, ciRepoID)
				default:
					t.Error("unexpected page")
				}
			default:
				t.Error("unexpected request", r.URL.Path)
			}
		})
		attempt, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2)
		if err != nil {
			t.Fatal(err)
		}
		empty, err := s.ListCIRunArtifactMetadata(ctx, attempt, 1)
		if current == ciRepoID && (err != nil || empty.Artifacts == nil || len(empty.Artifacts) != 0 || empty.TotalCount != 0) {
			t.Fatalf("enrolled empty page unavailable: %+v %v", empty, err)
		}
		if current != ciRepoID && (err != ErrObservation || empty.Artifacts != nil) {
			t.Fatalf("synthetic replacement erroneously confirmed empty: %+v %v", empty, err)
		}
		if current == ciRepoID {
			result, err := s.ListCIRunArtifactMetadata(ctx, attempt, 2)
			if err != nil || result.TotalCount != 51 || result.NextPage != 0 || len(result.Artifacts) != 1 || result.Artifacts[0].ID != 51 {
				t.Fatalf("malformed final page: %+v %v", result, err)
			}
		}
	}
}

func TestCIArtifactMetadataRejectsUnmintedCrossSourceExpiredAndCanceled(t *testing.T) {
	ctx := context.Background()
	s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ciFixtureAttempt {
			t.Error("unexpected artifact read")
		}
		io.WriteString(w, ciRunJSON(ciRepoID))
	})
	a, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewCIHealthSource(s.client, s.Repositories())
	if err != nil {
		t.Fatal(err)
	}
	s.client.resolve = func(context.Context, string) (string, error) {
		t.Error("credentials requested for invalid artifact capability")
		return "", ErrObservation
	}
	expired := a
	expired.at = time.Now().UTC().Add(-ciAttemptLifetime)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, item := range []struct {
		source *CIHealthSource
		ctx    context.Context
		a      CIAttempt
		page   int
	}{
		{s, ctx, CIAttempt{}, 1},
		{shadow, ctx, a, 1},
		{s, ctx, expired, 1},
		{s, cancelled, a, 1},
		{s, ctx, a, CIMaxPages + 1},
	} {
		result, err := item.source.ListCIRunArtifactMetadata(item.ctx, item.a, item.page)
		if err != ErrObservation || result.Artifacts != nil || result.RunID != 0 {
			t.Fatalf("invalid attempt returned artifact data: %+v %v", result, err)
		}
	}
}

func TestCIArtifactMetadataChecksFinalLeaseCancellationAndRollbackAfterProjection(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case ciFixtureAttempt:
					io.WriteString(w, ciRunJSON(ciRepoID))
				case ciArtifactFixturePath:
					if empty {
						io.WriteString(w, `{"total_count":0,"artifacts":[]}`)
					} else {
						fmt.Fprintf(w, `{"total_count":1,"artifacts":[{"id":1,"workflow_run":{"id":%d,"repository_id":%d}}]}`, ciRunID, ciRepoID)
					}
				case "/repos/owner/repo":
					if !empty {
						t.Error("unnecessary empty-list check")
					}
					fmt.Fprintf(w, `{"id":%d,"full_name":"owner/repo"}`, ciRepoID)
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			})
			a, err := s.ReadCIAttempt(context.Background(), repo.GarmID, ciRunID, 2)
			if err != nil {
				t.Fatal(err)
			}
			tick := func(wall, mono time.Duration) ciArtifactTick {
				return ciArtifactTick{wall: a.at.Add(wall), monotonic: a.minted.Add(mono)}
			}
			for _, tc := range []struct {
				name string
				end  ciArtifactTick
			}{
				{"success", tick(3*time.Second, 3*time.Second)},
				{"wall expiry", tick(ciAttemptLifetime, 3*time.Second)},
				{"monotonic expiry", tick(3*time.Second, ciAttemptLifetime)},
				{"wall rollback", tick(time.Second, 3*time.Second)},
				{"monotonic rollback", tick(3*time.Second, time.Second)},
				{"monotonic unavailable", ciArtifactTick{wall: a.at.Add(3 * time.Second), monotonic: a.minted.Add(3 * time.Second).Round(0)}},
				{"cancellation", tick(3*time.Second, 3*time.Second)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					ticks := []ciArtifactTick{tick(time.Second, time.Second), tick(2*time.Second, 2*time.Second), tc.end}
					i := 0
					clock := func() ciArtifactTick {
						if i == 2 && tc.name == "cancellation" {
							cancel()
						}
						at := ticks[i]
						i++
						return at
					}
					got, err := s.listCIRunArtifactMetadata(ctx, a, 1, clock)
					if i != 3 {
						t.Fatalf("missing final projection fence, ticks=%d", i)
					}
					if tc.name == "success" {
						if err != nil || got.Artifacts == nil || !got.ObservedAt.Equal(ticks[1].wall) || (len(got.Artifacts) == 0) != empty {
							t.Fatalf("successful page lost its actual observation time: %+v %v", got, err)
						}
					} else if err != ErrObservation || got.Artifacts != nil || !got.ObservedAt.IsZero() || got.RepositoryID != 0 {
						t.Fatalf("projection escaped final fence: %+v %v", got, err)
					}
				})
			}
		})
	}
}

func TestCIArtifactMetadataRejectsUnverifiedRowsPartialPageAndRedirect(t *testing.T) {
	ctx := context.Background()
	run := fmt.Sprintf(`"workflow_run":{"id":%d,"repository_id":%d}`, ciRunID, ciRepoID)
	got := fmt.Sprintf(`{"id":1,%s}`, run)
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{name: "unexpected run", body: fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":1,"workflow_run":{"id":%d,"repository_id":%d}}]}`, ciRunID-1, ciRepoID)},
		{name: "fork cannot replace repository pin", body: fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":1,"workflow_run":{"id":%d,"repository_id":3,"head_repository_id":%d}}]}`, ciRunID, ciRepoID)},
		{name: "no positive run relation", body: `{"total_count":1,"artifacts":[{"id":1}]}`},
		{name: "malformed run relation", body: `{"total_count":1,"artifacts":[{"id":1,"workflow_run":null}]}`},
		{name: "absent artifacts", body: `{"total_count":0}`},
		{name: "partial page", body: fmt.Sprintf(`{"total_count":2,"artifacts":[%s]}`, got)},
		{name: "duplicate artifact", body: fmt.Sprintf(`{"total_count":2,"artifacts":[%s,%s]}`, got, got)},
		{name: "Unicode case-alias", body: fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":1,"ſize_in_bytes":5,%s}]}`, run)},
		{name: "negative size", body: fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":1,"size_in_bytes":-5,%s}]}`, run)},
		{name: "inverted time", body: fmt.Sprintf(`{"total_count":1,"artifacts":[{"id":1,"created_at":"2026-01-05T00:00:00Z","expires_at":"2026-01-01T00:00:00Z",%s}]}`, run)},
		{name: "excessive response", body: strings.Repeat(" ", (4<<20)+1)},
		{name: "unauthorized", code: http.StatusForbidden},
		{name: "redirect", code: http.StatusFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ciTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case ciFixtureAttempt:
					io.WriteString(w, ciRunJSON(ciRepoID))
				case ciArtifactFixturePath:
					if tc.code != 0 {
						w.Header().Set("Location", "/archive")
						w.WriteHeader(tc.code)
					}
					io.WriteString(w, tc.body)
				default:
					t.Error("metadata client followed an unexpected route", r.URL.Path)
				}
			})
			a, err := s.ReadCIAttempt(ctx, repo.GarmID, ciRunID, 2)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.ListCIRunArtifactMetadata(ctx, a, 1)
			if err != ErrObservation || result.Artifacts != nil || !result.ObservedAt.IsZero() {
				t.Fatalf("invalid response leaked partial facts: %+v %v", result, err)
			}
		})
	}
}
