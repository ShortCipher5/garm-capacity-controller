package adminview

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/remotedisktelemetry"
	"github.com/ShortCipher5/garm-capacity-controller/internal/testsupport"
)

func TestRemoteRunnerDiskStartsOnlyForExactReviewedRoute(t *testing.T) {
	var calls atomic.Int32
	reached := make(chan struct{}, 1)
	server, trust, _ := testsupport.NativeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls.Add(1)
		if q.Method != http.MethodPost || q.URL.Path != remotedisktelemetry.HTTPPath || q.TLS == nil || len(q.TLS.VerifiedChains) == 0 {
			t.Error("not the expected reviewed challenge")
		}
		reached <- struct{}{}
		http.Error(w, "no accepted observation", http.StatusServiceUnavailable)
	}))
	client, err := x509.ParseCertificate(trust.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := runnerDiskAcceptanceFixture(t)
	b.ReviewExpiresAt = time.Now().UTC().Add(time.Minute)
	review := remotedisktelemetry.Review{
		Scope: b.Scope, DirectoryInode: b.DirectoryInode, ExpiresAt: b.ReviewExpiresAt,
		PeerKeySHA256: sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo),
	}
	reader, err := remotedisktelemetry.NewHTTPReader(server.URL, review, remotedisktelemetry.HTTPClientTrust{
		TLS: trust, ClientKeySHA256: sha256.Sum256(client.RawSubjectPublicKeyInfo),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RemoteRunnerDiskHealthInput{Routes: []RemoteRunnerDiskHealthRoute{{b, review, reader}}}
	if r, err := NewRemoteRunnerDiskHealthCoordinator(input); r != nil || err != nil {
		t.Fatal("not default off")
	}
	input.Enabled = true
	mismatch := input
	mismatch.Routes = append([]RemoteRunnerDiskHealthRoute(nil), input.Routes...)
	mismatch.Routes[0].Binding.DirectoryInode++
	if r, err := NewRemoteRunnerDiskHealthCoordinator(mismatch); r != nil || !errors.Is(err, errRemoteRunnerDiskCoordinator) {
		t.Fatal("drifting binding")
	}
	mismatch.Routes[0].Binding = b
	mismatch.Routes[0].Review.PeerKeySHA256[0] ^= 1
	if r, err := NewRemoteRunnerDiskHealthCoordinator(mismatch); r != nil || !errors.Is(err, errRemoteRunnerDiskCoordinator) {
		t.Fatal("drifting peer")
	}
	mismatch.Routes = append([]RemoteRunnerDiskHealthRoute{}, input.Routes[0], input.Routes[0])
	if r, err := NewRemoteRunnerDiskHealthCoordinator(mismatch); r != nil || !errors.Is(err, errRemoteRunnerDiskCoordinator) {
		t.Fatal("duplicate reader/route")
	}
	r, err := NewRemoteRunnerDiskHealthCoordinator(input)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	input.Routes[0].Binding.HostID = "changed"
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 || calls.Load() != 0 {
		t.Fatal("unowned startup attempted poll", out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-reached:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit Run did not make reviewed read")
	}
	if err := r.Run(ctx); !errors.Is(err, errRemoteRunnerDiskCoordinator) {
		t.Fatal("second owner admitted", err)
	}
	cancel()
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("canceled owner still displays data", out)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry", calls.Load())
	}
}

func TestRemoteRunnerDiskLatestReceiptWithdrawsAndCannotResurrect(t *testing.T) {
	b, sample := runnerDiskAcceptanceFixture(t)
	now := sample.ObservedAt
	cache, err := NewRunnerDiskHealthCache([]RunnerDiskHealthBinding{b})
	if err != nil {
		t.Fatal(err)
	}
	cache.now = func() time.Time { return now }
	life, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &remoteRunnerDiskCell{cache: cache, lifetime: life, cancel: cancel}
	r := &RemoteRunnerDiskHealthCoordinator{c}
	route := remoteRunnerDiskRead{binding: b, review: remotedisktelemetry.Review{Scope: b.Scope, DirectoryInode: b.DirectoryInode, ExpiresAt: b.ReviewExpiresAt}}
	fresh := func(_ context.Context, _ remotedisktelemetry.Review) (remotedisktelemetry.DisplayReading, error) {
		now = now.Add(time.Second)
		return remotedisktelemetry.DisplayReading{
			ObservedAt: now, ExpiresAt: now.Add(30 * time.Second), TotalBytes: 8192, AvailableBytes: 0, TotalInodes: 7, AvailableInodes: 0,
		}, nil
	}
	route.read = fresh
	c.routes = []remoteRunnerDiskRead{route}
	c.poll(life, route)
	first := r.SnapshotRunnerDiskHealth()
	if rows := first[b.HostID]; len(rows) != 1 || rows[0].AvailableBytes != 0 || rows[0].StoragePool != b.Scope.StoragePool || rows[0].TotalBytes != 8192 {
		t.Fatal("bad own receipt", first)
	}
	first[b.HostID][0].TotalBytes = 100
	if rows := r.SnapshotRunnerDiskHealth()[b.HostID]; len(rows) != 1 || rows[0].TotalBytes != 8192 {
		t.Fatal("returned aliased view")
	}
	route.read = func(context.Context, remotedisktelemetry.Review) (remotedisktelemetry.DisplayReading, error) {
		return remotedisktelemetry.DisplayReading{}, remotedisktelemetry.ErrHTTPReader
	}
	c.poll(life, route)
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("failure retained old data", out)
	}
	route.read = fresh
	readerCutoff := now.Add(time.Second + 30*time.Second)
	c.poll(life, route)
	now = readerCutoff
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("display extended reader receipt cutoff", out)
	}
	now = now.Add(-time.Second)
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("clock rollback revived receipt", out)
	}
	now = readerCutoff.Add(time.Second)
	route.read = func(ctx context.Context, v remotedisktelemetry.Review) (remotedisktelemetry.DisplayReading, error) {
		defer cancel()
		return fresh(ctx, v)
	}
	c.poll(life, route)
	if out := r.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("canceled owner published positive", out)
	}
	r.Close()
	if out := c.cache.SnapshotRunnerDiskHealth(); len(out) != 0 {
		t.Fatal("cache retained a stop-late receipt", out)
	}
}
