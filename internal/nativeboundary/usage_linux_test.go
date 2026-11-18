//go:build linux

package nativeboundary

import (
	"context"
	"testing"
	"time"
)

func TestUsageStrictCounters(t *testing.T) {
	for _, s := range []string{"-1\n", "max\n", "01\n", "1 \n", " 1\n", "9223372036854775808\n", "1\n2\n"} {
		if _, e := counter([]byte(s)); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"0\n", "1024\n"} {
		if _, e := counter([]byte(s)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestUsageCannotBeMintedFromZeroObservation(t *testing.T) {
	u, e := ObserveUsage(context.Background(), Observation{})
	if e == nil || u.Valid() {
		t.Fatal("synthetic usage")
	}
}

func TestUsageFreshness(t *testing.T) {
	for _, at := range []time.Time{time.Now().Add(-6 * time.Second), time.Now().Add(time.Second)} {
		if (Usage{namespace: 1, inode: 1, at: at}).Valid() {
			t.Fatal("stale/future usage")
		}
	}
	if !(Usage{namespace: 1, inode: 1, at: time.Now()}).Valid() {
		t.Fatal("fresh usage refused")
	}
}
