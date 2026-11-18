package nativedns

import "testing"

func TestResolverBytes(t *testing.T) {
	b, e := Content([]string{"1.1.1.1", "8.8.8.8"})
	if e != nil || string(b) != "nameserver 1.1.1.1\nnameserver 8.8.8.8\n" {
		t.Fatal(e)
	}
	for _, v := range [][]string{nil, {"127.0.0.1"}, {"10.1.1.1"}, {"169.254.169.254"}, {"100.64.0.1"}, {"::1"}, {"1.1.1.1", "1.1.1.1"}, {"1.1.1.1\nsearch evil"}} {
		if _, e := Content(v); e == nil {
			t.Fatal(v)
		}
	}
}
