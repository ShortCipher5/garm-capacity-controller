//go:build linux

package protectedinit

import (
	"errors"
	"net"
	"testing"
)

func TestGuestNetworkFenceReadback(t *testing.T) {
	for _, mode := range []string{"ok", "write-error", "still-up", "replacement", "extra-before", "extra-after", "read-error"} {
		t.Run(mode, func(t *testing.T) {
			reads, writes := 0, 0
			read := func() ([]net.Interface, error) {
				reads++
				v := []net.Interface{{Name: "lo", Index: 1, Flags: net.FlagLoopback | net.FlagUp}, {Name: "eth0", Index: 2, Flags: net.FlagUp}}
				if reads > 1 {
					v[1].Flags = 0
					if mode == "still-up" {
						v[1].Flags = net.FlagUp
					}
					if mode == "replacement" {
						v[1].Index = 3
					}
				}
				if mode == "extra-before" || (mode == "extra-after" && reads > 1) {
					v = append(v, net.Interface{Name: "tun0", Index: 4})
				}
				if mode == "read-error" {
					return nil, errors.New("read")
				}
				return v, nil
			}
			down := func(name string) error {
				writes++
				if name != "eth0" {
					t.Fatal("wrong interface")
				}
				if mode == "write-error" {
					return errors.New("write")
				}
				return nil
			}
			e := closeGuestNetworkWith(read, down)
			if (e == nil) != (mode == "ok") {
				t.Fatal(mode, e)
			}
			if (mode == "extra-before" || mode == "read-error") && writes != 0 {
				t.Fatal("mutated before validated topology")
			}
		})
	}
}
