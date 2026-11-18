//go:build linux

package nativeboundary

import "testing"

func TestOnlyOwnProcessBeforePlacement(t *testing.T) {
	if !onlyPID("123\n", 123) {
		t.Fatal("own")
	}
	for _, s := range []string{"", "123\n124\n", "124", "0123"} {
		if onlyPID(s, 123) {
			t.Fatal(s)
		}
	}
	if !controllers("cpu memory pids io") {
		t.Fatal("controllers")
	}
	if controllers("cpu memory") {
		t.Fatal("missingpids")
	}
	if PlaceOwner(Observation{}) == nil {
		t.Fatal("zero observation")
	}
}
