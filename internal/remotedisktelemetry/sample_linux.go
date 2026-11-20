// Package remotedisktelemetry defines an offline, display-only wire contract.
// It does not authenticate peers, establish routes, or provide scaler inputs.
package remotedisktelemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
)

const (
	wireKind      = "runner-dir-disk-display/v1"
	maxPayload    = 1536
	maxAttempt    = 15 * time.Second
	maxReadingAge = 30 * time.Second
)

// Review must come from an independently reviewed startup binding, not from
// any network response. PeerKeySHA256 identifies the telemetry peer; it is
// distinct from the Incus host scope and does not prove a route to that host.
type Review struct {
	Scope          hostinventory.DirFilesystemScope
	DirectoryInode uint64
	ExpiresAt      time.Time
	PeerKeySHA256  [32]byte
}

// AuthenticatedPayload is an assertion from a separately qualified transport,
// NOT evidence this package can authenticate. Only pass identity from the
// actual verified session, with a digest of its exact delivered body; never
// synthesize this from claimed wire fields or the expected Review.
type AuthenticatedPayload struct {
	PeerKeySHA256 [32]byte
	BodySHA256    [32]byte
}

type wireScope struct {
	Host           string `json:"host"`
	Pool           string `json:"pool"`
	Boot           string `json:"boot"`
	MountNamespace uint64 `json:"mount_namespace"`
	UniqueMountID  uint64 `json:"unique_mount_id"`
	DeviceMajor    uint32 `json:"device_major"`
	DeviceMinor    uint32 `json:"device_minor"`
}

func projectScope(s hostinventory.DirFilesystemScope) wireScope {
	return wireScope{s.HostFingerprint, s.StoragePool, s.BootID, s.MountNamespace, s.MountID, s.DeviceMajor, s.DeviceMinor}
}

type wireSample struct {
	Kind            string    `json:"kind"`
	Challenge       string    `json:"challenge"`
	Scope           wireScope `json:"scope"`
	DirectoryInode  uint64    `json:"directory_inode"`
	ReviewExpiresAt string    `json:"review_expires_at"`
	ObservedAt      string    `json:"observed_at"`
	TotalBytes      int64     `json:"total_bytes"`
	AvailableBytes  int64     `json:"available_bytes"`
	TotalInodes     int64     `json:"total_inodes"`
	AvailableInodes int64     `json:"available_inodes"`
}

func wireTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 }

func (s wireSample) validCounts() bool {
	// Every supported normalized fragment size is a multiple of 1024.
	return s.TotalBytes >= 1024 && s.TotalBytes%1024 == 0 &&
		s.AvailableBytes >= 0 && s.AvailableBytes <= s.TotalBytes && s.AvailableBytes%1024 == 0 &&
		s.TotalInodes > 0 && s.AvailableInodes >= 0 && s.AvailableInodes <= s.TotalInodes
}

// Encode projects only the existing opaque local descriptor observation. The
// receiver, not this exporter, validates the fresh challenge and its deadline.
// No paths, credentials, raw mount data or arbitrary caller counters enter it.
func Encode(challenge [32]byte, sample hostinventory.BoundDirFilesystemSample) ([]byte, bool) {
	s, inode, until, ok := sample.Evidence()
	if !ok {
		return nil, false
	}
	return encode(challenge, s, inode, until)
}

func encode(challenge [32]byte, s hostinventory.DirFilesystemSample, inode uint64, until time.Time) ([]byte, bool) {
	at, until := s.ObservedAt.UTC(), until.UTC()
	if challenge == ([32]byte{}) || !s.Scope.Valid() || inode == 0 ||
		!wireTime(at) || !wireTime(until) || !at.Before(until) {
		return nil, false
	}
	p := wireSample{wireKind, hex.EncodeToString(challenge[:]), projectScope(s.Scope), inode,
		until.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano),
		s.TotalBytes, s.AvailableBytes, s.TotalInodes, s.AvailableInodes}
	if !p.validCounts() {
		return nil, false
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > maxPayload {
		return nil, false
	}
	return raw, true
}

