package remotedisktelemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

func fixture(t *testing.T) (time.Time, Review, hostinventory.DirFilesystemSample, [32]byte, []byte) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 7, time.UTC)
	scope := hostinventory.DirFilesystemScope{
		HostFingerprint: strings.Repeat("a", 64), StoragePool: "fixture-pool",
		BootID: "11111111-1111-4111-8111-111111111111", MountNamespace: 10, MountID: 20,
		DeviceMajor: 8, DeviceMinor: 2,
	}
	r := Review{Scope: scope, DirectoryInode: 123, ExpiresAt: at.Add(time.Minute), PeerKeySHA256: [32]byte{2}}
	s := hostinventory.DirFilesystemSample{Scope: scope, ObservedAt: at.Add(time.Second),
		TotalBytes: 8192, AvailableBytes: 0, TotalInodes: 64, AvailableInodes: 17}
	challenge := [32]byte{3}
	payload, ok := encode(challenge, s, r.DirectoryInode, r.ExpiresAt)
	if !ok {
		t.Fatal("synthetic sample was refused")
	}
	return at, r, s, challenge, payload
}

func syntheticAuthenticated(r Review, p []byte) AuthenticatedPayload {
	// This fixture stands in for an intentionally absent real transport.
	return AuthenticatedPayload{r.PeerKeySHA256, sha256.Sum256(p)}
}

func beginFixture(t *testing.T, r Review, challenge [32]byte, now, deadline time.Time) *Attempt {
	t.Helper()
	a, ok := Begin(r, challenge, now, deadline)
	if !ok || a == nil {
		t.Fatal("expected valid fixture attempt")
	}
	return a
}

func TestRemoteDiskDisplayExactOneShot(t *testing.T) {
	at, r, s, challenge, p := fixture(t)
	if data, ok := Encode(challenge, hostinventory.BoundDirFilesystemSample{}); ok || data != nil {
		t.Fatal("unknown local observation was encoded")
	}
	for _, raw := range [][]byte{[]byte("path"), []byte("secret"), []byte("backend"), []byte("raw_counters")} {
		if bytes.Contains(p, raw) {
			t.Fatal("out of contract key in projection")
		}
	}
	a := beginFixture(t, r, challenge, at, at.Add(4*time.Second))
	copied := *a
	r.ExpiresAt = r.ExpiresAt.In(time.FixedZone("same fixture wall instant", 3600))
	got, ok := a.Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second))
	if !ok || got != (DisplayReading{
		ObservedAt: s.ObservedAt, ExpiresAt: s.ObservedAt.Add(maxReadingAge),
		TotalBytes: 8192, AvailableBytes: 0, TotalInodes: 64, AvailableInodes: 17,
	}) {
		t.Fatalf("expected display-only numbers including known zero, got %+v, %t", got, ok)
	}
	if got, ok := copied.Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second)); ok || got != (DisplayReading{}) {
		t.Fatal("copied attempt replayed")
	}
	other := beginFixture(t, r, [32]byte{4}, at, at.Add(4*time.Second))
	if got, ok := other.Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second)); ok || got != (DisplayReading{}) {
		t.Fatal("prior challenge replayed into a new attempt")
	}
}

