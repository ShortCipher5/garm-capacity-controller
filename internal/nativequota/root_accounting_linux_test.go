//go:build linux

package nativequota

import (
	"strings"
	"testing"
)

func TestRootResidualAccountingKeepsSharedBytes(t *testing.T) {
	raw := "tank/domain\tguid\t7\ntank/domain\ttype\tfilesystem\ntank/domain\tquota\t1000\ntank/domain\tused\t700\ntank/domain\tavailable\t200\ntank/domain\tusedbysnapshots\t100\ntank/domain\tusedbydataset\t200\ntank/domain\tusedbychildren\t300\ntank/domain\tusedbyrefreservation\t100\n"
	want := FilesystemIdentity{"tank/domain", "7", 1000}
	o, e := parseRootAccounting([]byte(raw), want)
	if e != nil || o.Used() != 700 || o.Available() != 200 || o.UsedBySnapshots() != 100 {
		t.Fatal(o, e)
	}
	for _, pair := range [][2]string{{"guid\t7", "guid\t8"}, {"quota\t1000", "quota\t2000"}, {"used\t700", "used\t699"}, {"available\t200", "available\t-1"}, {"usedbychildren\t300", "usedbychildren\t18446744073709551615"}} {
		if _, e := parseRootAccounting([]byte(strings.Replace(raw, pair[0], pair[1], 1)), want); e == nil {
			t.Fatal(pair)
		}
	}
}