// Attempt owns a single expected response, even across copies of the handle.
type Attempt struct{ cell *attemptCell }

type attemptCell struct {
	taken               atomic.Bool
	review              Review
	challenge           [32]byte
	startedAt, deadline time.Time
}

// Begin creates a bounded attempt from trusted local wall time and a unique
// unpredictable challenge chosen by the future caller for this request. This
// package does not generate challenges, authenticate, or persist attempts.
// Remote timestamps outside the local request window are unknown; an eventual
// transport must qualify clock agreement rather than apply an implicit skew.
func Begin(review Review, challenge [32]byte, now, callerDeadline time.Time) (*Attempt, bool) {
	now, callerDeadline, review.ExpiresAt = now.UTC(), callerDeadline.UTC(), review.ExpiresAt.UTC()
	if !review.Scope.Valid() || review.DirectoryInode == 0 || review.PeerKeySHA256 == ([32]byte{}) ||
		challenge == ([32]byte{}) || !wireTime(now) || !wireTime(callerDeadline) || !wireTime(review.ExpiresAt) {
		return nil, false
	}
	deadline := earlier(earlier(now.Add(maxAttempt), callerDeadline), review.ExpiresAt)
	if !now.Before(deadline) {
		return nil, false
	}
	return &Attempt{cell: &attemptCell{review: review, challenge: challenge, startedAt: now, deadline: deadline}}, true
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// DisplayReading is only the bounded projection accepted at one instant. It
// is not a local sampler result or permission; do not display it after
// ExpiresAt or after withdrawal of its separately reviewed route.
type DisplayReading struct {
	ObservedAt, ExpiresAt        time.Time
	TotalBytes, AvailableBytes   int64
	TotalInodes, AvailableInodes int64
}

// Accept irrevocably takes the attempt, including on malformed or rejected
// input. The caller must recheck the independent current Review and pass the
// separately authenticated body assertion. Its byte slice must not be mutated
// concurrently with this call. Only canonical bounded bytes are accepted.
func (a *Attempt) Accept(current Review, authenticated AuthenticatedPayload, raw []byte, now time.Time) (DisplayReading, bool) {
	var none DisplayReading
	if a == nil || a.cell == nil || !a.cell.taken.CompareAndSwap(false, true) {
		return none, false
	}
	cell := a.cell
	now, current.ExpiresAt = now.UTC(), current.ExpiresAt.UTC()
	if current != cell.review || !wireTime(now) || now.Before(cell.startedAt) || !now.Before(cell.deadline) ||
		authenticated.PeerKeySHA256 != current.PeerKeySHA256 || len(raw) == 0 || len(raw) > maxPayload {
		return none, false
	}
	owned := bytes.Clone(raw)
	if sha256.Sum256(owned) != authenticated.BodySHA256 {
		return none, false
	}
	var p wireSample
	if json.Unmarshal(owned, &p) != nil {
		return none, false
	}
	// A canonical re-encoding rejects unknown, duplicate, aliased, omitted
	// and noncanonical fields without relying on last-key-wins JSON order.
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, owned) || p.Kind != wireKind ||
		p.Challenge != hex.EncodeToString(cell.challenge[:]) || p.Scope != projectScope(current.Scope) ||
		p.DirectoryInode != current.DirectoryInode || p.ReviewExpiresAt != current.ExpiresAt.Format(time.RFC3339Nano) || !p.validCounts() {
		return none, false
	}
	at, err := time.Parse(time.RFC3339Nano, p.ObservedAt)
	if err != nil || !wireTime(at) || at.UTC().Format(time.RFC3339Nano) != p.ObservedAt ||
		!at.After(cell.startedAt) || at.After(now) {
		return none, false
	}
	expiry := earlier(at.Add(maxReadingAge), current.ExpiresAt)
	if !wireTime(expiry) || !now.Before(expiry) {
		return none, false
	}
	return DisplayReading{at.UTC(), expiry, p.TotalBytes, p.AvailableBytes, p.TotalInodes, p.AvailableInodes}, true
}
