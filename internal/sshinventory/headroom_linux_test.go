package sshinventory

import (
	"context"
	"golang.org/x/crypto/ssh"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHeadroomProtocolFreshnessAndMissing(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Millisecond)
	wire := "garm-host-headroom-v1\n" + strconv.FormatInt(at.UnixMilli(), 10) + "\n1000\n0\n" + sample
	got, e := parseHeadroom([]byte(wire), at)
	if e != nil || got.CPUIdleBasisPoints == nil || *got.CPUIdleBasisPoints != 0 || !got.ObservedAt.Equal(at) {
		t.Fatal(got, e)
	}
	for _, bad := range []string{strings.Replace(wire, "1000\n0", "999\n0", 1), strings.Replace(wire, "1000\n0", "1000\n10001", 1), wire + "extra\n", sample} {
		if _, e := parseHeadroom([]byte(bad), at); e == nil {
			t.Fatal("invalid response accepted")
		}
	}
	if _, e := parseHeadroom([]byte(wire), at.Add(-10*time.Second)); e == nil {
		t.Fatal("future observation accepted")
	}
	got, e = parseHeadroom([]byte(strings.Replace(wire, "1000\n0\n", "7000\nunknown\n", 1)), at)
	if e != nil || got.CPUIdleBasisPoints != nil {
		t.Fatal("unknown became zero")
	}
}
func TestHeadroomPinAndOldAgentFailClosed(t *testing.T) {
	cfg, res, auth := server(t, sample)
	cfg.HostKey = string(ssh.MarshalAuthorizedKey(signer(t).PublicKey()))
	c, _ := New(cfg, res)
	if _, e := c.ReadHeadroom(context.Background()); e == nil || auth.Load() != 0 {
		t.Fatal("wrong key allowed")
	}
	cfg, res, _ = server(t, sample)
	c, _ = New(cfg, res)
	if _, e := c.ReadHeadroom(context.Background()); e == nil {
		t.Fatal("legacy protocol accepted")
	}
}

func TestPinnedHeadroomCommandEndToEnd(t *testing.T) {
	cfg, resolve, _ := server(t, "", false)
	c, e := New(cfg, resolve)
	if e != nil {
		t.Fatal(e)
	}
	before := time.Now().Add(-time.Second)
	got, e := c.ReadHeadroom(context.Background())
	if e != nil || got.ObservedAt.Before(before) || got.ObservedAt.After(time.Now()) || got.CPUWindowMillis < 1000 {
		t.Fatal(got, e)
	}
}
