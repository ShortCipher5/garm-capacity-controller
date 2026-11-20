package garmapi

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestRepositoryIdleScopeOptInAndRefusal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(map[string]any)
		classified bool
	}{
		{"ordinary", func(j map[string]any) {}, true},
		{"locked", func(j map[string]any) { j["locked_by"] = otherID }, false},
		{"no candidates", func(j map[string]any) { j["labels"] = []string{"missing"} }, false},
		{"scaleset", func(j map[string]any) { j["scaleset_job_id"] = "scale-job" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := demandJob(1)
			tc.mutate(j)
			c := demandFixture(t, encodeJobs(t, j), nil)
			legacy, e := c.Observe(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if a := legacy.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute); a.Version() != "" || a.Classified() {
				t.Fatal("legacy minted proof")
			}
			o, e := c.ObserveWithRepositoryIdleScope(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			a := o.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute)
			if a.Version() != RepositoryIdleScopeVersion || a.Classified() != tc.classified {
				t.Fatalf("assessment %+v", a)
			}
			if !reflect.DeepEqual(legacy.Jobs, o.Jobs) || legacy.Digest != o.Digest {
				t.Fatal("legacy queue/digest changed")
			}
			for _, repo := range []string{"", otherID} {
				if o.RepositoryIdleScope(repo, time.Now(), time.Minute).Version() != "" {
					t.Fatal("foreign scope exposed")
				}
			}
			if o.RepositoryIdleScope(managedRepoID, o.StartedAt.Add(time.Minute), time.Minute).Version() != "" {
				t.Fatal("expiry equality accepted")
			}
			raw, _ := json.Marshal(o)
			var detached DemandObservation
			json.Unmarshal(raw, &detached)
			if detached.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute).Version() != "" {
				t.Fatal("JSON minted proof")
			}
			o.Supply[0].Other++
			if o.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute).Version() != "" {
				t.Fatal("modified observation accepted")
			}
		})
	}
}

func TestRepositoryIdleScopeForeignMutationsInvariant(t *testing.T) {
	base := demandJob(1)
	var digest string
	var projection []byte
	for _, repo := range []any{otherID, nil, "absent"} {
		foreign := demandJob(2)
		foreign["repo_id"] = repo
		foreign["locked_by"] = otherID
		foreign["labels"] = []string{"foreign"}
		if repo == "absent" {
			delete(foreign, "repo_id")
		}
		c := demandFixture(t, encodeJobs(t, base, foreign), nil)
		o, e := c.ObserveWithRepositoryIdleScope(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		a := o.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute)
		if !a.Classified() {
			t.Fatal("foreign job affected scope")
		}
		raw, _ := json.Marshal(struct {
			Jobs   []QueuedJob
			Supply []RunnerSupply
		}{o.Jobs, o.Supply})
		if digest != "" && (digest != o.Digest || string(raw) != string(projection)) {
			t.Fatal("foreign rows affected projected bytes/digest")
		}
		digest = o.Digest
		projection = raw
	}
}

func TestRepositoryIdleScopeEmptyAndMalformedOwnedQueue(t *testing.T) {
	c := demandFixture(t, "[]", nil)
	o, e := c.ObserveWithRepositoryIdleScope(context.Background())
	if e != nil || !o.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute).Classified() {
		t.Fatal("explicit empty scoped sample unavailable", e)
	}
	for _, field := range []string{"status", "labels", "workflow_job_id"} {
		j := demandJob(1)
		j[field] = map[string]string{"invalid": "type"}
		bad := demandFixture(t, encodeJobs(t, j), nil)
		o, e := bad.ObserveWithRepositoryIdleScope(context.Background())
		if e == nil || o.RepositoryIdleScope(managedRepoID, time.Now(), time.Minute).Version() != "" {
			t.Fatal("malformed owned row minted assessment", field)
		}
	}
}

func TestRepositoryIdleScopesBatchMatchesSinglesAndOwnsResults(t *testing.T) {
	c := demandFixture(t, "[]", nil)
	// Add one explicitly allowed repository before observation. No remote rows
	// are manufactured and no forbidden repository is included in the result.
	c.repositories[otherID] = true
	o, e := c.ObserveWithRepositoryIdleScope(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ids := []string{managedRepoID, otherID}
	batch := o.RepositoryIdleScopes(ids, now, time.Minute)
	if len(batch) != 2 {
		t.Fatal("missing batch", batch)
	}
	for _, id := range ids {
		if batch[id] != o.RepositoryIdleScope(id, now, time.Minute) {
			t.Fatal("single differs", id)
		}
	}
	delete(batch, managedRepoID)
	if !o.RepositoryIdleScope(managedRepoID, now, time.Minute).Classified() {
		t.Fatal("returned map aliases proof")
	}
	for _, bad := range [][]string{nil, {}, {managedRepoID, managedRepoID}, {managedRepoID, "unknown"}, make([]string, 1001)} {
		if got := o.RepositoryIdleScopes(bad, now, time.Minute); got != nil {
			t.Fatal("invalid request returned partial evidence", got)
		}
	}
	if o.RepositoryIdleScopes(ids, o.StartedAt.Add(time.Minute), time.Minute) != nil {
		t.Fatal("expired batch returned")
	}
	o.Pools[0].MaxRunners++
	if o.RepositoryIdleScopes(ids, now, time.Minute) != nil {
		t.Fatal("modified sample returned")
	}
}

func TestRepositoryIdleScopesIncludesBlockedEntriesWithoutInventingCoverage(t *testing.T) {
	j := demandJob(1)
	j["locked_by"] = otherID
	c := demandFixture(t, encodeJobs(t, j), nil)
	c.repositories[otherID] = true
	o, e := c.ObserveWithRepositoryIdleScope(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	got := o.RepositoryIdleScopes([]string{otherID, managedRepoID}, time.Now(), time.Minute)
	if len(got) != 2 || got[managedRepoID].Version() != RepositoryIdleScopeVersion || got[managedRepoID].Classified() || !got[otherID].Classified() {
		t.Fatal("incorrect classification", got)
	}
	if (DemandObservation{}).RepositoryIdleScopes([]string{managedRepoID}, time.Now(), time.Minute) != nil {
		t.Fatal("absence accepted")
	}
}
