package adminview

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func completeRunnerDiskFixture(c *RunnerDiskHealthCache, a *RunnerDiskHealthAttempt, s hostinventory.DirFilesystemSample, inode uint64, review time.Time) bool {
	return c.complete(a, func() (hostinventory.DirFilesystemSample, uint64, time.Time, bool) {
		return s, inode, review, true
	})
}

func TestRunnerDiskSnapshotLatestAttemptWinsAndCopies(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	b.HostID = "host-a"
	clock := sample.ObservedAt
	c, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return clock }
	h, _, token := fixture(t)
	h.SetRunnerDiskHealthSource(c)
	startOld, ok := c.Begin(b)
	if !ok {
		t.Fatal("old attempt did not begin")
	}
	current, ok := c.Begin(b)
	if !ok {
		t.Fatal("current attempt did not begin")
	}
	clock = clock.Add(time.Second)
	sample.ObservedAt = clock
	review := time.Now().UTC().Add(time.Minute)
	// The httptest overview uses the real clock. First exercise the synthetic
	// clock's ordering/copy semantics independent of the HTTP projection.
	if !completeRunnerDiskFixture(c, current, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("current accepted read was lost")
	}
	if c.Complete(startOld, hostinventory.BoundDirFilesystemSample{}) || completeRunnerDiskFixture(c, current, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("superseded or reused attempt was consumed")
	}
	first := c.SnapshotRunnerDiskHealth()
	if len(first[b.HostID]) != 1 || first[b.HostID][0].AvailableInodes != 0 {
		t.Fatalf("current zero sample lost: %+v", first)
	}
	first[b.HostID][0].AvailableBytes = 1234
	delete(first, b.HostID)
	next := c.SnapshotRunnerDiskHealth()
	if len(next[b.HostID]) != 1 || next[b.HostID][0].AvailableBytes != 0 {
		t.Fatalf("snapshot mutation escaped: %+v", next)
	}
	if got := request(t, h, http.MethodGet, "/admin/api/v1/overview", token); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "\"state\":\"current\"") {
		t.Fatal("expired fixture was presented as current")
	}
	// Separate actual-clock fixture for the existing Setter and authenticated
	// overview to render against their own real expiry.
	liveFixture := b
	liveFixture.ReviewExpiresAt = review
	fresh, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{liveFixture})
	if err != nil {
		t.Fatal(err)
	}
	call, ok := fresh.Begin(liveFixture)
	if !ok {
		t.Fatal("read-only fixture did not begin")
	}
	sample.ObservedAt = time.Now()
	if !completeRunnerDiskFixture(fresh, call, sample, liveFixture.DirectoryInode, review) {
		t.Fatal("known same-scope fixture was not accepted")
	}
	h.SetRunnerDiskHealthSource(fresh)
	got := request(t, h, http.MethodGet, "/admin/api/v1/overview", token)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"runner_disk_health":[{"storage_pool":"ci-pool","state":"current"`) {
		t.Fatalf("current cache did not reach existing authenticated overview: %d %s", got.Code, got.Body.String())
	}
}

func TestRunnerDiskSnapshotRejectsFailuresReplaysScopeChangeAndExpiry(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	clock := sample.ObservedAt
	c, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return clock }
	publish := func() {
		t.Helper()
		a, ok := c.Begin(b)
		if !ok {
			t.Fatal("attempt did not begin")
		}
		clock = clock.Add(time.Second)
		sample.ObservedAt = clock
		if !completeRunnerDiskFixture(c, a, sample, b.DirectoryInode, b.ReviewExpiresAt) {
			t.Fatal("current sample not accepted")
		}
	}
	publish()
	a, ok := c.Begin(b)
	if !ok {
		t.Fatal("failure attempt did not begin")
	}
	if c.Complete(a, hostinventory.BoundDirFilesystemSample{}) || len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("latest collection failure did not withdraw old reading")
	}
	a, ok = c.Begin(b)
	if !ok {
		t.Fatal("same route could not reobserve after transient failure")
	}
	if completeRunnerDiskFixture(c, a, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("old sample replayed on new ticket")
	}
	publish()
	expires := sample.ObservedAt.Add(maxRunnerDiskHealthAge)
	clock = expires
	if len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("expired reading still present")
	}
	clock = expires.Add(-time.Minute)
	if len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("expired reading resurrected on clock rollback")
	}
	if _, ok := c.Begin(b); ok {
		t.Fatal("new observation accepted while clock regressed")
	}
	clock = expires.Add(time.Second)
	publish()
	mismatch := b
	mismatch.Scope.MountID++
	if _, ok := c.Begin(mismatch); ok || len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("replacement did not invalidate old identity and readings")
	}
	if _, ok := c.Begin(b); ok {
		t.Fatal("old route could resume after replacement")
	}
}

func TestRunnerDiskSnapshotRefusesCrossCacheAndDetectedInodeDrift(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	clock := sample.ObservedAt
	first, _ := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	second, _ := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	first.now = func() time.Time { return clock }
	second.now = first.now
	a, ok := first.Begin(b)
	if !ok {
		t.Fatal("fixture attempt did not start")
	}
	clock = clock.Add(time.Second)
	sample.ObservedAt = clock
	if completeRunnerDiskFixture(second, a, sample, b.DirectoryInode, b.ReviewExpiresAt) ||
		completeRunnerDiskFixture(first, a, sample, b.DirectoryInode+1, b.ReviewExpiresAt) || len(first.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("cross-cache or wrong-dir sample accepted")
	}
	if _, ok := first.Begin(b); ok {
		t.Fatal("descriptor identity drift did not revoke the original")
	}
}

func TestRunnerDiskSnapshotCatalogIsBoundedAndImmutable(t *testing.T) {
	b, _ := runnerDiskAcceptanceFixture(t)
	var tooMany []RunnerDiskHealthBinding
	for i := 0; i <= maxRunnerDiskSnapshotBindings; i++ {
		v := b
		v.HostID = fmt.Sprintf("host-%d", i)
		tooMany = append(tooMany, v)
	}
	if c, err := NewRunnerDiskHealthCache(tooMany); c != nil || err == nil {
		t.Fatal("oversized catalog accepted")
	}
	var tooManyAtHost []RunnerDiskHealthBinding
	for i := 0; i <= maxRunnerDiskHealthPools; i++ {
		v := b
		v.Scope.StoragePool = fmt.Sprintf("pool-%d", i)
		tooManyAtHost = append(tooManyAtHost, v)
	}
	if c, err := NewRunnerDiskHealthCache(tooManyAtHost); c != nil || err == nil {
		t.Fatal("oversized host catalog accepted")
	}
	if c, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b, b}); c != nil || err == nil {
		t.Fatal("duplicate scope accepted")
	}
	input := []RunnerDiskHealthBinding{b}
	c, err := NewRunnerDiskHealthCache(input)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return b.ReviewExpiresAt.Add(-time.Minute) }
	input[0].DirectoryInode++
	a, _ := c.Begin(b)
	if a == nil {
		t.Fatal("caller modified the configured scope")
	}

	var reads sync.WaitGroup
	for i := 0; i < 8; i++ {
		reads.Add(1)
		go func() {
			defer reads.Done()
			_ = c.SnapshotRunnerDiskHealth()
			c.Complete(a, hostinventory.BoundDirFilesystemSample{})
		}()
	}
	reads.Wait()
	if len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("concurrent empty outcomes published data")
	}
}

func TestRunnerDiskSnapshotExplicitWithdrawalFencesPendingObservation(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	clock := sample.ObservedAt
	c, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return clock }
	a, ok := c.Begin(b)
	if !ok {
		t.Fatal("first attempt missing")
	}
	clock = clock.Add(time.Second)
	sample.ObservedAt = clock
	if !completeRunnerDiskFixture(c, a, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("first observation not accepted")
	}
	pending, ok := c.Begin(b)
	if !ok {
		t.Fatal("follow-up attempt missing")
	}
	wrong := b
	wrong.Scope.DeviceMinor++
	if c.Withdraw(wrong) || len(c.SnapshotRunnerDiskHealth()[b.HostID]) != 1 {
		t.Fatal("different route could withdraw the exact approved route")
	}
	if !c.Withdraw(b) || len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("withdrawal did not clear readings")
	}
	clock = clock.Add(time.Second)
	sample.ObservedAt = clock
	if completeRunnerDiskFixture(c, pending, sample, b.DirectoryInode, b.ReviewExpiresAt) {
		t.Fatal("older attempt resurrected withdrawn route")
	}
	if _, ok := c.Begin(b); ok {
		t.Fatal("withdrawn route reattached itself")
	}
}

func TestRunnerDiskSnapshotUsesWallOnlyForLocalClockAndSamplerTimestamps(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	start := time.Now()
	if start == start.Round(0) {
		t.Fatal("regression fixture needs a real monotonic time.Now value")
	}
	clock := start
	b.ReviewExpiresAt = start.Add(time.Minute)
	c, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return clock }
	a, ok := c.Begin(b)
	if !ok || a.startedAt != start.UTC() {
		t.Fatal("start stored a monotonic clock but reviewed deadlines are wall time")
	}
	s := c.slots[a.key]
	if s.checkedAt != start.UTC() || s.binding.ReviewExpiresAt != b.ReviewExpiresAt.UTC() {
		t.Fatal("scope or latest check retained incompatible clock encodings")
	}
	clock = start.Add(2 * time.Second)
	sample.ObservedAt = start.Add(time.Second)
	observed := sample.ObservedAt.UTC()
	review := start.Add(59 * time.Second)
	if !completeRunnerDiskFixture(c, a, sample, b.DirectoryInode, review) {
		t.Fatal("valid mixed-encoding read refused")
	}
	rows := c.SnapshotRunnerDiskHealth()[b.HostID]
	if len(rows) != 1 || rows[0].ObservedAt != observed || rows[0].ExpiresAt != review.UTC() || s.lastObserved != observed || s.checkedAt != clock.UTC() {
		t.Fatalf("a numeric row or retention watermark used the monotonic times: %+v", rows)
	}

	clock = start.Add(3 * time.Second) // later monotonic-bearing reading
	active, ok := c.Begin(b)
	if !ok {
		t.Fatal("second sampling attempt did not start")
	}
	// Reconstruct an earlier wall time without its former monotonic encoding.
	clock = time.Unix(0, start.Add(2*time.Second).UnixNano()).UTC()
	if _, ok := c.Begin(b); ok || len(c.SnapshotRunnerDiskHealth()) != 0 {
		t.Fatal("clock rollback retained counters or minted another attempt")
	}
	clock = start.Add(4 * time.Second)
	sample.ObservedAt = clock
	if completeRunnerDiskFixture(c, active, sample, b.DirectoryInode, review) {
		t.Fatal("an attempt started before rollback could still publish")
	}
}
