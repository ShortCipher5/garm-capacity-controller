package admission

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestReadReuseStatusDistinguishesHistoricFromVerifiedReleases ensures the UI
// never presents a pre-verification tombstone as verified reusable capacity.
func TestReadReuseStatusDistinguishesHistoricFromVerifiedReleases(t *testing.T) {
	ctx := context.Background()
	h := newReuseHarness(t)
	h.jobBody("/repos/owner/repo/actions/jobs/91", 91, 12, 17, "runner-1", "owner", "repo", true)
	repo := reuseRepo("a", "repo")
	f := newReuseFixture(t, h, "controller-1", "app", "runner-1", repo)
	charge := f.buildCharge(t, f.observeStockRegistration(t))
	workload := f.workloadProof(t, f.observeLifecycle(t, 91))
	// A second reservation excluded from accounting by a pre-verification
	// release path: the tombstone record is not verified terminal evidence.
	historic := Request{Key: reuseRunnerKey("controller-1", "app", "runner-legacy"), Pool: "app", Host: "a", Resources: Resources{CPU: 1000, RAM: 2048, Disk: 4096, Runners: 1}}
	if _, err := f.store.Admit(ctx, historic); err != nil {
		t.Fatal(err)
	}
	legacyRecord, err := json.Marshal(map[string]any{"legacy": "pre-verification release record", "absence_digest": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("INSERT INTO releases(request_key,pool,host,cpu,ram,disk,runners,absence_digest,record,released_at) VALUES(?,?,?,?,?,?,?,?,?,?)", historic.Key, historic.Pool, historic.Host, historic.Resources.CPU, historic.Resources.RAM, historic.Resources.Disk, historic.Resources.Runners, evidenceDigest(legacyRecord), legacyRecord, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// Full verified release of the first runner.
	if _, err := f.store.MarkDeleting(ctx, f.request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReleaseVerified(ctx, f.request, f.evidence(charge, workload, f.teardownProof(t), f.cleanupProof())); err != nil {
		t.Fatal(err)
	}
	view, err := f.store.ReadReuseStatus(ctx)
	if err != nil || len(view.Rows) != 2 {
		t.Fatalf("status view: %+v %v", view, err)
	}
	byKey := map[string]int{}
	for i, row := range view.Rows {
		byKey[row.RequestKey] = i
	}
	verified := view.Rows[byKey[f.request.Key]]
	if verified.Disposition != ReuseStatusVerifiedReusable || !verified.Reusable.All() || verified.Reasons[0] != "released_verified_teardown" {
		t.Fatalf("verified release not shown as verified reusable: %+v", verified)
	}
	old := view.Rows[byKey[historic.Key]]
	if old.Disposition != ReuseStatusReleasedHistoric || old.Reusable.CPU || old.Reusable.Disk || old.Remaining != (Resources{}) || len(old.Reasons) != 1 || old.Reasons[0] != "historic_release_requires_review" {
		t.Fatalf("historic release not kept distinct: %+v", old)
	}
	// Accounting stays unchanged: both exclusions remain excluded and no
	// charge reappears because of the distinction.
	if got := sqliteSnapshot(t, f.store); got.Allocated.Global.Runners != 0 {
		t.Fatalf("historic distinction changed accounting: %+v", got.Allocated.Global)
	}
}
