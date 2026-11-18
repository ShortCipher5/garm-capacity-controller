//go:build linux

package nativeids

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDedicatedRangesRejectOverlapsAndHostRoot(t *testing.T) {
	good := "other:100000:65536\ngarm-capacity-native:100000000:33554432\n"
	p, e := assignments([]byte(good))
	if e != nil || p.start != 100000000 {
		t.Fatal(p, e)
	}
	for _, s := range []string{"garm-capacity-native:0:33554432", good + "intruder:100000001:1\n", good + "garm-capacity-native:200000000:16777216\n", "garm-capacity-native:4290000000:33554432", "garm-capacity-native:100000000:65536", "garm-capacity-native:0100000000:33554432"} {
		if _, e := assignments([]byte(s)); e == nil {
			t.Fatal("accepted", s)
		}
	}
}
func TestUnknownReservationStillConsumesRange(t *testing.T) {
	pool := interval{100000000, 2 * BlockSize}
	r := Record{1, strings.Repeat("a", 64), pool.start, pool.start, BlockSize, time.Now().UTC()}
	if !valid(r) {
		t.Fatal("fixture")
	}
	got, e := next(pool, []Record{r}, true)
	if e != nil || got != pool.start+BlockSize {
		t.Fatal(got, e)
	}
	r2 := r
	r2.UIDStart += BlockSize
	if _, e = next(pool, []Record{r, r2}, true); e == nil {
		t.Fatal("reused consumed range")
	}
}
func TestReservationRefusesUnprivilegedHostEffects(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("rootless refusal")
	}
	if _, e := Reserve(t.TempDir()); e == nil {
		t.Fatal("accepted nonroot")
	}
}

func TestLocalAccountsAndOverflowRejected(t *testing.T) {
	p := interval{100000000, BlockSize}
	if rejectLocalIDs([]byte("root:x:0:0:root:/root:/bin/sh\n"), p, 7, 2) != nil {
		t.Fatal("root outside range")
	}
	if rejectLocalIDs([]byte("collision:x:100000001:0:x:/home/x:/bin/sh\n"), p, 7, 2) == nil {
		t.Fatal("host account overlaps")
	}
	r := Record{1, strings.Repeat("a", 64), ^uint64(0), 100000000, BlockSize, time.Now().UTC()}
	if valid(r) {
		t.Fatal("overflow bypass")
	}
}

func TestCopiesCannotAuthorizeSecondConstruction(t *testing.T) {
	r := Reservation{Record{1, strings.Repeat("a", 64), 100000000, 100000000, BlockSize, time.Now().UTC()}, new(atomic.Bool)}
	copy := r
	if _, e := r.Claim(); e != nil {
		t.Fatal(e)
	}
	if _, e := copy.Claim(); e == nil {
		t.Fatal("copy replayed range")
	}
	if _, e := (Reservation{}).Claim(); e == nil {
		t.Fatal("zero handle")
	}
}

func TestPasswdPrimaryGIDMayExistWithoutGroupEntry(t *testing.T) {
	p := interval{100000000, BlockSize}
	raw := []byte("host-user:x:2000:100000123:host:/home/host:/bin/sh\n")
	if rejectLocalIDs(raw, p, 7, 2) != nil {
		t.Fatal("UID fixture should be outside range")
	}
	if rejectLocalIDs(raw, p, 7, 3) == nil {
		t.Fatal("primary GID collision missed")
	}
}
