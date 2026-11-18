//go:build linux

package nativecapsule

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativezfs"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunningZeroRefusesAuthority(t *testing.T) {
	for _, r := range []*Running{nil, {}} {
		if r.BirthID() != "" || r.LauncherSHA256() != "" || r.ClaimClient() {
			t.Fatal("zero identity")
		}
		if r.CheckScope(context.Background()) == nil {
			t.Fatal("zero scope accepted")
		}
		req, _ := http.NewRequest("GET", "http://native.invalid/1.0", nil)
		if _, err := r.RoundTrip(req); err == nil {
			t.Fatal("zero transport accepted")
		}
	}
	var s *Staged
	if _, err := s.Finish(context.Background(), nativezfs.Birth{}, strings.Repeat("a", 64), strings.Repeat("b", 64), nativeegress.Birth{}); err == nil {
		t.Fatal("zero stage accepted")
	}
}
func TestNspawnMachineFitsFullBirth(t *testing.T) {
	b := strings.Repeat("a", 64)
	args, err := args(launchSpec{Image: "/root/image.raw", Socket: "/root/birth/bootstrap.sock", Birth: b, Start: 1 << 24, Count: 1 << 24})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range args {
		if strings.HasPrefix(a, "--machine=") {
			found = true
			if strings.TrimPrefix(a, "--machine=") != b {
				t.Fatal(a)
			}
		}
	}
	if !found {
		t.Fatal("missing machine")
	}
}

func TestRunningCopiesShareClaimState(t *testing.T) {
	original := Running{clientClaimed: new(atomic.Bool)}
	copied := original
	if !original.clientClaimed.CompareAndSwap(false, true) || copied.clientClaimed.CompareAndSwap(false, true) {
		t.Fatal("copy gained repeated claim")
	}
	later := original
	if later.clientClaimed.CompareAndSwap(false, true) {
		t.Fatal("postclaim copy gained claim")
	}
}

func TestNspawnVersionRefusesOtherMajor(t *testing.T) {
	for _, v := range []string{"systemd 257\n+FEATURES\n", "systemd 257 (257.9-1)\n"} {
		if !supportedNspawn([]byte(v)) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"systemd 256\n", "systemd 2570\n", "systemd 258 (258)\n", ""} {
		if supportedNspawn([]byte(v)) {
			t.Fatal(v)
		}
	}
}
