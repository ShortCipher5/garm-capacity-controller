package sshinventory

import (
	"bytes"
	"github.com/ShortCipher5/garm-capacity-controller/internal/hostinventory"
	"testing"
)

func TestRestrictedHostCommandProtocol(t *testing.T) {
	var out bytes.Buffer
	if e := hostinventory.Write(&out, hostinventory.Collect()); e != nil {
		t.Fatal(e)
	}
	got, e := parse(out.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	if got.PhysicalCapacityQualified {
		t.Fatal("OS view qualified physical capacity")
	}
}
