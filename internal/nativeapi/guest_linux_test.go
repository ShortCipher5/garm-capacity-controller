//go:build linux

package nativeapi

import (
	"context"
	"testing"
)

func TestGuestStateRequiresRunningPID(t *testing.T) {
	for _, raw := range []string{`{"type":"sync","status_code":200,"metadata":{"status":"Running","status_code":103,"pid":25}}`, `{"type":"sync","status_code":200,"metadata":{"status":"Stopped","status_code":102,"pid":25}}`, `{"type":"sync","status_code":200,"metadata":{"status":"Running","status_code":103,"pid":1}}`, `{"type":"sync","status_code":200,"metadata":{"status":"Running","status_code":103,"pid":null}}`} {
		pid, e := parseGuestPID([]byte(raw))
		if raw == `{"type":"sync","status_code":200,"metadata":{"status":"Running","status_code":103,"pid":25}}` {
			if e != nil || pid != 25 {
				t.Fatal(pid, e)
			}
		} else if e == nil {
			t.Fatal(raw)
		}
	}
}
func TestGuestIdentityCannotBeFabricated(t *testing.T) {
	var c *Client
	if _, e := c.ObserveGuestIncarnation(context.Background(), Prepared{}, "12345678-1234-1234-1234-123456789abc"); e == nil {
		t.Fatal("fabricated")
	}
	if _, e := c.ObserveGuestExit(context.Background(), Prepared{}, GuestIncarnation{}); e == nil {
		t.Fatal("fabricated exit")
	}
}