func TestRemoteDiskDisplayFailsClosedOnWireOrRouteDrift(t *testing.T) {
	at, r, _, challenge, p := fixture(t)
	for _, tc := range []struct {
		name   string
		change func(*Review, *AuthenticatedPayload, *[]byte, *time.Time)
	}{
		{"wrong peer", func(_ *Review, auth *AuthenticatedPayload, _ *[]byte, _ *time.Time) { auth.PeerKeySHA256[0]++ }},
		{"wrong exact byte digest", func(_ *Review, auth *AuthenticatedPayload, _ *[]byte, _ *time.Time) { auth.BodySHA256[0]++ }},
		{"new current review", func(review *Review, _ *AuthenticatedPayload, _ *[]byte, _ *time.Time) {
			review.ExpiresAt = review.ExpiresAt.Add(time.Second)
		}},
		{"new current root", func(review *Review, _ *AuthenticatedPayload, _ *[]byte, _ *time.Time) { review.DirectoryInode++ }},
		{"new current route", func(review *Review, _ *AuthenticatedPayload, _ *[]byte, _ *time.Time) { review.Scope.MountNamespace++ }},
		{"future sampler time", func(_ *Review, _ *AuthenticatedPayload, _ *[]byte, now *time.Time) { *now = at.Add(time.Millisecond) }},
		{"wall rollback before begin", func(_ *Review, _ *AuthenticatedPayload, _ *[]byte, now *time.Time) { *now = at.Add(-time.Nanosecond) }},
		{"exact caller deadline", func(_ *Review, _ *AuthenticatedPayload, _ *[]byte, now *time.Time) { *now = at.Add(4 * time.Second) }},
		{"too large", func(_ *Review, auth *AuthenticatedPayload, raw *[]byte, _ *time.Time) {
			*raw = bytes.Repeat([]byte{'x'}, maxPayload+1)
			auth.BodySHA256 = sha256.Sum256(*raw)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := beginFixture(t, r, challenge, at, at.Add(4*time.Second))
			current, raw, now, auth := r, p, at.Add(2*time.Second), syntheticAuthenticated(r, p)
			tc.change(&current, &auth, &raw, &now)
			if result, ok := a.Accept(current, auth, raw, now); ok || result != (DisplayReading{}) {
				t.Fatal("invalid input returned data")
			}
			if _, ok := a.Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second)); ok {
				t.Fatal("a refused first response did not consume the attempt")
			}
		})
	}

	for _, tc := range []struct{ name, from, to string }{
		{"bad version", wireKind, "other/v1"},
		{"nested unknown", `"device_minor":2`, `"device_minor":2,"extra":null`},
		{"duplicate", `"total_bytes":8192`, `"total_bytes":8192,"total_bytes":8192`},
		{"field alias", `"total_bytes"`, `"TotalBytes"`},
		{"omitted zero", `"available_bytes":0,`, ""},
		{"null zero", `"available_bytes":0`, `"available_bytes":null`},
		{"floating representation", `"available_bytes":0`, `"available_bytes":0e0`},
		{"out of int64", `"total_bytes":8192`, `"total_bytes":9223372036854775808`},
		{"broken JSON", `"total_bytes":8192`, `"total_bytes":`},
		{"unknown field", `"available_bytes":0`, `"available_bytes":0,"mount_path":"/tmp"`},
		{"unrepresentable fragments", `"available_bytes":0`, `"available_bytes":1`},
		{"negative blocks", `"available_bytes":0`, `"available_bytes":-1024`},
		{"too many blocks", `"available_bytes":0`, `"available_bytes":9216`},
		{"too many inodes", `"available_inodes":17`, `"available_inodes":65`},
		{"different reviewed peer scope", `"mount_namespace":10`, `"mount_namespace":11`},
		{"different inode", `"directory_inode":123`, `"directory_inode":124`},
		{"different original review", r.ExpiresAt.Format(time.RFC3339Nano), r.ExpiresAt.Add(time.Second).Format(time.RFC3339Nano)},
		{"pre-attempt sample", at.Add(time.Second).Format(time.RFC3339Nano), at.Format(time.RFC3339Nano)},
		{"timestamp alias", at.Add(time.Second).Format(time.RFC3339Nano), strings.Replace(at.Add(time.Second).Format(time.RFC3339Nano), "Z", "+00:00", 1)},
		{"trailing data", `"available_inodes":17}`, `"available_inodes":17} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := beginFixture(t, r, challenge, at, at.Add(4*time.Second))
			bad := []byte(strings.Replace(string(p), tc.from, tc.to, 1))
			if bytes.Equal(bad, p) {
				t.Fatal("bad test fixture")
			}
			if result, ok := a.Accept(r, syntheticAuthenticated(r, bad), bad, at.Add(2*time.Second)); ok || result != (DisplayReading{}) {
				t.Fatal("invalid authenticated wire response returned data")
			}
		})
	}
}

func TestRemoteDiskDisplayCutsOffAndAcquiresAcrossAliases(t *testing.T) {
	at, r, s, challenge, _ := fixture(t)
	for _, invalid := range []func(*Review, *[32]byte, *time.Time){
		func(r *Review, _ *[32]byte, _ *time.Time) { r.PeerKeySHA256 = [32]byte{} },
		func(_ *Review, c *[32]byte, _ *time.Time) { *c = [32]byte{} },
		func(r *Review, _ *[32]byte, _ *time.Time) { r.DirectoryInode = 0 },
		func(r *Review, _ *[32]byte, at *time.Time) { r.ExpiresAt = *at },
	} {
		rr, cc, now := r, challenge, at
		invalid(&rr, &cc, &now)
		if a, ok := Begin(rr, cc, now, at.Add(time.Minute)); ok || a != nil {
			t.Fatal("invalid attempt began")
		}
	}
	defaultReview := r
	r.ExpiresAt = at.Add(3 * time.Second)
	p, ok := encode(challenge, s, r.DirectoryInode, r.ExpiresAt)
	if !ok {
		t.Fatal("short review refused")
	}
	if result, ok := beginFixture(t, r, challenge, at, at.Add(time.Minute)).Accept(r, syntheticAuthenticated(r, p), p, r.ExpiresAt); ok || result != (DisplayReading{}) {
		t.Fatal("exact review boundary accepted")
	}
	out, ok := beginFixture(t, r, challenge, at, at.Add(time.Minute)).Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second))
	if !ok || out.ExpiresAt != r.ExpiresAt {
		t.Fatal("display outlived review")
	}
	encoded, ok := encode(challenge, s, defaultReview.DirectoryInode, defaultReview.ExpiresAt)
	if !ok || !json.Valid(encoded) {
		t.Fatal("normal fixture did not encode")
	}
	if result, ok := beginFixture(t, defaultReview, challenge, at, at.Add(time.Minute)).Accept(defaultReview, syntheticAuthenticated(defaultReview, encoded), encoded, at.Add(maxAttempt)); ok || result != (DisplayReading{}) {
		t.Fatal("hard attempt boundary accepted")
	}
	a := beginFixture(t, r, challenge, at, at.Add(time.Minute))
	var won atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		alias := *a
		group.Go(func() {
			if _, ok := alias.Accept(r, syntheticAuthenticated(r, p), p, at.Add(2*time.Second)); ok {
				won.Add(1)
			}
		})
	}
	group.Wait()
	if won.Load() != 1 {
		t.Fatal("aliases did not consume exactly once", won.Load())
	}
}
